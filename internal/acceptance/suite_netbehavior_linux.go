// Theme: behavioural network proof (Linux guest), plus the shared
// infrastructure both behavioural suites use (guest command channels, the
// probe drivers, reachability assertions, and the OCI-cache bridge). The macOS
// counterpart lives in suite_netbehavior_macos.go.
//
// Unlike the `network` suite (which proves the config round-trip), the
// netbehavior-linux suite boots a real Linux guest under each network profile,
// runs an in-guest reachability battery (netprobe.sh) over SSH (or the vsock
// guest agent when the image provides one), and asserts the observed
// reachability matches the profile's contract at the packet level:
//
//   - nat           internet ✓, dns ✓, host/gateway ✓
//   - internet-only internet ✓, dns ✓                     (softnet, root)
//   - isolated      internet ✗, dns ✗                     (softnet, root)
//   - vm-lab        host ✓, internet ✗                    (vmnet host, root)
//   - bridged       internet ✓                            (bridged, entitlement)
//
// The softnet cases assert egress posture only: under softnet the guest's
// default gateway is softnet's own userspace NAT (not the macOS host), so a
// "host/gateway" ping is not a meaningful host-reachability signal there. The
// vm-lab "host" probe pings the subnet's .1, the host's address in vmnet host
// mode (which provides no default gateway).
//
// Each scenario boots a single VM and asserts the reachability that VM sees.
// VM-to-VM interconnect is intentionally not asserted: vmnet networks are
// process-scoped (separate `weave run` invocations cannot share one), and
// guest↔guest / guest↔host isolation under nat is expected behaviour.
//
// Privilege: nat needs none; softnet (internet-only, isolated) and vmnet
// (vm-lab) need root; bridged needs the com.apple.vm.networking entitlement
// (root does not bypass VZBridgedNetworkDeviceAttachment), opted in via
// WEAVE_ACC_BRIDGED=1.
//
// A bootable Linux guest image must be cached first (one-time):
//
//	weave pull ghcr.io/cirruslabs/ubuntu:latest
//
// The whole suite skips cleanly when no image is available.
//go:build darwin

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Probe targets shared across scenarios. The internet probes use a captive-
// portal-free 204 endpoint and a literal-IP fallback so a DNS block is
// distinguishable from an egress block.
const (
	probeInternetURL = "http://connectivitycheck.gstatic.com/generate_204"
	probeInternetIP  = "http://1.1.1.1/"
	probeDNSName     = "example.com"
)

// netBehaviorConfig resolves the image and guest credentials from the
// environment, with defaults for the public cirruslabs Ubuntu image.
type netBehaviorConfig struct {
	image    string
	user     string
	password string
}

func loadNetBehaviorConfig() netBehaviorConfig {
	cfg := netBehaviorConfig{
		image:    os.Getenv("WEAVE_ACC_LINUX_IMAGE"),
		user:     os.Getenv("WEAVE_ACC_LINUX_USER"),
		password: os.Getenv("WEAVE_ACC_LINUX_PASSWORD"),
	}
	if cfg.image == "" {
		cfg.image = "ghcr.io/cirruslabs/ubuntu:latest"
	}
	if cfg.user == "" {
		cfg.user = "admin"
	}
	if cfg.password == "" {
		cfg.password = "admin"
	}
	return cfg
}

// ── Guest command channels ───────────────────────────────────────────────────

// execRunner runs guest commands over the vsock guest agent (`weave exec`),
// which works regardless of network isolation.
type execRunner struct {
	h  *Harness
	vm string
}

func (r execRunner) RunGuest(_ context.Context, shellCommand string) (string, int, error) {
	result := r.h.RunTimeout(2*time.Minute, nil, "exec", r.vm, "sh", "-c", shellCommand)
	return result.Stdout + result.Stderr, result.ExitCode, nil
}

// agentAvailable reports whether the guest answers over the vsock guest agent.
// Images without the Tart Guest Agent (e.g. stock cirruslabs Linux) fail with
// a control-socket error, in which case the suite uses SSH instead.
func (r execRunner) agentAvailable(h *Harness) bool {
	result := h.RunTimeout(15*time.Second, nil, "exec", r.vm, "true")
	return result.ExitCode == 0 &&
		!strings.Contains(result.Stdout+result.Stderr, "Guest Agent")
}

// sshRunner runs guest commands over `weave ssh` (which resolves the guest's
// address), usable only when the guest is host-reachable. `weave ssh` joins its
// trailing args with spaces and runs the result as a remote shell line, so the
// whole command must be passed as a single argument. resolver selects the IP
// resolution strategy: "dhcp" for weave-managed leases (nat, vm-lab) or "arp"
// for a bridged guest, whose LAN DHCP lease is invisible to weave but whose MAC
// appears in the host ARP cache.
type sshRunner struct {
	h        *Harness
	vm       string
	user     string
	password string
	resolver string
}

func (r sshRunner) RunGuest(_ context.Context, shellCommand string) (string, int, error) {
	args := []string{"ssh", "--user", r.user, "--password", r.password}
	if r.resolver != "" {
		args = append(args, "--resolver", r.resolver)
	}
	args = append(args, r.vm, shellCommand)
	result := r.h.RunTimeout(2*time.Minute, nil, args...)
	return result.Stdout + result.Stderr, result.ExitCode, nil
}

// ── Suite ────────────────────────────────────────────────────────────────────

func netBehaviorLinuxSuite() *Suite {
	cfg := loadNetBehaviorConfig()
	root := os.Geteuid() == 0

	// Scenario VMs cloned from the base image (one per profile).
	const (
		vmNAT      = "acc-netb-nat"
		vmInternet = "acc-netb-internet-only"
		vmIsolated = "acc-netb-isolated"
		vmLab      = "acc-netb-vmlab"
		vmBridged  = "acc-netb-bridged"
	)
	allVMs := []string{vmNAT, vmInternet, vmIsolated, vmLab, vmBridged}

	suite := &Suite{
		Name: "netbehavior-linux",
		Setup: func(h *Harness) error {
			// The harness isolates WEAVE_HOME, but the multi-GB guest image is
			// cached under the real ~/.weave. Share the OCI cache read-side
			// into the isolated home (cloned VMs still land in the isolated
			// WEAVE_HOME/vms) so we reuse the pulled image instead of
			// re-downloading it.
			if err := shareOCICache(h); err != nil {
				return err
			}
			if !imageAvailable(cfg.image) {
				return fmt.Errorf(
					"no bootable Linux guest image cached — run: weave pull %s (or set WEAVE_ACC_LINUX_IMAGE)",
					cfg.image)
			}
			return nil
		},
		Teardown: func(h *Harness) {
			h.Run(append([]string{"delete"}, allVMs...)...)
		},
	}

	// nat — no privilege required. The guest reaches the internet, resolves
	// DNS, and reaches its host-side gateway.
	suite.Cases = append(suite.Cases,
		Case{"nat: guest reaches internet, DNS and host gateway", func(t *T, h *Harness) {
			matrix := bootProbeStop(t, h, cfg, vmNAT, "nat", probeTargets{
				HostIP:      "auto",
				DNSName:     probeDNSName,
				InternetURL: probeInternetURL,
				InternetIP:  probeInternetIP,
			})
			assertReachable(t, matrix, "internet", true)
			assertReachable(t, matrix, "internet_ip", true)
			assertReachable(t, matrix, "dns", true)
			assertReachable(t, matrix, "host", true)
		}},
	)

	// Softnet profiles — need sudo (root). The assertions target each
	// profile's defining egress posture; the "host" probe is omitted here
	// because under softnet the guest's default gateway is softnet's own
	// userspace NAT, not the macOS host, so it is not a meaningful host-
	// reachability signal.
	suite.Cases = append(suite.Cases,
		Case{"internet-only: guest reaches the internet (softnet default)", func(t *T, h *Harness) {
			requireRoot(t, root, "softnet needs sudo")
			matrix := bootProbeStop(t, h, cfg, vmInternet, "internet-only", probeTargets{
				DNSName:     probeDNSName,
				InternetURL: probeInternetURL,
				InternetIP:  probeInternetIP,
			})
			assertReachable(t, matrix, "internet", true)
			assertReachable(t, matrix, "internet_ip", true)
		}},
		Case{"isolated: air-gapped — no egress at all", func(t *T, h *Harness) {
			requireRoot(t, root, "softnet needs sudo")
			matrix := bootProbeStop(t, h, cfg, vmIsolated, "isolated", probeTargets{
				DNSName:     probeDNSName,
				InternetURL: probeInternetURL,
				InternetIP:  probeInternetIP,
			})
			assertReachable(t, matrix, "internet", false)
			assertReachable(t, matrix, "internet_ip", false)
			assertReachable(t, matrix, "dns", false)
		}},
	)

	// vmnet host-mode reaches the host segment (its router) but has no
	// internet; root is sufficient for the vmnet path. (VM-to-VM interconnect
	// is not asserted: vmnet networks are process-scoped, so two separate
	// `weave run` invocations cannot share one — and guest↔guest/guest↔host
	// isolation under nat is expected, not a defect.)
	suite.Cases = append(suite.Cases,
		Case{"vm-lab: reaches the host segment, but not the internet", func(t *T, h *Harness) {
			requireRoot(t, root, "vmnet host-mode needs the entitlement or root")
			matrix := bootProbeStop(t, h, cfg, vmLab, "vm-lab", probeTargets{
				HostIP:      "auto",
				InternetURL: probeInternetURL,
				InternetIP:  probeInternetIP,
			})
			assertReachable(t, matrix, "host", true)
			assertReachable(t, matrix, "internet", false)
			assertReachable(t, matrix, "internet_ip", false)
		}},
		// bridged uses VZBridgedNetworkDeviceAttachment, which the Virtualization
		// framework gates on the com.apple.vm.networking entitlement — and root
		// does NOT bypass it (unlike the vmnet/softnet paths). So it only runs
		// with a properly entitled binary (Path B), opted in via WEAVE_ACC_BRIDGED=1.
		Case{"bridged: guest is a LAN peer with internet", func(t *T, h *Harness) {
			requireBridged(t)
			matrix := bootProbeStop(t, h, cfg, vmBridged, "bridged", probeTargets{
				InternetURL: probeInternetURL,
			})
			assertReachable(t, matrix, "internet", true)
		}},
	)

	return suite
}

// ── Scenario drivers ─────────────────────────────────────────────────────────

// runNetArgs returns the `weave run` network flag(s) for a profile. Every
// profile uses the named --net-profile; all of them — including the softnet
// profiles (internet-only, isolated) — leave the guest reachable from the host
// via `weave ssh` (softnet's userspace NAT still resolves and forwards to the
// guest; its --block filters the guest's egress, not host→guest ingress).
func runNetArgs(profile string) []string {
	return []string{"--net-profile", profile}
}

// bootProbeStop clones the base image into vm, boots it under the profile,
// waits for a usable guest channel, runs the probe battery, records the matrix
// as evidence, and stops the VM. It fails the case on any infrastructure error
// (clone/boot/channel) so a genuine boot failure is never mistaken for a
// network result.
func bootProbeStop(t *T, h *Harness, cfg netBehaviorConfig, vm, profile string,
	targets probeTargets,
) reachability {
	ensureClone(t, h, cfg.image, vm)

	runArgs := append([]string{"run", vm, "--no-graphics"}, runNetArgs(profile)...)
	bg, err := h.Start(nil, runArgs...)
	if err != nil {
		t.Fatalf("starting %q under %s: %v", vm, profile, err)
	}
	defer bg.Stop()

	runner := openGuestChannel(t, h, cfg, vm, profile, bg)
	matrix, err := runProbes(context.Background(), runner, targets)
	if err != nil {
		t.Fatalf("probing %q: %v\n--- run log ---\n%s", vm, err, tail(bg.Output(), 30))
	}

	recordMatrix(t, "guest", matrix)
	return matrix
}

// recordMatrix emits the reachability matrix as evidence, one summary line plus
// one detail line per probe.
func recordMatrix(t *T, label string, matrix reachability) {
	t.Evidence("%s uname=%s; reachability: %s", label, matrix.Uname, matrix.summary())
	for _, p := range matrix.Probes {
		state := "fail"
		if p.OK {
			state = "ok"
		}
		t.Evidence("  %s=%s (%s)", p.Name, state, p.Detail)
	}
}

// openGuestChannel boot-waits for a usable guest command channel: the vsock
// guest agent if the image provides one (works under any isolation), otherwise
// `weave ssh` — which reaches the guest under every profile here, including the
// softnet ones (softnet's userspace NAT still resolves and forwards host→guest;
// bridged resolves via the host ARP cache). It fails the case — surfacing
// weave's error log — when the VM never reaches running (a boot failure) or no
// channel comes up in time.
func openGuestChannel(t *T, h *Harness, cfg netBehaviorConfig, vm, profile string, bg *background) guestRunner {
	// A guest that never reaches running is a boot failure, not a network
	// result — surface the logs so the cause (e.g. softnet helper or
	// bridged-interface setup) is visible rather than a bare "not reachable".
	if !waitRunning(h, vm, 3*time.Minute) {
		t.Fatalf("guest %q never reached running under %s\n--- run log ---\n%s\n%s",
			vm, profile, tail(bg.Output(), 30), channelDiagnostics(h, nil))
	}

	// Prefer the vsock guest agent (works under any isolation); fall through
	// to SSH for images without it.
	if exec := (execRunner{h: h, vm: vm}); exec.agentAvailable(h) {
		t.Logf("guest %q reachable via vsock agent", vm)
		return exec
	}

	ssh := sshRunner{h: h, vm: vm, user: cfg.user, password: cfg.password}
	if profile == "bridged" {
		// The guest's LAN DHCP lease is invisible to weave; resolve via ARP.
		ssh.resolver = "arp"
	}

	deadline := time.Now().Add(3 * time.Minute)
	var lastErr error
	for time.Now().Before(deadline) {
		_, code, err := ssh.RunGuest(context.Background(), "true")
		if err == nil && code == 0 {
			t.Logf("guest %q reachable via SSH", vm)
			return ssh
		}
		lastErr = err
		if !isRunning(h, vm) {
			t.Fatalf("guest %q exited while waiting for SSH under %s\n%s",
				vm, profile, channelDiagnostics(h, lastErr))
		}
		time.Sleep(3 * time.Second)
	}

	t.Fatalf("guest %q booted but never became SSH-reachable under %s\n%s",
		vm, profile, channelDiagnostics(h, lastErr))
	return nil
}

// channelDiagnostics gathers what is useful for diagnosing why a guest channel
// never came up: the last SSH error and weave's own error log (which captures
// softnet/vmnet failures that `weave run` does not print to stdout).
func channelDiagnostics(h *Harness, lastErr error) string {
	var b strings.Builder
	if lastErr != nil {
		fmt.Fprintf(&b, "last SSH error: %v\n", lastErr)
	}
	errLog := h.Run("logs", "error", "--lines", "40")
	fmt.Fprintf(&b, "--- weave error log ---\n%s", strings.TrimSpace(errLog.Stdout+errLog.Stderr))
	return b.String()
}

// waitRunning blocks until the VM reports running or the timeout elapses,
// returning whether it is running.
func waitRunning(h *Harness, vm string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if isRunning(h, vm) {
			return true
		}
		time.Sleep(2 * time.Second)
	}
	return isRunning(h, vm)
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func requireRoot(t *T, root bool, why string) {
	if !root {
		t.Skip("requires root (%s) — re-run the suite under sudo", why)
	}
}

// requireBridged gates the bridged scenario. VZBridgedNetworkDeviceAttachment
// requires the com.apple.vm.networking entitlement. Root does not bypass it —
// AMFI kills any binary that claims it without an Apple-authorized provisioning
// profile. Opt in with WEAVE_ACC_BRIDGED=1 and the two signing env vars:
//
//	WEAVE_SIGNING_IDENTITY  — Developer ID cert (Apple must have granted the entitlement)
//	WEAVE_PROVISIONING_PROFILE — path to the .provisionprofile with com.apple.vm.networking
func requireBridged(t *T) {
	if os.Getenv("WEAVE_ACC_BRIDGED") != "1" {
		t.Skip("bridged skipped: set WEAVE_ACC_BRIDGED=1 with WEAVE_SIGNING_IDENTITY and " +
			"WEAVE_PROVISIONING_PROFILE to run (Apple must authorize com.apple.vm.networking " +
			"for your Team ID first — developer.apple.com → Contact → Virtualization entitlement request)")
	}
}

func assertReachable(t *T, matrix reachability, name string, want bool) {
	if !matrix.ran(name) {
		t.Errorf("probe %q did not run", name)
		return
	}
	if got := matrix.ok(name); got != want {
		t.Errorf("probe %q reachable = %v, want %v", name, got, want)
	}
}

// realOCICacheDir is the user's real OCI image cache (weave pulls land here
// when WEAVE_HOME is unset). The harness isolates WEAVE_HOME, so the suite
// bridges to this directory rather than re-pulling.
//
// When running under sudo the effective home is /var/root, not the invoking
// user's home. WEAVE_ACC_REAL_HOME overrides UserHomeDir so the cache bridge
// still points at the correct location (set by script/main.go before escalation).
func realOCICacheDir() string {
	home := os.Getenv("WEAVE_ACC_REAL_HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return ""
		}
	}
	return filepath.Join(home, ".weave", "cache", "OCIs")
}

// ociCachePath maps an image ref (ghcr.io/cirruslabs/ubuntu:latest) to its
// cached directory (…/OCIs/ghcr.io/cirruslabs/ubuntu/latest).
func ociCachePath(image string) string {
	repo, tag := image, "latest"
	if idx := strings.LastIndex(image, ":"); idx >= 0 {
		repo, tag = image[:idx], image[idx+1:]
	}
	return filepath.Join(realOCICacheDir(), filepath.FromSlash(repo), tag)
}

// imageAvailable reports whether the base image is present in the real OCI
// cache (so a clone is offline).
func imageAvailable(image string) bool {
	info, err := os.Stat(ociCachePath(image))
	return err == nil && info.IsDir()
}

// shareOCICache symlinks the real OCI cache into the isolated WEAVE_HOME so
// cloned VMs reuse the already-pulled image. It is a no-op when the real cache
// is absent (the availability check then skips the suite) or already linked.
func shareOCICache(h *Harness) error {
	real := realOCICacheDir()
	if real == "" {
		return nil
	}
	if info, err := os.Stat(real); err != nil || !info.IsDir() {
		return nil // nothing to share; imageAvailable will skip the suite
	}
	isolatedCache := filepath.Join(h.WeaveHome, "cache")
	if err := os.MkdirAll(isolatedCache, 0o755); err != nil {
		return fmt.Errorf("creating isolated cache dir: %w", err)
	}
	link := filepath.Join(isolatedCache, "OCIs")
	if _, err := os.Lstat(link); err == nil {
		return nil // already present
	}
	if err := os.Symlink(real, link); err != nil {
		return fmt.Errorf("linking OCI cache %s → %s: %w", real, link, err)
	}
	return nil
}

// ensureClone clones the base image into vm (idempotent — reuses an existing
// clone), failing the case if the clone cannot be produced.
func ensureClone(t *T, h *Harness, image, vm string) {
	if isRunning(h, vm) {
		return
	}
	if h.Run("get", vm, "--format", "json").ExitCode == 0 {
		return // already cloned
	}
	if result := h.RunTimeout(10*time.Minute, nil, "clone", image, vm); result.ExitCode != 0 {
		t.Fatalf("cloning %q → %q: %s", image, vm, strings.TrimSpace(result.Stderr))
	}
}
