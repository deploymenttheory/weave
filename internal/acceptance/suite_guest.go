// Theme: features that require a provisioned, running guest OS with SSH
// enabled — ssh, exec and clipboard. Two entry points share the same checks:
//
//   - guestSuite (-guest <vm>): runs the checks against an externally
//     provisioned, already-running VM.
//   - provisionSuite (-provision): self-provisions a macOS VM end to end
//     (IPSW pre-flight → create → unattended setup) and then runs the checks.
//go:build darwin

package main

import (
	"strings"
	"time"
)

// guestChecks are the leaf cases exercised once a guest is running with SSH.
func guestChecks(vmName, user, password string) []Case {
	return []Case{
		{"ssh runs a one-shot command", func(t *T, h *Harness) {
			result := h.RunEnv(nil, "ssh", "--user", user, "--password", password, vmName, "echo", "weave-ok")
			t.assertExit(result, 0)
			t.assertContains(result.Stdout, "weave-ok", "ssh echo")
		}},
		{"ssh propagates a non-zero remote exit code", func(t *T, h *Harness) {
			result := h.RunEnv(nil, "ssh", "--user", user, "--password", password, vmName, "exit", "7")
			if result.ExitCode != 7 {
				t.Errorf("remote exit 7 not propagated: got %d", result.ExitCode)
			}
		}},
		{"exec runs through the guest agent", func(t *T, h *Harness) {
			result := h.Run("exec", vmName, "echo", "agent-ok")
			if result.ExitCode != 0 {
				t.Skip("guest agent not available: %s", strings.TrimSpace(result.Stderr))
			}
			t.assertContains(result.Stdout, "agent-ok", "exec echo")
		}},
		{"clipboard sync starts over SSH", func(t *T, h *Harness) {
			bg, err := h.Start(nil, "run", vmName, "--clipboard",
				"--clipboard-user", user, "--clipboard-password", password, "--no-graphics")
			if err != nil {
				t.Fatalf("starting run --clipboard: %v", err)
			}
			defer bg.Stop()
			if !bg.waitForOutput("Clipboard sync started", 30*time.Second) {
				t.Errorf("clipboard sync did not start:\n%s", bg.Output())
			}
		}},
	}
}

// guestSuite runs the checks against an externally provisioned, running VM.
func guestSuite(vmName, user, password string) *Suite {
	return &Suite{
		Name: "guest",
		Setup: func(h *Harness) error {
			result := h.Run("get", vmName, "--format", "json")
			if result.ExitCode != 0 {
				return errExit("get "+vmName, result)
			}
			if !strings.Contains(result.Stdout, `"Running": true`) {
				return errMsg("guest VM %q is not running", vmName)
			}
			return nil
		},
		Cases: guestChecks(vmName, user, password),
	}
}
