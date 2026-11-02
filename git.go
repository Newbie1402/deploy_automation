package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type GitManager struct{}

var appNamePattern = regexp.MustCompile(`^[a-zA-Z0-9]+([._-][a-zA-Z0-9]+)*$`)
var sshRepoPattern = regexp.MustCompile(`^git@github[.]com:[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+([.]git)?$`)

func (g *GitManager) Sync(ctx context.Context, cfg GitConfig, target string, out io.Writer) (string, error) {
	if err := validateGit(cfg); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(target, ".git")); os.IsNotExist(err) {
		if err := runGit(ctx, cfg, out, "clone", "--branch", cfg.Branch, "--single-branch", "--", cfg.Repository, target); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	} else {
		if err := runGit(ctx, cfg, out, "-C", target, "remote", "set-url", "origin", cfg.Repository); err != nil {
			return "", err
		}
		if err := runGit(ctx, cfg, out, "-C", target, "fetch", "--prune", "origin", cfg.Branch); err != nil {
			return "", err
		}
		if err := runGit(ctx, cfg, out, "-C", target, "reset", "--hard", "FETCH_HEAD"); err != nil {
			return "", err
		}
		if err := runGit(ctx, cfg, out, "-C", target, "clean", "-fdx"); err != nil {
			return "", err
		}
	}
	cmd := exec.CommandContext(ctx, "git", "-C", target, "rev-parse", "--short=8", "HEAD")
	hash, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w", err)
	}
	return strings.TrimSpace(string(hash)), nil
}

func validateGit(cfg GitConfig) error {
	if strings.ContainsAny(cfg.Token, "\r\n\x00") {
		return fmt.Errorf("invalid GitHub token")
	}
	if cfg.Branch == "" || strings.HasPrefix(cfg.Branch, "-") {
		return fmt.Errorf("git.branch is required")
	}
	if err := exec.Command("git", "check-ref-format", "--branch", cfg.Branch).Run(); err != nil {
		return fmt.Errorf("invalid git branch")
	}
	if strings.HasPrefix(cfg.Repository, "git@github.com:") {
		if !sshRepoPattern.MatchString(cfg.Repository) {
			return fmt.Errorf("invalid GitHub SSH repository URL")
		}
		if cfg.Token != "" {
			return fmt.Errorf("token authentication requires an HTTPS GitHub repository URL")
		}
		if cfg.SSHKeyPath != "" && !filepath.IsAbs(cfg.SSHKeyPath) {
			return fmt.Errorf("git.ssh_key_path must be absolute")
		}
		return nil
	}
	if cfg.SSHKeyPath != "" {
		return fmt.Errorf("ssh_key_path requires a git@github.com repository URL")
	}
	u, err := url.Parse(cfg.Repository)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("git.repository must be an HTTPS GitHub URL or git@github.com URL")
	}
	return nil
}

func runGit(ctx context.Context, cfg GitConfig, out io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Env = make([]string, 0, len(os.Environ())+4)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GIT_CONFIG_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	if cfg.Token != "" {
		auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + cfg.Token))
		cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader", "GIT_CONFIG_VALUE_0=AUTHORIZATION: basic "+auth)
	}
	if cfg.SSHKeyPath != "" {
		if strings.ContainsAny(cfg.SSHKeyPath, "\r\n") {
			return fmt.Errorf("invalid ssh_key_path")
		}
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -i "+shellQuote(cfg.SSHKeyPath)+" -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes")
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w", args[0], err)
	}
	return nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
