//go:build darwin

package main

import (
	"context"
	"strings"
	"testing"
)

// fakeRunner returns canned guest output and records the delivered command, so
// the test verifies both parsing and the battery built by guestProbeCommand.
type fakeRunner struct {
	output  string
	exey    int
	err     error
	lastCmd string
}

func (f *fakeRunner) RunGuest(_ context.Context, shellCommand string) (string, int, error) {
	f.lastCmd = shellCommand
	return f.output, f.exey, f.err
}

func TestRunProbesParsesMatrix(t *testing.T) {
	runner := &fakeRunner{output: `Welcome to Ubuntu 24.04 LTS
===NETPROBE-BEGIN===
uname=Linux
PROBE host ok ping 192.168.64.1
PROBE peer ok ping 192.168.64.7
PROBE dns fail resolve example.com
PROBE internet fail fetch http://connectivitycheck.gstatic.com/generate_204
===NETPROBE-END===
Connection closed.`}

	got, err := runProbes(context.Background(), runner, probeTargets{
		HostIP:      "192.168.64.1",
		PeerIP:      "192.168.64.7",
		DNSName:     "example.com",
		InternetURL: "http://connectivitycheck.gstatic.com/generate_204",
	})
	if err != nil {
		t.Fatalf("runProbes: %v", err)
	}

	if got.Uname != "Linux" {
		t.Errorf("Uname = %q, want Linux", got.Uname)
	}
	want := map[string]bool{"host": true, "peer": true, "dns": false, "internet": false}
	for name, expect := range want {
		if !got.ran(name) {
			t.Errorf("probe %q did not run", name)
		}
		if got.ok(name) != expect {
			t.Errorf("probe %q ok = %v, want %v", name, got.ok(name), expect)
		}
	}
	// A probe never emitted reports not-run and not-ok.
	if got.ran("internet_ip") || got.ok("internet_ip") {
		t.Error("absent probe must report not-run and OK=false")
	}
	if s := got.summary(); s != "host=ok peer=ok dns=fail internet=fail" {
		t.Errorf("summary = %q", s)
	}
}

func TestGuestProbeCommandSkipsEmptyTargets(t *testing.T) {
	cmd := guestProbeCommand(probeTargets{HostIP: "auto", InternetURL: "http://example.com"})
	tail := cmd[strings.LastIndex(cmd, "sh -s -- "):]
	if want := "sh -s -- auto - - http://example.com -"; tail != want {
		t.Errorf("positional args = %q, want %q", tail, want)
	}
	if !strings.Contains(cmd, "base64") {
		t.Error("expected base64 delivery in the guest command")
	}
}

func TestParseProbesRejectsMissingSentinels(t *testing.T) {
	if _, err := parseProbes("no sentinels\nPROBE host ok ping 1.2.3.4"); err == nil {
		t.Error("want error without begin sentinel")
	}
	if _, err := parseProbes("===NETPROBE-BEGIN===\nuname=Linux\n(no end)"); err == nil {
		t.Error("want error without end sentinel")
	}
	if _, err := parseProbes("===NETPROBE-BEGIN===\n===NETPROBE-END==="); err == nil {
		t.Error("want error with no PROBE lines")
	}
}

// TestProbeScriptEmbedded guards the fixture path the harness depends on.
func TestProbeScriptEmbedded(t *testing.T) {
	if !strings.Contains(probeScript, "NETPROBE-BEGIN") {
		t.Fatal("embedded netprobe.sh missing or malformed")
	}
}
