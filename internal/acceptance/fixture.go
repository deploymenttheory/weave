// Fixture-driven scenarios. Each scenario is a committed JSON fixture under
// fixtures/<theme>/ describing a *complete* VM definition — the guest OS (and
// macOS restore image), disk size/format, CPU/memory/display, how it is run
// (headless/windowed, VNC), and its per-NIC network topology — plus the
// assertions to check. The harness creates the VM to that spec, writes the
// network topology into its config.json, forces a weave load→save round-trip,
// and re-reads the result so a suite can assert the full
// definition → loader → saver workflow is valid. One VM per fixture, torn down
// before the next: committed fixtures give traceability, the round-trip gives
// strong config validation.
//go:build darwin

package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed fixtures
var fixturesFS embed.FS

// vmFixture is one committed scenario: a full VM definition plus expectations.
type vmFixture struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	VM          vmSpec      `json:"vm"`
	Run         runSpec     `json:"run"`
	Network     networkSpec `json:"network"`
	Expect      expectSpec  `json:"expect"`
}

// vmSpec is the create-time definition of the guest.
type vmSpec struct {
	OS         string      `json:"os"`         // "linux" | "macos"
	IPSW       string      `json:"ipsw"`       // macOS only: "latest" | URL | path
	MacOSLabel string      `json:"macosLabel"` // macOS only: human version label for traceability
	DiskSizeGB int         `json:"diskSizeGB"`
	DiskFormat string      `json:"diskFormat"` // "raw" | "asif"
	CPU        int         `json:"cpu"`
	MemoryMB   uint64      `json:"memoryMB"`
	Display    displaySpec `json:"display"`
}

type displaySpec struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

func (d displaySpec) String() string { return fmt.Sprintf("%dx%d", d.Width, d.Height) }

// runSpec is how the VM is intended to be booted. It is declarative: the
// hermetic config suite records and validates it; the (gated) live-boot layer
// consumes it to drive the run command's flags.
type runSpec struct {
	Headless bool   `json:"headless"`
	VNC      string `json:"vnc"` // "none" | "screensharing" | "experimental"
}

type networkSpec struct {
	NICs []map[string]any `json:"nics"`
}

// expectSpec declares the network-topology assertions. Zero/absent fields are
// not checked, except PrimarySoftnetBlock which uses a pointer so an empty
// expectation ("must be unset") is explicit.
type expectSpec struct {
	NICCount            int     `json:"nicCount"`
	PrimaryMode         string  `json:"primaryMode"`
	PrimarySoftnetBlock *string `json:"primarySoftnetBlock"`
	PrimaryVmnetMode    string  `json:"primaryVmnetMode"`
	DistinctMACs        bool    `json:"distinctMACs"`
}

// validVNCModes is the recognised set for runSpec.VNC.
var validVNCModes = map[string]bool{"": true, "none": true, "screensharing": true, "experimental": true}

// loadVMFixtures reads and parses every committed network fixture, sorted by
// file name (the numeric prefix fixes scenario order), validating each.
func loadVMFixtures() ([]vmFixture, error) {
	entries, err := fixturesFS.ReadDir("fixtures/network")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	fixtures := make([]vmFixture, 0, len(names))
	for _, name := range names {
		data, err := fixturesFS.ReadFile("fixtures/network/" + name)
		if err != nil {
			return nil, err
		}
		var fixture vmFixture
		if err := json.Unmarshal(data, &fixture); err != nil {
			return nil, fmt.Errorf("parsing fixture %s: %w", name, err)
		}
		if err := validateFixture(fixture); err != nil {
			return nil, fmt.Errorf("fixture %s: %w", name, err)
		}
		fixtures = append(fixtures, fixture)
	}
	return fixtures, nil
}

func validateFixture(fixture vmFixture) error {
	switch fixture.VM.OS {
	case "linux", "macos":
	default:
		return fmt.Errorf("vm.os = %q, want linux|macos", fixture.VM.OS)
	}
	if fixture.VM.OS == "macos" && fixture.VM.IPSW == "" {
		return fmt.Errorf("macos fixture needs vm.ipsw")
	}
	if fixture.VM.DiskSizeGB <= 0 {
		return fmt.Errorf("vm.diskSizeGB must be > 0")
	}
	if fixture.VM.CPU <= 0 || fixture.VM.MemoryMB == 0 {
		return fmt.Errorf("vm.cpu and vm.memoryMB must be set")
	}
	if !validVNCModes[fixture.Run.VNC] {
		return fmt.Errorf("run.vnc = %q, want none|screensharing|experimental", fixture.Run.VNC)
	}
	if len(fixture.Network.NICs) == 0 {
		return fmt.Errorf("network.nics must not be empty")
	}
	return nil
}

// applyNICTopology writes the fixture's NIC topology into the VM's config.json,
// binding the primary NIC's MAC to the VM's generated address and requiring
// non-primary NICs to carry an explicit MAC (so the persisted config is fully
// determined by, and traceable to, the fixture).
func (h *Harness) applyNICTopology(vmName string, nics []map[string]any) error {
	path := filepath.Join(h.WeaveHome, "vms", vmName, "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}

	baseMAC, _ := object["macAddress"].(string)
	if baseMAC == "" {
		return fmt.Errorf("base VM %s has no macAddress to bind the primary NIC to", vmName)
	}

	out := make([]map[string]any, 0, len(nics))
	for _, source := range nics {
		nic := maps.Clone(source)
		if isPrimaryNIC(nic) {
			nic["macAddress"] = baseMAC
		} else if mac, _ := nic["macAddress"].(string); mac == "" {
			return fmt.Errorf("non-primary NIC needs an explicit macAddress")
		}
		out = append(out, nic)
	}
	object["nics"] = out

	encoded, err := json.MarshalIndent(object, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o644)
}

func isPrimaryNIC(nic map[string]any) bool {
	primary, _ := nic["isPrimary"].(bool)
	return primary
}
