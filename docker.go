package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type DockerManager interface {
	CheckAvailable(context.Context) error
	BuildImage(context.Context, string, string, io.Writer) error
	ContainerImage(context.Context, string) (string, bool, error)
	ContainerEnvironment(context.Context, string) (map[string]string, error)
	ContainerRunning(context.Context, string) (bool, error)
	StopContainer(context.Context, string) error
	RemoveContainer(context.Context, string) error
	RunContainer(context.Context, AppManifest, string, string, string) error
	StreamLogs(context.Context, string, io.Writer) error
}

type CLIDockerManager struct{}

func (d *CLIDockerManager) CheckAvailable(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("Docker daemon unavailable: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func (d *CLIDockerManager) BuildImage(ctx context.Context, source, tag string, out io.Writer) error {
	cmd := exec.CommandContext(ctx, "docker", "build", "--tag", tag, source)
	cmd.Stdout, cmd.Stderr = out, out
	return commandError("docker build", cmd.Run())
}

func (d *CLIDockerManager) ContainerImage(ctx context.Context, name string) (string, bool, error) {
	cmd := exec.CommandContext(ctx, "docker", "container", "inspect", "--format", "{{.Config.Image}}", name)
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			message := strings.ToLower(string(exit.Stderr))
			if strings.Contains(message, "no such object") || strings.Contains(message, "no such container") {
				return "", false, nil
			}
		}
		return "", false, commandError("docker inspect", err)
	}
	return strings.TrimSpace(string(out)), true, nil
}

func (d *CLIDockerManager) ContainerEnvironment(ctx context.Context, name string) (map[string]string, error) {
	cmd := exec.CommandContext(ctx, "docker", "container", "inspect", "--format", "{{json .Config.Env}}", name)
	out, err := cmd.Output()
	if err != nil {
		return nil, commandError("docker inspect environment", err)
	}
	var entries []string
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, err
	}
	values := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	return values, nil
}

func (d *CLIDockerManager) ContainerRunning(ctx context.Context, name string) (bool, error) {
	cmd := exec.CommandContext(ctx, "docker", "container", "inspect", "--format", "{{.State.Running}}", name)
	out, err := cmd.Output()
	if err != nil {
		return false, commandError("docker inspect state", err)
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

func (d *CLIDockerManager) StopContainer(ctx context.Context, name string) error {
	check := exec.CommandContext(ctx, "docker", "container", "inspect", "--format", "{{.State.Running}}", name)
	state, err := check.Output()
	if err != nil {
		return commandError("docker inspect", err)
	}
	if strings.TrimSpace(string(state)) != "true" {
		return nil
	}
	cmd := exec.CommandContext(ctx, "docker", "stop", "--time", "20", name)
	return commandError("docker stop", cmd.Run())
}

func (d *CLIDockerManager) RemoveContainer(ctx context.Context, name string) error {
	cmd := exec.CommandContext(ctx, "docker", "rm", name)
	return commandError("docker rm", cmd.Run())
}

func (d *CLIDockerManager) RunContainer(ctx context.Context, manifest AppManifest, image, ip, envFile string) error {
	policy := manifest.RestartPolicy
	if policy == "" {
		policy = "unless-stopped"
	}
	args := []string{"run", "-d", "--name", manifest.AppName, "--restart", policy}
	if manifest.MemoryLimit != "" {
		args = append(args, "--memory", manifest.MemoryLimit)
	}
	if manifest.CPULimit != "" {
		args = append(args, "--cpus", manifest.CPULimit)
	}
	args = append(args, "--publish", fmt.Sprintf("%s:%d:%d", ip, manifest.HostPort, manifest.ContainerPort), "--env-file", envFile, image)
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker run: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (d *CLIDockerManager) StreamLogs(ctx context.Context, name string, out io.Writer) error {
	cmd := exec.CommandContext(ctx, "docker", "logs", "--follow", "--timestamps", name)
	cmd.Stdout, cmd.Stderr = out, out
	return cmd.Run()
}

func commandError(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", op, err)
}

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func writeEnvFile(values map[string]string) (string, func(), error) {
	f, err := os.CreateTemp("", "deploy-env-*.env")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	if err := f.Chmod(0600); err != nil {
		f.Close()
		cleanup()
		return "", nil, err
	}
	w := bufio.NewWriter(f)
	for key, value := range values {
		if !envKey.MatchString(key) || strings.ContainsAny(value, "\r\n\x00") {
			f.Close()
			cleanup()
			return "", nil, fmt.Errorf("invalid environment variable %q (values cannot contain newlines)", key)
		}
		if _, err := fmt.Fprintf(w, "%s=%s\n", key, value); err != nil {
			f.Close()
			cleanup()
			return "", nil, err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		cleanup()
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return f.Name(), cleanup, nil
}

func discoverNetworkIP(cfg NetworkConfig) (string, error) {
	if cfg.Type != "tailscale" && cfg.Type != "wireguard" {
		return "", fmt.Errorf("network.type must be tailscale or wireguard")
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		matches := iface.Name == cfg.Interface
		if cfg.Interface == "" {
			if cfg.Type == "tailscale" {
				matches = iface.Name == "tailscale0" || strings.HasPrefix(iface.Name, "utun")
			}
			if cfg.Type == "wireguard" {
				matches = iface.Name == "wg0" || strings.HasPrefix(iface.Name, "wg")
			}
		}
		if !matches {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			ip = ip.To4()
			if ip == nil {
				continue
			}
			if cfg.Type == "tailscale" && !(&net.IPNet{IP: net.IP{100, 64, 0, 0}, Mask: net.CIDRMask(10, 32)}).Contains(ip) {
				continue
			}
			if cfg.Type == "wireguard" && !ip.IsPrivate() {
				continue
			}
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("no usable %s interface/IP found", cfg.Type)
}

func waitHealthy(ctx context.Context, ip string, port uint16, path string) error {
	if path == "" {
		return nil
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\r\n") {
		return fmt.Errorf("healthcheck_path must start with /")
	}
	client := &http.Client{Timeout: 3 * time.Second}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	url := fmt.Sprintf("http://%s:%d%s", ip, port, path)
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("health check timed out: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
