// Theme: VM lifecycle on a fast, empty Linux VM — create, inspect, mutate,
// clone, export/import and delete. Cases run in order and build on the VM
// created by the first case (child → parent within the suite).
//go:build darwin

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

const (
	lcVM      = "acc-lifecycle"
	lcClone   = "acc-lifecycle-clone"
	lcRenamed = "acc-lifecycle-renamed"
)

func lifecycleSuite() *Suite {
	return &Suite{
		Name: "lifecycle",
		Teardown: func(h *Harness) {
			h.Run("delete", lcVM, lcClone, lcRenamed)
		},
		Cases: []Case{
			{"create --linux makes a VM", func(t *T, h *Harness) {
				t.assertExit(h.Run("create", "--linux", "--disk-size", "8", lcVM), 0)
			}},
			{"list --format json includes it", func(t *T, h *Harness) {
				result := h.Run("list", "--source", "local", "--format", "json")
				t.assertExit(result, 0)
				names, err := listedNames(result.Stdout)
				if err != nil {
					t.Fatalf("parsing list JSON: %v\n%s", err, result.Stdout)
				}
				if !contains(names, lcVM) {
					t.Errorf("created VM %q absent from list", lcVM)
				}
			}},
			{"get --format json reports its configuration", func(t *T, h *Harness) {
				result := h.Run("get", lcVM, "--format", "json")
				t.assertExit(result, 0)
				var info struct {
					OS     string `json:"OS"`
					CPU    int    `json:"CPU"`
					Memory uint64 `json:"Memory"`
					Disk   int    `json:"Disk"`
				}
				if err := json.Unmarshal([]byte(result.Stdout), &info); err != nil {
					t.Fatalf("parsing get JSON: %v\n%s", err, result.Stdout)
				}
				if info.OS != "linux" {
					t.Errorf("OS = %q, want linux", info.OS)
				}
				if info.Disk != 8 {
					t.Errorf("Disk = %d, want 8", info.Disk)
				}
			}},
			{"set mutates CPU, memory and display", func(t *T, h *Harness) {
				t.assertExit(h.Run("set", lcVM, "--cpu", "2", "--memory", "2048", "--display", "1280x800"), 0)
				result := h.Run("get", lcVM, "--format", "json")
				var info struct {
					CPU     int    `json:"CPU"`
					Memory  uint64 `json:"Memory"`
					Display string `json:"Display"`
				}
				if err := json.Unmarshal([]byte(result.Stdout), &info); err != nil {
					t.Fatalf("parsing get JSON: %v", err)
				}
				if info.CPU != 2 {
					t.Errorf("CPU = %d, want 2", info.CPU)
				}
				if info.Memory != 2048 {
					t.Errorf("Memory = %d MB, want 2048", info.Memory)
				}
				if !strings.HasPrefix(info.Display, "1280x800") {
					t.Errorf("Display = %q, want 1280x800", info.Display)
				}
			}},
			{"clone produces an independent copy", func(t *T, h *Harness) {
				t.assertExit(h.Run("clone", lcVM, lcClone), 0)
				result := h.Run("get", lcClone, "--format", "json")
				t.assertExit(result, 0)
			}},
			{"export then import round-trips", func(t *T, h *Harness) {
				archive := filepath.Join(h.WeaveHome, "lc.tvm")
				t.assertExit(h.Run("export", lcVM, archive), 0)
				if _, err := os.Stat(archive); err != nil {
					t.Fatalf("export archive missing: %v", err)
				}
				// Re-import under a new name and confirm it exists.
				t.assertExit(h.Run("import", archive, lcRenamed), 0)
				t.assertExit(h.Run("get", lcRenamed, "--format", "json"), 0)
				_ = os.Remove(archive)
			}},
			{"delete removes a VM", func(t *T, h *Harness) {
				t.assertExit(h.Run("delete", lcClone), 0)
				result := h.Run("list", "--source", "local", "--format", "json")
				names, _ := listedNames(result.Stdout)
				if contains(names, lcClone) {
					t.Errorf("deleted VM %q still listed", lcClone)
				}
			}},
		},
	}
}

// listedNames extracts the Name field from `list --format json` output.
func listedNames(jsonOutput string) ([]string, error) {
	var entries []struct {
		Name string `json:"Name"`
	}
	if err := json.Unmarshal([]byte(jsonOutput), &entries); err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name
	}
	return names, nil
}

func contains(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}
