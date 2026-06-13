// Port of lume's Unattended/HealthCheckRunner.swift: verify a freshly
// set-up VM via SSH or HTTP probes with retries, and run post-setup
// commands over SSH (more reliable than typing into Terminal via VNC).
//go:build darwin

package unattended

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	weavessh "github.com/deploymenttheory/weave/internal/ssh"
)

// RunHealthCheck probes the VM at ip per the check definition.
func RunHealthCheck(ctx context.Context, check *UnattendedHealthCheck, ip string) error {
	timeout := check.Timeout
	if timeout == 0 {
		timeout = 30
	}
	retries := check.Retries
	if retries == 0 {
		retries = 3
	}
	retryDelay := check.RetryDelay
	if retryDelay == 0 {
		retryDelay = 5
	}

	var lastErr error
	for attempt := 1; attempt <= retries; attempt++ {
		fmt.Printf("Health check attempt %d/%d (%s)...\n", attempt, retries, check.Type)

		switch check.Type {
		case "ssh":
			user := check.User
			if user == "" {
				user = "weave"
			}
			password := check.Password
			if password == "" {
				password = "weave"
			}
			client := weavessh.NewSSHClient(ip, 22, user, password)
			result, err := client.Execute(ctx, "echo ok", time.Duration(timeout)*time.Second)
			if err == nil && result.ExitCode == 0 && strings.Contains(result.Output, "ok") {
				fmt.Println("SSH health check passed")
				return nil
			}
			if err != nil {
				lastErr = err
			} else {
				lastErr = fmt.Errorf("ssh probe exited with code %d", result.ExitCode)
			}

		case "http":
			client := &http.Client{Timeout: time.Duration(timeout) * time.Second}
			response, err := client.Get(fmt.Sprintf("http://%s/", ip))
			if err == nil {
				_ = response.Body.Close()
				fmt.Printf("HTTP health check passed (status %d)\n", response.StatusCode)
				return nil
			}
			lastErr = err

		default:
			return weaveerrors.ErrHealthCheckFailed(fmt.Sprintf("unknown health check type %q", check.Type))
		}

		if attempt < retries {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(retryDelay) * time.Second):
			}
		}
	}

	return weaveerrors.ErrHealthCheckFailed(fmt.Sprintf("%s probe failed after %d attempts: %v", check.Type, retries, lastErr))
}

// RunPostSSHCommands executes each command over SSH, printing its output.
func RunPostSSHCommands(ctx context.Context, commands []string, ip string, user string, password string) error {
	client := weavessh.NewSSHClient(ip, 22, user, password)
	for index, command := range commands {
		fmt.Printf("Post-setup command %d/%d: %s\n", index+1, len(commands), command)
		result, err := client.Execute(ctx, command, 60*time.Second)
		if err != nil {
			return err
		}
		if output := strings.TrimSpace(result.Output); output != "" {
			fmt.Println(output)
		}
		if result.ExitCode != 0 {
			return weaveerrors.ErrHealthCheckFailed(fmt.Sprintf("post-setup command %q exited with code %d", command, result.ExitCode))
		}
	}
	return nil
}
