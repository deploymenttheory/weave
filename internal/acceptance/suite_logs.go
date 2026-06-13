// Theme: the file logger and the logs command. Runs commands that emit log
// lines into the isolated $WEAVE_HOME/logs, then reads them back.
//go:build darwin

package main

import "strings"

func logsSuite() *Suite {
	return &Suite{
		Name: "logs",
		Setup: func(h *Harness) error {
			// Generate one info line (command start) and one error line.
			h.Run("ip", "ghost-for-logs")
			return nil
		},
		Cases: []Case{
			{"logs info shows the command start line", func(t *T, h *Harness) {
				result := h.Run("logs", "info")
				t.assertExit(result, 0)
				t.assertContains(result.Stdout, "command \"ip\" started", "info log")
			}},
			{"logs error shows the failure", func(t *T, h *Harness) {
				result := h.Run("logs", "error")
				t.assertExit(result, 0)
				t.assertContains(result.Stdout, "does not exist", "error log")
			}},
			{"logs all interleaves both with prefixes", func(t *T, h *Harness) {
				result := h.Run("logs", "all")
				t.assertExit(result, 0)
				t.assertContains(result.Stdout, "INFO:", "info prefix")
				t.assertContains(result.Stdout, "ERROR:", "error prefix")
			}},
			{"logs --lines limits the output", func(t *T, h *Harness) {
				// Emit a few more error lines first.
				for i := 0; i < 3; i++ {
					h.Run("ip", "ghost-for-logs")
				}
				result := h.Run("logs", "error", "--lines", "1")
				t.assertExit(result, 0)
				lines := nonEmptyLines(result.Stdout)
				if len(lines) != 1 {
					t.Errorf("--lines 1 returned %d lines:\n%s", len(lines), result.Stdout)
				}
			}},
		},
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
