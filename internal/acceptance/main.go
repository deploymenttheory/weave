// Command acceptance drives the code-signed weave binary through themed
// end-to-end suites against an isolated $WEAVE_HOME and settings directory.
//
// Usage:
//
//	go run ./example/weave/acceptance                            # default suites
//	go run ./example/weave/acceptance -suites cli,config
//	go run ./example/weave/acceptance -suites network,netbehavior-linux,netbehavior-macos
//	go run ./example/weave/acceptance -net                       # include network suites
//	go run ./example/weave/acceptance -guest my-vm               # include guest suite
//	go run ./example/weave/acceptance -keep                      # keep the isolated home
//
// Privilege and auto-escalation:
//
// Suites that require root (netbehavior-linux, netbehavior-macos) trigger
// automatic sudo escalation when not already running as root. The bridged
// scenario additionally requires a Developer ID signed binary; set
// WEAVE_ACC_BRIDGED=1 and WEAVE_SIGNING_IDENTITY to opt in:
//
//	WEAVE_ACC_BRIDGED=1 go run ./example/weave/acceptance -suites netbehavior-linux
//
// When WEAVE_ACC_BRIDGED=1 the runner builds and signs the weave binary as the
// invoking user (so the Developer ID private key in the login keychain is
// accessible) before escalating to sudo.
//
// Suites (children → parent): cli, config and logs touch no VM; network builds
// VMs from committed full-VM fixtures (fixtures/network/*.json — OS, disk,
// CPU/memory/display, run mode, NIC topology), validating the
// definition → loader → saver round-trip and the run command's network-flag
// validation (Linux fixtures run; macOS fixtures need WEAVE_ACC_MACOS_IPSW);
// lifecycle, vnc, serve and mcp drive real (fast, empty Linux) VMs and servers;
// ipsw is network-gated; guest requires a provisioned running VM.
//go:build darwin

package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() {
	// Keep the host awake for the duration of the run: provisioning suites
	// are long and idle-input stretches let macOS sleep the machine, which
	// freezes the VM, the automation and its timers mid-flight (observed
	// 2026-06-11: a <delay 10> boot command took 16 minutes across a host
	// sleep). caffeinate exits with us via -w.
	_ = exec.Command("caffeinate", "-dims", "-w", strconv.Itoa(os.Getpid())).Start()
	suitesFlag := flag.String("suites", "cli,config,logs,lifecycle,network,serve,mcp,vnc",
		"comma-separated suites to run")
	net := flag.Bool("net", false, "include network-dependent suites (ipsw)")
	provision := flag.Bool("provision", false, "include the macOS provisioning suites: a headless pass then a --show-screen pass, each a full IPSW pre-flight → create → setup → guest checks cycle on its own VM")
	guest := flag.String("guest", "", "name of a provisioned, running VM for the guest suite")
	guestUser := flag.String("guest-user", "weave", "guest SSH user")
	guestPassword := flag.String("guest-password", "weave", "guest SSH password")
	keep := flag.Bool("keep", false, "keep the isolated home and VMs after the run")
	home := flag.String("home", "", "isolated home base directory (default: a temp dir)")
	bin := flag.String("bin", "", "path to a pre-built, pre-signed weave binary (skips the build step)")
	ipsw := flag.String("ipsw", "", "cached restore image path for macOS tests (default: autodetect)")
	verbose := flag.Bool("v", false, "print case logs even on success")
	flag.Parse()

	repoRoot, err := findRepoRoot()
	if err != nil {
		fatal("%v", err)
	}

	// Auto-escalation: suites that exercise vmnet or softnet need root. When
	// invoked as a normal user we re-exec under sudo, passing all original flags.
	// Developer ID signing (WEAVE_ACC_BRIDGED=1) must happen before escalation
	// because the private key lives in the login keychain that root cannot reach.
	if os.Getuid() != 0 && suitesNeedRoot(*suitesFlag) {
		escalate(repoRoot)
		return
	}

	base := *home
	if base == "" {
		base, err = os.MkdirTemp("", "weave-acceptance-*")
		if err != nil {
			fatal("creating temp home: %v", err)
		}
	} else if err := os.MkdirAll(base, 0o755); err != nil {
		fatal("creating home %s: %v", base, err)
	}

	weaveHome := filepath.Join(base, "weave")
	configHome := filepath.Join(base, "config")
	binDir := filepath.Join(base, "bin")
	for _, dir := range []string{weaveHome, configHome, binDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fatal("mkdir %s: %v", dir, err)
		}
	}

	var binary string
	if *bin != "" {
		// Pre-built, pre-signed binary passed by the auto-escalation path so
		// the Developer ID signing happened as the non-root invoking user.
		binary = *bin
		fmt.Printf("Using pre-built binary %s (isolated home: %s)...\n", binary, base)
	} else {
		fmt.Printf("Building and signing weave (isolated home: %s)...\n", base)
		if os.Getenv("WEAVE_ACC_BRIDGED") == "1" {
			identity, ok := entitledSigningEnv()
			if !ok {
				fatal("WEAVE_ACC_BRIDGED=1 requires WEAVE_SIGNING_IDENTITY to be set.\n" +
					"Example:\n" +
					"  export WEAVE_SIGNING_IDENTITY=\"Developer ID Application: D Watkins (5GM6DW5337)\"\n" +
					"  WEAVE_ACC_BRIDGED=1 sudo go run ./example/weave/acceptance -suites netbehavior-linux")
			}
			binary, err = buildAndSignEntitled(repoRoot, binDir, identity)
		} else {
			binary, err = buildAndSign(repoRoot, binDir)
		}
		if err != nil {
			fatal("%v", err)
		}
	}

	cachedIPSW := *ipsw
	if cachedIPSW == "" {
		cachedIPSW = findCachedIPSW()
	}

	harness := &Harness{
		Bin:        binary,
		WeaveHome:  weaveHome,
		ConfigHome: configHome,
		IPSW:       cachedIPSW,
		RepoRoot:   repoRoot,
		Keep:       *keep,
	}

	// Build the registry of available suites.
	registry := map[string]func() *Suite{
		"cli":               cliSuite,
		"config":            configSuite,
		"logs":              logsSuite,
		"lifecycle":         lifecycleSuite,
		"network":           networkSuite,
		"netbehavior-linux": netBehaviorLinuxSuite,
		"netbehavior-macos": netBehaviorMacOSSuite,
		"vnc":               vncSuite,
		"serve":             serveSuite,
		"mcp":               mcpSuite,
	}

	var selected []*Suite
	for name := range strings.SplitSeq(*suitesFlag, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		factory, ok := registry[name]
		if !ok {
			fatal("unknown suite %q (available: %s)", name, strings.Join(suiteNames(registry), ", "))
		}
		selected = append(selected, factory())
	}
	if *net {
		selected = append(selected, ipswSuite())
	}
	if *provision {
		// Headless first, then the view-only screen viewer pass: both setup
		// code paths must provision a fresh VM end-to-end.
		selected = append(selected, provisionSuite(false), provisionSuite(true))
	}
	if *guest != "" {
		selected = append(selected, guestSuite(*guest, *guestUser, *guestPassword))
	}

	if !*keep {
		defer os.RemoveAll(base)
	}

	exitCode := runAll(harness, selected, *verbose)
	if *keep {
		fmt.Printf("\nIsolated home kept at %s\n", base)
	}
	os.Exit(exitCode)
}

// runAll executes the suites and prints a tree summary, returning the process
// exit code (non-zero if any case failed). Each case line is timestamped with
// its wall-clock start and tagged with its duration so a run is auditable
// after the fact.
func runAll(h *Harness, suites []*Suite, verbose bool) int {
	var totalPassed, totalFailed, totalSkipped int
	runStart := time.Now()

	for _, suite := range suites {
		fmt.Printf("\n=== %s === (%s)\n", suite.Name, time.Now().Format("15:04:05"))
		result := suite.run(h)

		if result.skipped {
			fmt.Printf("  (suite skipped: %s)\n", result.skipMsg)
			continue
		}

		for _, c := range result.cases {
			symbol := "PASS"
			switch c.outcome {
			case caseFailed:
				symbol = "FAIL"
			case caseSkipped:
				symbol = "SKIP"
			}
			fmt.Printf("  %s [%s] %s (%s)\n",
				c.started.Format("15:04:05"), symbol, c.name, formatDuration(c.duration))
			// Evidence lines are the concrete observed values the case
			// verified — always shown so a PASS is self-proving.
			for _, line := range c.evidence {
				fmt.Printf("        ✓ %s\n", line)
			}
			if verbose || c.outcome != casePassed {
				for _, line := range c.logs {
					fmt.Printf("        %s\n", line)
				}
			}
		}

		passed, failed, skipped := result.counts()
		totalPassed += passed
		totalFailed += failed
		totalSkipped += skipped
	}

	fmt.Printf("\n──────────────────────────────────────────\n")
	fmt.Printf("Total: %d passed, %d failed, %d skipped — %s elapsed (finished %s)\n",
		totalPassed, totalFailed, totalSkipped,
		formatDuration(time.Since(runStart)), time.Now().Format("15:04:05"))
	if totalFailed > 0 {
		return 1
	}
	return 0
}

// formatDuration renders a duration compactly: sub-minute as seconds with one
// decimal (1.2s), minute-plus as m s (3m05s).
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

func suiteNames(registry map[string]func() *Suite) []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	return names
}

// suitesNeedRoot reports whether any of the comma-separated suite names
// require root (vmnet / softnet privilege).
func suitesNeedRoot(suitesFlag string) bool {
	for name := range strings.SplitSeq(suitesFlag, ",") {
		switch strings.TrimSpace(name) {
		case "netbehavior-linux", "netbehavior-macos":
			return true
		}
	}
	return false
}

// escalate re-execs the acceptance suite under sudo, forwarding all original
// flags. When WEAVE_ACC_BRIDGED=1 it first builds and signs the weave binary
// as the invoking user (Developer ID private key is in the login keychain,
// inaccessible to the root process) and passes the result via -bin.
func escalate(repoRoot string) {
	realHome, _ := os.UserHomeDir()

	// Collect env overrides for the sudo env(1) prefix.
	envArgs := []string{"WEAVE_ACC_REAL_HOME=" + realHome}
	if os.Getenv("WEAVE_ACC_BRIDGED") == "1" {
		envArgs = append(envArgs, "WEAVE_ACC_BRIDGED=1")
	}

	// Forward all original flags unchanged; we may append -bin below.
	accArgs := append([]string{"go", "run", "./example/weave/acceptance"}, os.Args[1:]...)

	if os.Getenv("WEAVE_ACC_BRIDGED") == "1" {
		identity, ok := entitledSigningEnv()
		if !ok {
			fatal("WEAVE_ACC_BRIDGED=1 requires WEAVE_SIGNING_IDENTITY.\n" +
				"  export WEAVE_SIGNING_IDENTITY=\"Developer ID Application: D Watkins (5GM6DW5337)\"\n" +
				"  WEAVE_ACC_BRIDGED=1 go run ./example/weave/acceptance -suites netbehavior-linux")
		}
		// Build and sign as the invoking user before sudo changes our identity.
		binDir, err := os.MkdirTemp("/tmp", "weave-acc-bin-*")
		if err != nil {
			fatal("creating bin dir: %v", err)
		}
		// binDir cleanup: the sudo child inherits it; remove after it exits.
		defer os.RemoveAll(binDir)

		fmt.Println("Building and signing weave (Developer ID) before escalation...")
		binPath, err := buildAndSignEntitled(repoRoot, binDir, identity)
		if err != nil {
			fatal("%v", err)
		}
		accArgs = append(accArgs, "-bin", binPath)
	}

	fmt.Println("Escalating to root for network tests...")
	sudoArgs := append(append([]string{"env"}, envArgs...), accArgs...)
	cmd := exec.Command("sudo", sudoArgs...)
	cmd.Dir = repoRoot
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		os.Exit(1)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "acceptance: "+format+"\n", args...)
	os.Exit(2)
}

// errExit and errMsg build setup errors from binary results.
func errExit(action string, result runResult) error {
	return fmt.Errorf("%s failed (exit %d): %s", action, result.ExitCode, strings.TrimSpace(result.Stderr))
}

func errMsg(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}
