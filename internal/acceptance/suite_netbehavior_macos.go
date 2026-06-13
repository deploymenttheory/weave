// Theme: behavioural network proof for a *macOS* guest. This is the macOS
// equivalent of the netbehavior suite — the same in-guest reachability battery
// (netprobe.sh is Darwin-aware), but run inside a real macOS VM where the
// vmnet/softnet/bridged distinctions are most consequential.
//
// A macOS guest is expensive to produce (IPSW install + Setup Assistant), so
// this suite does NOT provision one — it reuses a macOS VM already provisioned
// by the `-provision` suite (default name "acc-macos", override with
// WEAVE_ACC_MACOS_VM). For each profile it stops the VM, re-runs it under that
// profile (the installed OS keeps its disk; only the host-side network
// changes), waits for a guest channel, probes, and asserts the matrix.
//
// Gating: the suite skips entirely unless a provisioned macOS VM exists.
// Non-nat profiles additionally require root (softnet sudo; vmnet/bridged
// entitlement-or-root), exactly as for Linux.
//go:build darwin

package main

import (
	"context"
	"fmt"
	"os"
	"time"
)

const (
	macosNetVMDefault = "acc-macos"
	macosNetUser      = "weave"
	macosNetPassword  = "weave"
)

func netBehaviorMacOSSuite() *Suite {
	root := os.Geteuid() == 0
	vm := os.Getenv("WEAVE_ACC_MACOS_VM")
	if vm == "" {
		vm = macosNetVMDefault
	}
	cfg := netBehaviorConfig{user: macosNetUser, password: macosNetPassword}

	suite := &Suite{
		Name: "netbehavior-macos",
		Setup: func(h *Harness) error {
			if h.Run("get", vm, "--format", "json").ExitCode != 0 {
				return fmt.Errorf(
					"no provisioned macOS guest %q — provision one first: "+
						"sudo go run ./example/weave/acceptance -provision (or set WEAVE_ACC_MACOS_VM)", vm)
			}
			return nil
		},
		Teardown: func(h *Harness) {
			// Leave the (expensive) provisioned VM in place; just stop it.
			h.Run("stop", vm)
		},
	}

	suite.Cases = append(suite.Cases,
		Case{"macOS nat: guest reaches internet, DNS and host gateway", func(t *T, h *Harness) {
			matrix := macosBootProbeStop(t, h, cfg, vm, "nat", probeTargets{
				HostIP:      "auto",
				DNSName:     probeDNSName,
				InternetURL: probeInternetURL,
				InternetIP:  probeInternetIP,
			})
			assertReachable(t, matrix, "internet", true)
			assertReachable(t, matrix, "dns", true)
			assertReachable(t, matrix, "host", true)
		}},
		Case{"macOS isolated: air-gapped — no egress at all", func(t *T, h *Harness) {
			requireRoot(t, root, "softnet needs sudo")
			matrix := macosBootProbeStop(t, h, cfg, vm, "isolated", probeTargets{
				DNSName:     probeDNSName,
				InternetURL: probeInternetURL,
				InternetIP:  probeInternetIP,
			})
			assertReachable(t, matrix, "internet", false)
			assertReachable(t, matrix, "internet_ip", false)
		}},
		Case{"macOS vm-lab: host-mode segment reaches the host, not the internet", func(t *T, h *Harness) {
			requireRoot(t, root, "vmnet host-mode needs the entitlement or root")
			matrix := macosBootProbeStop(t, h, cfg, vm, "vm-lab", probeTargets{
				HostIP:      "auto",
				InternetURL: probeInternetURL,
			})
			assertReachable(t, matrix, "host", true)
			assertReachable(t, matrix, "internet", false)
		}},
		Case{"macOS bridged: guest is a LAN peer with internet", func(t *T, h *Harness) {
			requireBridged(t)
			matrix := macosBootProbeStop(t, h, cfg, vm, "bridged", probeTargets{
				InternetURL: probeInternetURL,
			})
			assertReachable(t, matrix, "internet", true)
		}},
	)

	return suite
}

// macosBootProbeStop stops the provisioned macOS VM, re-runs it under the
// given profile, probes it, records the matrix as evidence, and stops it. The
// VM's installed OS persists across runs — only the host-side network changes.
func macosBootProbeStop(t *T, h *Harness, cfg netBehaviorConfig, vm, profile string,
	targets probeTargets,
) reachability {
	// Ensure a clean start under the new profile.
	h.Run("stop", vm)
	waitNotRunning(h, vm, 90*time.Second)

	runArgs := append([]string{"run", vm, "--no-graphics"}, runNetArgs(profile)...)
	bg, err := h.Start(nil, runArgs...)
	if err != nil {
		t.Fatalf("starting macOS %q under %s: %v", vm, profile, err)
	}
	defer bg.Stop()

	runner := openGuestChannel(t, h, cfg, vm, profile, bg)
	matrix, err := runProbes(context.Background(), runner, targets)
	if err != nil {
		t.Fatalf("probing macOS %q: %v\n--- run log ---\n%s", vm, err, tail(bg.Output(), 20))
	}

	recordMatrix(t, "macOS guest", matrix)
	return matrix
}

// waitNotRunning blocks until the VM reports stopped or the timeout elapses.
func waitNotRunning(h *Harness, vm string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !isRunning(h, vm) {
			return
		}
		time.Sleep(2 * time.Second)
	}
}
