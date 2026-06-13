// Port of lume's SSH/SystemSSHClient.swift: an SSH client that delegates to
// the system's /usr/bin/ssh binary. Used as a fallback when the in-process
// client cannot establish a TCP connection (e.g. sandboxed environments
// where only system-signed binaries can reach vmnet interfaces). Password
// authentication is provided non-interactively via SSH_ASKPASS.
//go:build darwin

package ssh

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// SystemSSHClient mirrors SSHClient but shells out to /usr/bin/ssh.
type SystemSSHClient struct {
	Host     string
	Port     uint16
	User     string
	Password string
}

func NewSystemSSHClient(host string, port uint16, user string, password string) *SystemSSHClient {
	return &SystemSSHClient{Host: host, Port: port, User: user, Password: password}
}

func (c *SystemSSHClient) sshArguments(extraArgs ...string) []string {
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=10",
	}
	if c.Port != 22 {
		args = append(args, "-p", fmt.Sprintf("%d", c.Port))
	}
	return append(args, extraArgs...)
}

// createAskpassScript writes a temporary script that prints the password for
// SSH_ASKPASS. The caller must remove the returned path.
func (c *SystemSSHClient) createAskpassScript() (string, error) {
	file, err := os.CreateTemp("", "weave-askpass-*.sh")
	if err != nil {
		return "", ErrSSHConnectionFailed("Failed to create askpass script")
	}
	escaped := strings.ReplaceAll(c.Password, "'", `'\''`)
	script := fmt.Sprintf("#!/bin/sh\necho '%s'\n", escaped)
	if _, err := file.WriteString(script); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return "", ErrSSHConnectionFailed("Failed to create askpass script")
	}
	if err := file.Chmod(0o700); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return "", ErrSSHConnectionFailed("Failed to create askpass script")
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(file.Name())
		return "", ErrSSHConnectionFailed("Failed to create askpass script")
	}
	return file.Name(), nil
}

func (c *SystemSSHClient) askpassEnv(askpassPath string) []string {
	return append(os.Environ(),
		"SSH_ASKPASS="+askpassPath,
		"SSH_ASKPASS_REQUIRE=force",
		"DISPLAY=:0",
	)
}

// Execute runs a command on the remote host using system ssh.
func (c *SystemSSHClient) Execute(ctx context.Context, command string, timeout time.Duration) (SSHResult, error) {
	askpassPath, err := c.createAskpassScript()
	if err != nil {
		return SSHResult{}, err
	}
	defer os.Remove(askpassPath)

	runCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	args := c.sshArguments(fmt.Sprintf("%s@%s", c.User, c.Host), command)
	cmd := exec.CommandContext(runCtx, "/usr/bin/ssh", args...)
	cmd.Env = c.askpassEnv(askpassPath)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Detach stdin from the terminal so SSH_ASKPASS is honoured.
	cmd.Stdin = nil

	runErr := cmd.Run()
	if runCtx.Err() != nil {
		return SSHResult{}, ErrSSHTimeout()
	}

	// Filter known-hosts warnings from stderr, as SystemSSHClient.swift does.
	var filtered []string
	for _, line := range strings.Split(stderr.String(), "\n") {
		if strings.Contains(line, "Warning: Permanently added") ||
			strings.Contains(line, "known_hosts") ||
			strings.TrimSpace(line) == "" {
			continue
		}
		filtered = append(filtered, line)
	}
	combined := stdout.String()
	if len(filtered) > 0 {
		combined += strings.Join(filtered, "\n")
	}

	exitCode := int32(0)
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		exitCode = int32(exitErr.ExitCode())
	} else if runErr != nil {
		return SSHResult{}, ErrSSHConnectionFailed(runErr.Error())
	}

	return SSHResult{ExitCode: exitCode, Output: combined}, nil
}

// Interactive starts an interactive session using system ssh with the local
// terminal passed straight through.
func (c *SystemSSHClient) Interactive(ctx context.Context) error {
	askpassPath, err := c.createAskpassScript()
	if err != nil {
		return err
	}
	defer os.Remove(askpassPath)

	args := c.sshArguments("-t", fmt.Sprintf("%s@%s", c.User, c.Host))
	cmd := exec.CommandContext(ctx, "/usr/bin/ssh", args...)
	cmd.Env = c.askpassEnv(askpassPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return ErrSSHConnectionFailed(fmt.Sprintf("System SSH exited with code %d", exitErr.ExitCode()))
		}
		return ErrSSHConnectionFailed(err.Error())
	}
	return nil
}
