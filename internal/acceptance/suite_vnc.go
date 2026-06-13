// Theme: the experimental VNC server (_VZVNCServer via run
// --vnc-experimental). A fast empty Linux VM is booted headless with a known
// VNC password; the suite parses the printed vnc:// URL and verifies the
// server by performing the full RFB handshake and capturing one framebuffer.
//go:build darwin

package main

import (
	"regexp"
	"strconv"
	"time"
)

const vncVM = "acc-vnc"

var vncURLRegexp = regexp.MustCompile(`vnc://:([^@]+)@([\d.]+):(\d+)`)

func vncSuite() *Suite {
	const password = "acc-vnc-pw"
	var running *background

	return &Suite{
		Name: "vnc",
		Setup: func(h *Harness) error {
			h.Run("delete", vncVM)
			if r := h.Run("create", "--linux", "--disk-size", "8", vncVM); r.ExitCode != 0 {
				return errExit("create", r)
			}
			bg, err := h.Start(nil, "run", vncVM, "--vnc-experimental", "--vnc-password", password, "--no-graphics")
			if err != nil {
				return err
			}
			running = bg
			return nil
		},
		Teardown: func(h *Harness) {
			if running != nil {
				running.Stop()
			}
			h.Run("stop", vncVM)
			h.Run("delete", vncVM)
		},
		Cases: []Case{
			{"run prints the VNC URL", func(t *T, h *Harness) {
				if !running.waitForOutput("vnc://", 60*time.Second) {
					t.Fatalf("no VNC URL within 60s; output:\n%s", running.Output())
				}
				if !vncURLRegexp.MatchString(running.Output()) {
					t.Fatalf("VNC URL not in expected form:\n%s", running.Output())
				}
			}},
			{"RFB handshake and framebuffer capture succeed", func(t *T, h *Harness) {
				match := vncURLRegexp.FindStringSubmatch(running.Output())
				if match == nil {
					t.Skip("no VNC URL (prior case failed)")
				}
				host := match[2]
				port, err := strconv.Atoi(match[3])
				if err != nil {
					t.Fatalf("bad port %q: %v", match[3], err)
				}

				// The server needs a moment after printing the URL.
				var width, height int
				deadline := time.Now().Add(30 * time.Second)
				for {
					width, height, err = probeVNC(host, port, password)
					if err == nil || time.Now().After(deadline) {
						break
					}
					time.Sleep(time.Second)
				}
				if err != nil {
					t.Fatalf("RFB probe failed: %v\n--- run output ---\n%s", err, running.Output())
				}
				t.Logf("captured %dx%d framebuffer", width, height)
				if width == 0 || height == 0 {
					t.Errorf("captured an empty framebuffer (%dx%d)", width, height)
				}
			}},
		},
	}
}
