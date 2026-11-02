package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Deployer struct {
	Docker   DockerManager
	Git      *GitManager
	Root     string
	BuildTTL time.Duration
	RunTTL   time.Duration
	Audit    string
	lockMu   sync.Mutex
	appLocks map[string]*appLock
}

type appLock struct {
	mutex sync.Mutex
	refs  int
}

func (d *Deployer) lockFor(app string) func() {
	d.lockMu.Lock()
	if d.appLocks == nil {
		d.appLocks = make(map[string]*appLock)
	}
	if d.appLocks[app] == nil {
		d.appLocks[app] = &appLock{}
	}
	lock := d.appLocks[app]
	lock.refs++
	d.lockMu.Unlock()
	lock.mutex.Lock()
	return func() {
		lock.mutex.Unlock()
		d.lockMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(d.appLocks, app)
		}
		d.lockMu.Unlock()
	}
}

func (d *Deployer) Deploy(ctx context.Context, req DeployRequest, j *job) {
	ctx, cancelDeploy := context.WithTimeout(ctx, time.Hour)
	defer cancelDeploy()
	app := req.Manifest.AppName
	result, commit := "FAILED", ""
	defer func() {
		snapshot := j.view()
		d.audit(snapshot.ID, app, snapshot.RequestedBy, commit, result)
	}()

	source, err := d.sourcePath(req.Manifest)
	if err != nil {
		j.update(JobFailed, err.Error())
		return
	}
	unlockApp := d.lockFor("app:" + app)
	defer unlockApp()
	unlockPath := d.lockFor("path:" + source)
	defer unlockPath()
	if err := d.Docker.CheckAvailable(ctx); err != nil {
		j.update(JobFailed, err.Error())
		return
	}
	j.update(JobCloning, "synchronizing GitHub repository")
	commit, err = d.Git.Sync(ctx, req.Git, source, j)
	if err != nil {
		j.update(JobFailed, err.Error())
		return
	}
	dockerfilePath := filepath.Join(source, "Dockerfile")
	if req.Manifest.Dockerfile != "" {
		if info, statErr := os.Lstat(dockerfilePath); statErr == nil {
			if info.IsDir() {
				j.update(JobFailed, "Dockerfile path is a directory")
				return
			}
			if err := os.Remove(dockerfilePath); err != nil {
				j.update(JobFailed, "could not replace Dockerfile: "+err.Error())
				return
			}
		} else if !os.IsNotExist(statErr) {
			j.update(JobFailed, "could not inspect Dockerfile: "+statErr.Error())
			return
		}
		if err := os.WriteFile(dockerfilePath, []byte(req.Manifest.Dockerfile), 0644); err != nil {
			j.update(JobFailed, "could not write Dockerfile: "+err.Error())
			return
		}
	}
	dockerfile, err := os.Stat(dockerfilePath)
	if err != nil || dockerfile.IsDir() {
		j.update(JobFailed, "Dockerfile not found in source directory")
		return
	}
	ip, err := discoverNetworkIP(req.Network)
	if err != nil {
		j.update(JobFailed, err.Error())
		return
	}

	tag := fmt.Sprintf("%s:%d-%s", strings.ToLower(app), time.Now().UnixNano(), commit)
	buildCtx, cancel := context.WithTimeout(ctx, d.BuildTTL)
	defer cancel()
	j.update(JobBuilding, "building image "+tag)
	if err := d.Docker.BuildImage(buildCtx, source, tag, j); err != nil {
		j.update(JobFailed, err.Error())
		return
	}

	envFile, cleanup, err := writeEnvFile(req.Manifest.Environment)
	if err != nil {
		j.update(JobFailed, err.Error())
		return
	}
	defer cleanup()
	ip, err = discoverNetworkIP(req.Network)
	if err != nil {
		j.update(JobFailed, err.Error())
		return
	}
	oldImage, hadOld, err := d.Docker.ContainerImage(ctx, app)
	if err != nil {
		j.update(JobFailed, err.Error())
		return
	}
	var oldEnv map[string]string
	if hadOld {
		oldEnv, err = d.Docker.ContainerEnvironment(ctx, app)
		if err != nil {
			j.update(JobFailed, err.Error())
			return
		}
	}
	if hadOld {
		if err := d.Docker.StopContainer(ctx, app); err != nil {
			j.update(JobFailed, err.Error())
			return
		}
		if err := d.Docker.RemoveContainer(ctx, app); err != nil {
			j.update(JobFailed, err.Error())
			return
		}
	}
	j.update(JobRunning, "starting new container")
	runCtx, runCancel := context.WithTimeout(ctx, d.RunTTL)
	err = d.Docker.RunContainer(runCtx, req.Manifest, tag, ip, envFile)
	if err == nil {
		err = waitContainerRunning(runCtx, d.Docker, app)
	}
	if err == nil {
		err = waitHealthy(runCtx, ip, req.Manifest.HostPort, req.Manifest.HealthcheckPath)
	}
	runCancel()
	if err != nil {
		if hadOld {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = d.Docker.StopContainer(cleanupCtx, app)
			_ = d.Docker.RemoveContainer(cleanupCtx, app)
			cleanupCancel()
			rollbackEnv, rollbackCleanup, envErr := writeEnvFile(oldEnv)
			if envErr != nil {
				j.update(JobFailed, fmt.Sprintf("new container failed: %v; could not prepare rollback environment: %v", err, envErr))
				return
			}
			defer rollbackCleanup()
			rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), d.RunTTL)
			rollbackErr := d.Docker.RunContainer(rollbackCtx, req.Manifest, oldImage, ip, rollbackEnv)
			if rollbackErr == nil {
				rollbackErr = waitContainerRunning(rollbackCtx, d.Docker, app)
			}
			rollbackCancel()
			if rollbackErr == nil {
				result = "ROLLED_BACK"
				j.update(JobRolledBack, "new container failed; previous image restored: "+err.Error())
				return
			}
			j.update(JobFailed, fmt.Sprintf("new container failed: %v; rollback failed: %v", err, rollbackErr))
			return
		}
		j.update(JobFailed, err.Error())
		return
	}
	result = "SUCCESS"
	j.update(JobSuccess, "container is running with image "+tag)
}

func waitContainerRunning(ctx context.Context, docker DockerManager, app string) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		running, err := docker.ContainerRunning(ctx, app)
		if err != nil {
			return err
		}
		if running {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("container did not stay running: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (d *Deployer) sourcePath(m AppManifest) (string, error) {
	if len(m.AppName) > 63 || !appNamePattern.MatchString(m.AppName) {
		return "", fmt.Errorf("invalid app_name")
	}
	if m.ContainerPort == 0 || m.HostPort == 0 {
		return "", fmt.Errorf("host_port and container_port must be between 1 and 65535")
	}
	if m.RestartPolicy != "" && m.RestartPolicy != "no" && m.RestartPolicy != "always" && m.RestartPolicy != "unless-stopped" && m.RestartPolicy != "on-failure" {
		return "", fmt.Errorf("invalid restart_policy")
	}
	if m.HealthcheckPath != "" && (!strings.HasPrefix(m.HealthcheckPath, "/") || strings.ContainsAny(m.HealthcheckPath, "\r\n")) {
		return "", fmt.Errorf("healthcheck_path must start with /")
	}
	if d.Root == "" {
		return "", fmt.Errorf("workspace root is not configured")
	}
	root, err := filepath.Abs(d.Root)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	path := m.SourcePath
	if path == "" {
		path = m.AppName
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("source_path must stay under WORKSPACE_ROOT")
	}
	ancestor := path
	for {
		resolved, resolveErr := filepath.EvalSymlinks(ancestor)
		if resolveErr == nil {
			realRel, relErr := filepath.Rel(root, resolved)
			if relErr != nil || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) {
				return "", fmt.Errorf("source_path resolves outside WORKSPACE_ROOT")
			}
			break
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("source_path has no safe existing parent")
		}
		ancestor = parent
	}
	return path, nil
}

func (d *Deployer) audit(jobID, app, actor, commit, result string) {
	record := struct {
		JobID       string    `json:"job_id"`
		AppName     string    `json:"app_name"`
		RequestedBy string    `json:"requested_by"`
		Commit      string    `json:"commit"`
		Result      string    `json:"result"`
		At          time.Time `json:"at"`
	}{jobID, app, actor, commit, result, time.Now().UTC()}
	b, _ := json.Marshal(record)
	f, err := os.OpenFile(d.Audit, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}
