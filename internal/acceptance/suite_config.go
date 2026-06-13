// Theme: the config command and the YAML settings file. Runs against the
// isolated $XDG_CONFIG_HOME; no VM is touched.
//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"strings"
)

func configSuite() *Suite {
	return &Suite{
		Name: "config",
		Teardown: func(h *Harness) {
			_ = os.Remove(filepath.Join(h.ConfigHome, "weave", "config.yaml"))
		},
		Cases: []Case{
			{"config get shows defaults", func(t *T, h *Harness) {
				result := h.Run("config", "get")
				t.assertExit(result, 0)
				t.assertContains(result.Stdout, "Registry: ghcr.io", "default registry")
			}},
			{"storage add creates and lists a location", func(t *T, h *Harness) {
				path := filepath.Join(h.WeaveHome, "fast-storage")
				t.assertExit(h.Run("config", "storage", "add", "fast", path), 0)
				result := h.Run("config", "storage", "list")
				t.assertExit(result, 0)
				t.assertContains(result.Stdout, "fast", "storage list")
				t.assertContains(result.Stdout, path, "storage path")
			}},
			{"storage default selects it", func(t *T, h *Harness) {
				result := h.Run("config", "storage", "default", "fast")
				t.assertExit(result, 0)
				t.assertContains(result.Stdout, "Default storage set", "default ack")
			}},
			{"settings file round-trips on disk", func(t *T, h *Harness) {
				data, err := os.ReadFile(filepath.Join(h.ConfigHome, "weave", "config.yaml"))
				if err != nil {
					t.Fatalf("reading settings file: %v", err)
				}
				yaml := string(data)
				t.assertContains(yaml, "defaultStorage: fast", "persisted default")
				t.assertContains(yaml, "storageLocations:", "persisted locations")
			}},
			{"WEAVE_HOME overrides the default storage", func(t *T, h *Harness) {
				override := filepath.Join(h.WeaveHome, "override-home")
				if err := os.MkdirAll(override, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				result := h.RunEnv([]string{"WEAVE_HOME=" + override}, "config", "get")
				t.assertExit(result, 0)
				t.assertContains(result.Stdout, override, "WEAVE_HOME precedence")
			}},
			{"registry ghcr sets the organization", func(t *T, h *Harness) {
				t.assertExit(h.Run("config", "registry", "ghcr", "--organization", "acme"), 0)
				result := h.Run("config", "registry", "status")
				t.assertExit(result, 0)
				t.assertContains(result.Stdout, "acme", "registry org")
			}},
			{"storage remove drops it and clears the default", func(t *T, h *Harness) {
				t.assertExit(h.Run("config", "storage", "remove", "fast"), 0)
				result := h.Run("config", "storage", "list")
				if strings.Contains(result.Stdout, "fast") {
					t.Errorf("location 'fast' still present after removal:\n%s", result.Stdout)
				}
			}},
		},
	}
}
