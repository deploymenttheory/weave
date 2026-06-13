// In-guest network reachability probing for the behavioral network suite.
//
// It exists to prove — from inside a VM — what a network-isolation profile
// actually does at the packet level: can the guest reach the internet, resolve
// DNS, reach its host/gateway, reach a peer VM? Those are the behavioural facts
// a profile promises, and the only honest way to verify them is to ask the
// guest. The probe battery itself is the committed fixture
// fixtures/network/netprobe.sh; this file delivers it into a guest over a
// transport-agnostic Runner and parses the PROBE lines it prints.
//go:build darwin

package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
)

// probeScript is the netprobe battery, loaded from the embedded fixtures tree
// (see fixture.go's fixturesFS).
var probeScript = mustReadFixture("fixtures/network/netprobe.sh")

func mustReadFixture(path string) string {
	data, err := fixturesFS.ReadFile(path)
	if err != nil {
		panic("acceptance: reading embedded fixture " + path + ": " + err.Error())
	}
	return string(data)
}

// guestRunner executes a shell command inside a guest and returns its combined
// output and exit code. Implementations wrap whatever channel reaches the
// guest — `weave exec` (vsock guest agent, works under any network isolation)
// or `weave ssh` (only when the guest is host-reachable).
type guestRunner interface {
	RunGuest(ctx context.Context, shellCommand string) (output string, exitCode int, err error)
}

// probeTargets selects which probes to run and against what. An empty field
// skips that probe, so a scenario asks only the questions meaningful to it.
type probeTargets struct {
	// HostIP pings the VM host/gateway. The literal "auto" makes the script
	// discover and ping the guest's default gateway (the host's NAT/vmnet
	// router in Apple's shared and host modes).
	HostIP string
	// PeerIP pings another VM.
	PeerIP string
	// DNSName is resolved (resolution only, no fetch) to isolate DNS from egress.
	DNSName string
	// InternetURL is fetched by name (DNS + egress + reachability together).
	InternetURL string
	// InternetIP is an http://<ip>/ URL fetched to test egress without DNS,
	// distinguishing "no DNS" from "no route out".
	InternetIP string
}

// probe is the outcome of one reachability check.
type probe struct {
	Name   string // host | peer | dns | internet | internet_ip
	OK     bool
	Detail string // the command the guest ran, e.g. "ping 192.168.64.1"
}

// reachability is the parsed result of a probe battery.
type reachability struct {
	Uname  string
	Probes []probe
	Raw    string
}

func (r reachability) get(name string) (probe, bool) {
	for _, p := range r.Probes {
		if p.Name == name {
			return p, true
		}
	}
	return probe{}, false
}

// ok reports whether the named probe ran and succeeded. A probe that did not
// run reports false.
func (r reachability) ok(name string) bool {
	p, found := r.get(name)
	return found && p.OK
}

func (r reachability) ran(name string) bool {
	_, found := r.get(name)
	return found
}

// summary renders the matrix compactly for evidence, e.g.
// "internet=ok dns=ok host=fail peer=fail".
func (r reachability) summary() string {
	parts := make([]string, 0, len(r.Probes))
	for _, p := range r.Probes {
		state := "fail"
		if p.OK {
			state = "ok"
		}
		parts = append(parts, p.Name+"="+state)
	}
	return strings.Join(parts, " ")
}

// guestProbeCommand returns the single shell command that runs the probe
// battery for targets inside a guest and prints its PROBE lines. The script is
// delivered base64-encoded and decoded in-guest with whichever base64 flavour
// is present (GNU --decode, BSD -D, or openssl), needing no writable guest
// filesystem or file-copy channel.
func guestProbeCommand(targets probeTargets) string {
	arg := func(value string) string {
		if value == "" {
			return "-"
		}
		return value
	}
	positional := fmt.Sprintf("%s %s %s %s %s",
		arg(targets.HostIP),
		arg(targets.PeerIP),
		arg(targets.DNSName),
		arg(targets.InternetURL),
		arg(targets.InternetIP),
	)
	encoded := base64.StdEncoding.EncodeToString([]byte(probeScript))
	return fmt.Sprintf(
		"printf %%s '%s' | { base64 --decode 2>/dev/null || base64 -D 2>/dev/null || openssl base64 -d; } | sh -s -- %s",
		encoded, positional,
	)
}

// runProbes delivers and executes the probe battery in the guest via runner
// and parses the result. A non-nil error means the battery could not be run
// (guest unreachable or channel failure) — distinct from a probe reporting
// unreachability, which is a successful run with OK=false.
func runProbes(ctx context.Context, runner guestRunner, targets probeTargets) (reachability, error) {
	output, _, err := runner.RunGuest(ctx, guestProbeCommand(targets))
	if err != nil {
		return reachability{Raw: output}, fmt.Errorf("running probe battery in guest: %w", err)
	}
	return parseProbes(output)
}

// parseProbes extracts the reachability matrix from guest output, tolerating
// surrounding noise (login banners, motd) by reading only between the netprobe
// sentinels.
func parseProbes(output string) (reachability, error) {
	const begin, end = "===NETPROBE-BEGIN===", "===NETPROBE-END==="
	_, after, found := strings.Cut(output, begin)
	if !found {
		return reachability{Raw: output}, fmt.Errorf("netprobe: begin sentinel not found in guest output:\n%s", output)
	}
	body, _, found := strings.Cut(after, end)
	if !found {
		return reachability{Raw: output}, fmt.Errorf("netprobe: end sentinel not found in guest output:\n%s", output)
	}

	result := reachability{Raw: output}
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "uname="):
			result.Uname = strings.TrimPrefix(line, "uname=")
		case strings.HasPrefix(line, "PROBE "):
			fields := strings.SplitN(line, " ", 4)
			if len(fields) < 3 {
				continue
			}
			p := probe{Name: fields[1], OK: fields[2] == "ok"}
			if len(fields) == 4 {
				p.Detail = fields[3]
			}
			result.Probes = append(result.Probes, p)
		}
	}
	if len(result.Probes) == 0 {
		return result, fmt.Errorf("netprobe: no PROBE lines parsed from guest output:\n%s", output)
	}
	return result, nil
}
