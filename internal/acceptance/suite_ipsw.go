// Theme: network-dependent lookups (ipsw). Gated behind -net because it
// reaches Apple's servers.
//go:build darwin

package main

import "strings"

func ipswSuite() *Suite {
	return &Suite{
		Name: "ipsw",
		Cases: []Case{
			{"ipsw prints the latest restore image URL", func(t *T, h *Harness) {
				result := h.Run("ipsw")
				if result.ExitCode != 0 {
					t.Skip("ipsw lookup failed (network?): %s", strings.TrimSpace(result.Stderr))
				}
				// Output is the spinner status line(s) plus the URL line; find
				// the URL line rather than assuming stdout is only the URL.
				var url string
				for _, line := range strings.Split(result.Stdout, "\n") {
					if line = strings.TrimSpace(line); strings.HasPrefix(line, "https://") {
						url = line
					}
				}
				if url == "" || !strings.HasSuffix(url, ".ipsw") {
					t.Errorf("no valid IPSW URL in output:\n%s", result.Stdout)
				}
			}},
		},
	}
}
