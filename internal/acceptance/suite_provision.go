// Theme: end-to-end macOS provisioning (gated by -provision). Runs the IPSW
// pre-flight, creates a macOS VM (reusing an existing one), drives the
// unattended Setup Assistant automation, then exercises the guest checks
// (ssh/exec/clipboard) against the freshly provisioned VM. The unattended
// presets provision a weave/weave account with Remote Login enabled.
//
// -provision runs the suite twice, sequentially: once headless and once with
// the view-only screen viewer (--show-screen), each against its own fresh VM
// — Setup Assistant only runs once per VM, so both code paths need a full
// provisioning cycle of their own.
//go:build darwin

package main

import (
	"fmt"
	"strings"
	"time"
)

const (
	provisionUser     = "weave"
	provisionPassword = "weave"
)

func provisionSuite(showScreen bool) *Suite {
	suiteName, provisionVM := "provision", "acc-macos"
	if showScreen {
		suiteName, provisionVM = "provision-viewer", "acc-macos-viewer"
	}

	// Shared across cases: the resolved IPSW source and chosen preset.
	state := &struct {
		source string
		preset string
	}{}

	cases := []Case{
		{"pre-flight: validate the IPSW cache", func(t *T, h *Harness) {
			source, err := ipswPreflight(t.Logf)
			if err != nil {
				t.Fatalf("IPSW pre-flight failed: %v", err)
			}
			state.source = source
			state.preset = presetForIPSW(source)
			t.Logf("using IPSW source %q with the %q preset", source, state.preset)
		}},
		{"create (or reuse) the macOS VM", func(t *T, h *Harness) {
			if h.Run("get", provisionVM, "--format", "json").ExitCode == 0 {
				t.Logf("reusing existing VM %q", provisionVM)
				return
			}
			if state.source == "" {
				t.Skip("pre-flight did not resolve an IPSW source")
			}
			t.Logf("installing macOS into %q (this takes a few minutes)...", provisionVM)
			result := h.RunTimeout(45*time.Minute, nil, "create", "--from-ipsw", state.source, provisionVM)
			t.assertExit(result, 0)
		}},
		{fmt.Sprintf("unattended setup provisions the guest (show-screen: %t)", showScreen), func(t *T, h *Harness) {
			if isRunning(h, provisionVM) {
				t.Logf("VM already running; assuming it is provisioned")
				return
			}
			t.Logf("running unattended %q setup (boots the VM and drives Setup Assistant)...", state.preset)
			args := []string{"setup", provisionVM, "--unattended", state.preset}
			if showScreen {
				args = append(args, "--show-screen")
				t.Logf("--show-screen: a view-only browser viewer will open so you can watch the steps")
			}
			result := h.RunTimeout(40*time.Minute, nil, args...)
			if result.ExitCode != 0 {
				t.Fatalf("setup failed (exit %d):\nstdout:\n%s\nstderr:\n%s",
					result.ExitCode, tail(result.Stdout, 40), strings.TrimSpace(result.Stderr))
			}
			t.assertContains(result.Stdout, "Unattended setup finished", "setup completion")
		}},
		{"guest is running with an IP", func(t *T, h *Harness) {
			deadline := time.Now().Add(2 * time.Minute)
			for time.Now().Before(deadline) {
				if isRunning(h, provisionVM) {
					return
				}
				time.Sleep(2 * time.Second)
			}
			t.Fatalf("VM %q is not running after setup", provisionVM)
		}},
	}
	cases = append(cases, guestChecks(provisionVM, provisionUser, provisionPassword)...)

	return &Suite{
		Name:  suiteName,
		Cases: cases,
		Teardown: func(h *Harness) {
			if h.Keep {
				return
			}
			h.Run("stop", provisionVM)
			h.Run("delete", provisionVM)
		},
	}
}

func isRunning(h *Harness, vmName string) bool {
	result := h.Run("get", vmName, "--format", "json")
	return result.ExitCode == 0 && strings.Contains(result.Stdout, `"Running": true`)
}

// tail returns the last n lines of s.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
