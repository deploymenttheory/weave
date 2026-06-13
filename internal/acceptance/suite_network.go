// Theme: networking scenarios, driven by committed full-VM fixtures. Each
// fixture (fixtures/network/*.json) is a complete VM definition — guest OS (and
// macOS restore image), disk size/format, CPU/memory/display, run mode, and the
// per-NIC network topology. The suite builds each VM to that spec, forces a
// weave load→save round-trip, and asserts both the VM definition (via `get`)
// and the persisted network topology (via config.json) — validating the full
// definition → loader → saver workflow end to end, one scenario at a time with
// teardown between. It also keeps deterministic CLI-contract cases (flag
// validation) that need no VM.
//
// Linux fixtures are entitlement-free and run in full. macOS fixtures need a
// cached restore image and a long install, so they are skipped unless one is
// available (Harness.IPSW) — the skip keeps the macOS scenario documented and
// ready for the gated/provision path.
//go:build darwin

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// nicJSON mirrors the persisted NICConfig fields the suite inspects.
type nicJSON struct {
	Mode             string `json:"mode"`
	MACAddress       string `json:"macAddress"`
	IsPrimary        bool   `json:"isPrimary"`
	BridgedInterface string `json:"bridgedInterface"`
	SoftnetBlock     string `json:"softnetBlock"`
	SoftnetHostMode  bool   `json:"softnetHostMode"`
	VmnetMode        string `json:"vmnetMode"`
}

// configFileJSON is the subset of config.json the suite reads for NIC checks.
type configFileJSON struct {
	MACAddress string    `json:"macAddress"`
	NICs       []nicJSON `json:"nics"`
}

// getInfoJSON is the subset of `get --format json` the suite asserts the VM
// definition against.
type getInfoJSON struct {
	OS         string `json:"OS"`
	CPU        int    `json:"CPU"`
	Memory     uint64 `json:"Memory"`
	Disk       int    `json:"Disk"`
	DiskFormat string `json:"DiskFormat"`
	Display    string `json:"Display"`
}

func readConfigFile(h *Harness, name string) (configFileJSON, error) {
	path := filepath.Join(h.WeaveHome, "vms", name, "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return configFileJSON{}, err
	}
	var config configFileJSON
	if err := json.Unmarshal(data, &config); err != nil {
		return configFileJSON{}, err
	}
	return config, nil
}

func primaryNIC(config configFileJSON) (nicJSON, bool) {
	for _, nic := range config.NICs {
		if nic.IsPrimary {
			return nic, true
		}
	}
	if len(config.NICs) > 0 {
		return config.NICs[0], true
	}
	return nicJSON{}, false
}

const netValidationVM = "acc-net-base"

func networkSuite() *Suite {
	fixtures, fixtureErr := loadVMFixtures()

	suite := &Suite{
		Name: "network",
		Setup: func(h *Harness) error {
			if fixtureErr != nil {
				return fmt.Errorf("loading VM fixtures: %w", fixtureErr)
			}
			// A throwaway VM for the CLI-contract (validation) cases.
			if result := h.Run("create", "--linux", "--disk-size", "8", netValidationVM); result.ExitCode != 0 {
				return errExit("create base VM", result)
			}
			return nil
		},
		Teardown: func(h *Harness) {
			names := []string{netValidationVM, "acc-net-bogus"}
			for _, fixture := range fixtures {
				names = append(names, fixtureVMName(fixture))
			}
			h.Run(append([]string{"delete"}, names...)...)
		},
	}

	// One end-to-end case per committed fixture.
	for _, fixture := range fixtures {
		name := fmt.Sprintf("fixture %s: %s", fixture.Name, fixture.Description)
		suite.Cases = append(suite.Cases, Case{name, func(t *T, h *Harness) {
			runVMFixture(t, h, fixture)
		}})
	}

	// Deterministic CLI-contract cases (no live networking required). Each
	// records the binary's actual rejection message as evidence.
	suite.Cases = append(suite.Cases,
		Case{"create --net-profile bogus is rejected", func(t *T, h *Harness) {
			result := h.Run("create", "--linux", "--disk-size", "8", "--net-profile", "bogus", "acc-net-bogus")
			t.assertExit(result, 1)
			t.assertContains(result.Stderr, "network profile", "bogus profile error")
			t.Evidence("rejected with: %s", firstLine(result.Stderr))
		}},
		Case{"run rejects --net-profile + --net-device together", func(t *T, h *Harness) {
			result := h.Run("run", netValidationVM, "--net-profile", "nat", "--net-device", "nat")
			t.assertExit(result, 1)
			t.assertContains(result.Stderr, "mutually exclusive", "profile+device conflict")
			t.Evidence("rejected with: %s", firstLine(result.Stderr))
		}},
		Case{"run rejects legacy --net-bridged + --net-softnet together", func(t *T, h *Harness) {
			result := h.Run("run", netValidationVM, "--net-bridged", "en0", "--net-softnet")
			t.assertExit(result, 1)
			t.assertContains(result.Stderr, "mutually exclusive", "legacy net conflict")
			t.Evidence("rejected with: %s", firstLine(result.Stderr))
		}},
		Case{"run rejects a bogus --net-profile", func(t *T, h *Harness) {
			result := h.Run("run", netValidationVM, "--net-profile", "bogus")
			t.assertExit(result, 1)
			t.assertContains(result.Stderr, "network profile", "bogus run profile")
			t.Evidence("rejected with: %s", firstLine(result.Stderr))
		}},
		Case{"run rejects a bad --net-device spec", func(t *T, h *Harness) {
			result := h.Run("run", netValidationVM, "--net-device", "wifi")
			t.assertExit(result, 1)
			t.assertContains(result.Stderr, "net-device", "bad device spec")
			t.Evidence("rejected with: %s", firstLine(result.Stderr))
		}},
	)

	return suite
}

func fixtureVMName(fixture vmFixture) string { return "acc-net-fx-" + fixture.Name }

// runVMFixture builds a VM to the fixture's full definition, forces a weave
// load→save round-trip, then asserts both the VM definition and the persisted
// network topology.
func runVMFixture(t *T, h *Harness, fixture vmFixture) {
	vmName := fixtureVMName(fixture)

	// Create the guest per the spec. macOS needs a cached restore image and a
	// long install — skip (don't fail) when one is not available.
	createVMForFixture(t, h, vmName, fixture.VM)

	// Overwrite the topology with the fixture, then force weave's own
	// load→save round-trip (which also applies the CPU/memory/display spec),
	// proving the topology and sizing survive load→save — not just our JSON.
	if err := h.applyNICTopology(vmName, fixture.Network.NICs); err != nil {
		t.Fatalf("applying fixture %q topology: %v", fixture.Name, err)
	}
	t.assertExit(h.Run("set", vmName,
		"--cpu", fmt.Sprintf("%d", fixture.VM.CPU),
		"--memory", fmt.Sprintf("%d", fixture.VM.MemoryMB),
		"--display", fixture.VM.Display.String()), 0)

	// Assert the VM definition through the real loader.
	assertVMDefinition(t, h, vmName, fixture)

	// Assert the persisted, round-tripped network topology.
	config, err := readConfigFile(h, vmName)
	if err != nil {
		t.Fatalf("reading round-tripped config for %q: %v", fixture.Name, err)
	}
	assertNICTopology(t, fixture, config)

	// Blow it away before the next scenario.
	t.assertExit(h.Run("delete", vmName), 0)
}

// createVMForFixture creates the base VM matching the fixture's guest OS, disk
// size and format. macOS fixtures are skipped without a cached IPSW.
func createVMForFixture(t *T, h *Harness, vmName string, spec vmSpec) {
	switch spec.OS {
	case "linux":
		t.assertExit(h.Run("create", "--linux",
			"--disk-size", fmt.Sprintf("%d", spec.DiskSizeGB),
			"--disk-format", spec.DiskFormat, vmName), 0)
	case "macos":
		// A macOS install is a long (~tens of minutes) operation, so it is
		// opt-in only: set WEAVE_ACC_MACOS_IPSW to a restore-image path/URL (or
		// "latest") to run macOS fixtures. We deliberately do NOT trigger on a
		// merely-cached IPSW, so the fast network suite never blocks on an
		// install.
		ipsw := os.Getenv("WEAVE_ACC_MACOS_IPSW")
		if ipsw == "" {
			t.Skip("macOS fixture is opt-in: set WEAVE_ACC_MACOS_IPSW to a restore image (path/URL/\"latest\") to run it")
		}
		if ipsw == "latest" && spec.IPSW != "" && spec.IPSW != "latest" {
			ipsw = spec.IPSW
		}
		result := h.RunTimeout(40*time.Minute, nil, "create", "--from-ipsw", ipsw,
			"--disk-size", fmt.Sprintf("%d", spec.DiskSizeGB),
			"--disk-format", spec.DiskFormat, vmName)
		t.assertExit(result, 0)
	default:
		t.Fatalf("unsupported fixture OS %q", spec.OS)
	}
}

// assertVMDefinition checks `get --format json` against the fixture's VM spec.
func assertVMDefinition(t *T, h *Harness, vmName string, fixture vmFixture) {
	result := h.Run("get", vmName, "--format", "json")
	t.assertExit(result, 0)

	var info getInfoJSON
	if err := json.Unmarshal([]byte(result.Stdout), &info); err != nil {
		t.Fatalf("parsing get JSON for %q: %v\n%s", fixture.Name, err, result.Stdout)
	}

	wantOS := "linux"
	if fixture.VM.OS == "macos" {
		wantOS = "macOS"
	}
	if info.OS != wantOS {
		t.Errorf("fixture %q: OS = %q, want %q", fixture.Name, info.OS, wantOS)
	}
	if info.CPU != fixture.VM.CPU {
		t.Errorf("fixture %q: CPU = %d, want %d", fixture.Name, info.CPU, fixture.VM.CPU)
	}
	if info.Memory != fixture.VM.MemoryMB {
		t.Errorf("fixture %q: Memory = %d MB, want %d MB", fixture.Name, info.Memory, fixture.VM.MemoryMB)
	}
	if info.Disk != fixture.VM.DiskSizeGB {
		t.Errorf("fixture %q: Disk = %d GB, want %d GB", fixture.Name, info.Disk, fixture.VM.DiskSizeGB)
	}
	if info.DiskFormat != fixture.VM.DiskFormat {
		t.Errorf("fixture %q: DiskFormat = %q, want %q", fixture.Name, info.DiskFormat, fixture.VM.DiskFormat)
	}
	if info.Display != fixture.VM.Display.String() {
		t.Errorf("fixture %q: Display = %q, want %q", fixture.Name, info.Display, fixture.VM.Display.String())
	}

	// Record what the loader actually reported, proving the definition
	// round-trip with the observed values rather than the fixture's.
	t.Evidence("get reported: OS=%s CPU=%d memory=%dMB disk=%dGB format=%s display=%s",
		info.OS, info.CPU, info.Memory, info.Disk, info.DiskFormat, info.Display)
}

// assertNICTopology runs the generic + expectation checks against the
// persisted, round-tripped network topology, recording the observed settings
// as evidence so a passing case shows exactly what was verified.
func assertNICTopology(t *T, fixture vmFixture, config configFileJSON) {
	wantCount := fixture.Expect.NICCount
	if wantCount == 0 {
		wantCount = len(fixture.Network.NICs)
	}
	if len(config.NICs) != wantCount {
		t.Fatalf("fixture %q: persisted %d NICs, want %d", fixture.Name, len(config.NICs), wantCount)
	}
	t.Evidence("config.json persisted %d NIC(s) after load→save round-trip", len(config.NICs))
	for index, nic := range config.NICs {
		t.Evidence("nic[%d]: %s", index, describeNIC(nic))
	}

	primary, ok := primaryNIC(config)
	if !ok {
		t.Fatalf("fixture %q: no primary NIC persisted", fixture.Name)
	}

	// The primary NIC's MAC must mirror the legacy top-level macAddress.
	if primary.MACAddress != config.MACAddress {
		t.Errorf("fixture %q: primary NIC MAC %q != config MAC %q",
			fixture.Name, primary.MACAddress, config.MACAddress)
	} else {
		t.Evidence("primary NIC MAC %s mirrors top-level macAddress", primary.MACAddress)
	}

	if want := fixture.Expect.PrimaryMode; want != "" {
		if primary.Mode != want {
			t.Errorf("fixture %q: primary mode = %q, want %q", fixture.Name, primary.Mode, want)
		} else {
			t.Evidence("primary mode = %q as expected", primary.Mode)
		}
	}
	if want := fixture.Expect.PrimarySoftnetBlock; want != nil {
		if primary.SoftnetBlock != *want {
			t.Errorf("fixture %q: primary softnetBlock = %q, want %q", fixture.Name, primary.SoftnetBlock, *want)
		} else if *want == "" {
			t.Evidence("primary softnetBlock unset as expected")
		} else {
			t.Evidence("primary softnetBlock = %q as expected", primary.SoftnetBlock)
		}
	}
	if want := fixture.Expect.PrimaryVmnetMode; want != "" {
		if primary.VmnetMode != want {
			t.Errorf("fixture %q: primary vmnetMode = %q, want %q", fixture.Name, primary.VmnetMode, want)
		} else {
			t.Evidence("primary vmnetMode = %q as expected", primary.VmnetMode)
		}
	}

	if fixture.Expect.DistinctMACs {
		seen := map[string]bool{}
		for _, nic := range config.NICs {
			if seen[nic.MACAddress] {
				t.Errorf("fixture %q: duplicate NIC MAC %q", fixture.Name, nic.MACAddress)
			}
			seen[nic.MACAddress] = true
		}
		if !t.failed {
			t.Evidence("all %d NIC MACs distinct", len(config.NICs))
		}
	}
}

// firstLine returns the first non-empty line of s, trimmed — used to quote a
// rejection message as evidence without dumping a full usage block.
func firstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// describeNIC renders the persisted settings of one NIC, including only the
// fields that are set so the evidence line stays scannable.
func describeNIC(nic nicJSON) string {
	parts := []string{fmt.Sprintf("mode=%s mac=%s", nic.Mode, nic.MACAddress)}
	if nic.IsPrimary {
		parts = append(parts, "primary")
	}
	if nic.BridgedInterface != "" {
		parts = append(parts, "bridged="+nic.BridgedInterface)
	}
	if nic.SoftnetBlock != "" {
		parts = append(parts, "softnetBlock="+nic.SoftnetBlock)
	}
	if nic.SoftnetHostMode {
		parts = append(parts, "softnetHostMode")
	}
	if nic.VmnetMode != "" {
		parts = append(parts, "vmnetMode="+nic.VmnetMode)
	}
	return strings.Join(parts, " ")
}
