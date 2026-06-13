// Theme: CLI surface and the exit-code contract. No VM is touched.
//go:build darwin

package main

func cliSuite() *Suite {
	return &Suite{
		Name: "cli",
		Cases: []Case{
			{"version prints", func(t *T, h *Harness) {
				result := h.Run("--version")
				t.assertExit(result, 0)
				if result.Stdout == "" {
					t.Errorf("--version produced no output")
				}
			}},
			{"unknown subcommand exits 1 and lists subcommands", func(t *T, h *Harness) {
				result := h.Run("frobnicate")
				t.assertExit(result, 1)
				t.assertContains(result.Stderr, "subcommands:", "unknown subcommand")
				t.assertContains(result.Stderr, "create", "subcommand list")
			}},
			{"ip on missing VM exits 2", func(t *T, h *Harness) {
				result := h.Run("ip", "ghost")
				t.assertExit(result, 2)
				t.assertContains(result.Stderr, "does not exist", "missing VM error")
			}},
			{"stop on missing VM exits 2", func(t *T, h *Harness) {
				t.assertExit(h.Run("stop", "ghost"), 2)
			}},
			{"get on missing VM exits 2", func(t *T, h *Harness) {
				t.assertExit(h.Run("get", "ghost"), 2)
			}},
			{"usage error exits 1", func(t *T, h *Harness) {
				// create with no name is a usage/validation error, not a VM error.
				t.assertExit(h.Run("create"), 1)
			}},
		},
	}
}
