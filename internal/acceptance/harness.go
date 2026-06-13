// Acceptance-test harness for weave: drives the real, code-signed binary
// against an isolated $WEAVE_HOME and settings directory and provides the
// themed-suite test model. Suites (parent) hold cases (children); cases run
// in order and may build on the VMs created by earlier cases in the same
// suite.
//go:build darwin

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Harness owns the binary under test and the isolated environment.
type Harness struct {
	Bin        string // path to the signed weave binary
	WeaveHome  string // isolated $WEAVE_HOME
	ConfigHome string // isolated $XDG_CONFIG_HOME
	IPSW       string // cached restore image path (may be empty)
	RepoRoot   string
	Keep       bool // keep VMs created by suites (skip teardown delete)
}

// result of a single binary invocation.
type runResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// env returns the isolated environment for a binary invocation, with any
// extra KEY=VALUE entries appended (later entries win).
func (h *Harness) env(extra ...string) []string {
	env := append([]string{}, os.Environ()...)
	env = append(env,
		"WEAVE_HOME="+h.WeaveHome,
		"XDG_CONFIG_HOME="+h.ConfigHome,
	)
	return append(env, extra...)
}

// Run invokes the binary and waits for it to exit.
func (h *Harness) Run(args ...string) runResult {
	return h.RunEnv(nil, args...)
}

// RunEnv invokes the binary with extra environment entries (5-minute cap).
func (h *Harness) RunEnv(extra []string, args ...string) runResult {
	return h.RunTimeout(5*time.Minute, extra, args...)
}

// RunTimeout invokes the binary with a custom timeout — used for the long
// macOS install and unattended setup.
func (h *Harness) RunTimeout(timeout time.Duration, extra []string, args ...string) runResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	command := exec.CommandContext(ctx, h.Bin, args...)
	command.Env = h.env(extra...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	exitCode := 0
	if err := command.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
			stderr.WriteString(err.Error())
		}
	}
	return runResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}
}

// background process tracking the streamed combined output.
type background struct {
	cmd    *exec.Cmd
	output *syncBuffer
}

// Start launches the binary in the background, returning a handle whose
// Output() accumulates stdout+stderr.
func (h *Harness) Start(extra []string, args ...string) (*background, error) {
	command := exec.Command(h.Bin, args...)
	command.Env = h.env(extra...)
	output := &syncBuffer{}
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		return nil, err
	}
	return &background{cmd: command, output: output}, nil
}

func (b *background) Output() string { return b.output.String() }

func (b *background) Stop() {
	if b.cmd.Process != nil {
		_ = b.cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = b.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = b.cmd.Process.Kill()
		}
	}
}

// waitForOutput blocks until the background output contains substr or the
// timeout elapses.
func (b *background) waitForOutput(substr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(b.Output(), substr) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return strings.Contains(b.Output(), substr)
}

// ---------------------------------------------------------------------------
// Suite / Case model (parent → child)
// ---------------------------------------------------------------------------

// T is the per-case context, modelled on testing.T.
type T struct {
	name     string
	failed   bool
	skipped  bool
	skipMsg  string
	logs     []string
	evidence []string
}

type fatalPanic struct{}

func (t *T) Logf(format string, args ...any) {
	t.logs = append(t.logs, fmt.Sprintf(format, args...))
}

// Evidence records a line of proof for the case — the concrete observed
// values an assertion verified (e.g. the persisted NIC settings). Unlike
// Logf output, evidence is printed even for passing cases without -v, so a
// PASS line shows exactly what was checked.
func (t *T) Evidence(format string, args ...any) {
	t.evidence = append(t.evidence, fmt.Sprintf(format, args...))
}

func (t *T) Errorf(format string, args ...any) {
	t.failed = true
	t.logs = append(t.logs, "ERROR: "+fmt.Sprintf(format, args...))
}

func (t *T) Fatalf(format string, args ...any) {
	t.Errorf(format, args...)
	panic(fatalPanic{})
}

func (t *T) Skip(format string, args ...any) {
	t.skipped = true
	t.skipMsg = fmt.Sprintf(format, args...)
	panic(fatalPanic{})
}

// Case is one leaf test.
type Case struct {
	Name string
	Fn   func(t *T, h *Harness)
}

// Suite is a themed collection of cases sharing optional setup/teardown.
type Suite struct {
	Name     string
	Setup    func(h *Harness) error
	Teardown func(h *Harness)
	Cases    []Case
}

type caseOutcome int

const (
	casePassed caseOutcome = iota
	caseFailed
	caseSkipped
)

type caseResult struct {
	name     string
	outcome  caseOutcome
	logs     []string
	evidence []string
	started  time.Time
	duration time.Duration
}

type suiteResult struct {
	name    string
	cases   []caseResult
	skipped bool
	skipMsg string
}

func (s suiteResult) counts() (passed, failed, skipped int) {
	for _, c := range s.cases {
		switch c.outcome {
		case casePassed:
			passed++
		case caseFailed:
			failed++
		case caseSkipped:
			skipped++
		}
	}
	return
}

// run executes the suite and returns its result. A setup failure marks the
// whole suite skipped (the cases never ran).
func (s *Suite) run(h *Harness) suiteResult {
	result := suiteResult{name: s.Name}

	if s.Setup != nil {
		if err := s.Setup(h); err != nil {
			result.skipped = true
			result.skipMsg = "setup failed: " + err.Error()
			return result
		}
	}
	if s.Teardown != nil {
		defer s.Teardown(h)
	}

	for _, testCase := range s.Cases {
		result.cases = append(result.cases, runCase(testCase, h))
	}
	return result
}

func runCase(testCase Case, h *Harness) (result caseResult) {
	t := &T{name: testCase.Name}
	started := time.Now()
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(fatalPanic); !ok {
				t.failed = true
				t.logs = append(t.logs, fmt.Sprintf("PANIC: %v", r))
			}
		}
		outcome := casePassed
		logs := t.logs
		switch {
		case t.skipped:
			outcome, logs = caseSkipped, append(t.logs, t.skipMsg)
		case t.failed:
			outcome = caseFailed
		}
		result = caseResult{
			name:     testCase.Name,
			outcome:  outcome,
			logs:     logs,
			evidence: t.evidence,
			started:  started,
			duration: time.Since(started),
		}
	}()
	testCase.Fn(t, h)
	return
}

// ---------------------------------------------------------------------------
// Assertion helpers
// ---------------------------------------------------------------------------

func (t *T) assertExit(result runResult, want int) {
	if result.ExitCode != want {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", result.ExitCode, want, strings.TrimSpace(result.Stderr))
	}
}

func (t *T) assertContains(haystack, needle, context string) {
	if !strings.Contains(haystack, needle) {
		t.Errorf("%s: output does not contain %q\n--- output ---\n%s", context, needle, haystack)
	}
}

// ---------------------------------------------------------------------------
// Build & sign
// ---------------------------------------------------------------------------

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find repo root (go.mod) from the current directory")
		}
		dir = parent
	}
}

// buildAndSign compiles the weave binary into outDir and code-signs it with
// the Virtualization entitlement so VM-touching commands work.
func buildAndSign(repoRoot, outDir string) (string, error) {
	binary := filepath.Join(outDir, "weave")

	build := exec.Command("go", "build", "-o", binary, "./example/weave/")
	build.Dir = repoRoot
	build.Env = os.Environ()
	if output, err := build.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build failed: %v\n%s", err, output)
	}

	entitlements := filepath.Join(repoRoot, "example", "weave", "entitlements.plist")
	sign := exec.Command("codesign", "--entitlements", entitlements, "--force", "-s", "-", binary)
	if output, err := sign.CombinedOutput(); err != nil {
		return "", fmt.Errorf("codesign failed: %v\n%s", err, output)
	}
	return binary, nil
}

// buildAndSignEntitled is like buildAndSign but uses a named Developer ID
// signing identity and includes the com.apple.developer.networking.vmnet
// entitlement (entitlements-bridged.plist). Apple must have authorized that
// entitlement for the Team ID associated with signingIdentity — the
// authorization is tied to the cert chain, not an embedded profile.
//
// signingIdentity: full cert name, e.g.
//
//	"Developer ID Application: D Watkins (5GM6DW5337)"
func buildAndSignEntitled(repoRoot, outDir, signingIdentity string) (string, error) {
	binary := filepath.Join(outDir, "weave")

	build := exec.Command("go", "build", "-o", binary, "./example/weave/")
	build.Dir = repoRoot
	build.Env = os.Environ()
	if output, err := build.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build failed: %v\n%s", err, output)
	}

	entitlements := filepath.Join(repoRoot, "example", "weave", "entitlements-bridged.plist")
	sign := exec.Command("codesign",
		"--sign", signingIdentity,
		"--entitlements", entitlements,
		"--timestamp",
		"--force",
		binary,
	)
	if output, err := sign.CombinedOutput(); err != nil {
		return "", fmt.Errorf("codesign (entitled) failed: %v\n%s", err, output)
	}
	return binary, nil
}

// entitledSigningEnv reads WEAVE_SIGNING_IDENTITY for the entitled signing
// path. Returns ("", false) when unset (normal ad-hoc dev builds).
func entitledSigningEnv() (identity string, ok bool) {
	identity = os.Getenv("WEAVE_SIGNING_IDENTITY")
	return identity, identity != ""
}

// findCachedIPSW returns the newest cached restore image, if any.
func findCachedIPSW() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	matches, err := filepath.Glob(filepath.Join(home, ".weave", "cache", "IPSWs", "*.ipsw"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// syncBuffer is a goroutine-safe bytes.Buffer for streamed subprocess output.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
