// Gated VNC screen diagnostic for live _VZVNCServer debugging. It drives the
// real automation engine against a running VM and records evidence (PNG frame
// dumps plus OCR text) for every step, which is how the 2026-06-11
// one-update-per-connection capture stall and the drifting Language-screen
// arrow coordinate were diagnosed.
//
// Usage (VM must be running with --vnc-experimental):
//
//	WEAVE_VNC_ENDPOINT=$(cat <vmdir>/.vnc-endpoint) \
//	WEAVE_VNC_PROBE_COMMANDS="<wait 'Language', timeout=180> ; <enter> ; <delay 3>" \
//	go test ./example/weave/ -run TestVNCScreenDiagnostic -v -timeout 600s
//
// WEAVE_VNC_ENDPOINT  vnc://:password@host:port (required; gates the test)
// WEAVE_VNC_PROBE_COMMANDS  optional ";"-separated boot-command DSL executed
//
//	via the production Automation engine; when empty the diagnostic just
//	captures, OCRs and saves the current screen.
//
// Frames land in /tmp/weave-vnc-diagnostic/ — inspect them visually.
//go:build darwin

package unattended

import (
	"context"
	"fmt"
	"image/png"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/deploymenttheory/weave/internal/ocr"
	weavevnc "github.com/deploymenttheory/weave/internal/vnc"
)

func TestVNCScreenDiagnostic(t *testing.T) {
	endpoint := os.Getenv("WEAVE_VNC_ENDPOINT")
	if endpoint == "" {
		t.Skip("set WEAVE_VNC_ENDPOINT (vnc://:password@host:port) to run the VNC diagnostic")
	}
	match := VNCURLPattern.FindStringSubmatch(endpoint)
	if match == nil {
		t.Fatalf("malformed endpoint %q", endpoint)
	}
	port, err := strconv.Atoi(match[3])
	if err != nil {
		t.Fatal(err)
	}

	const frameDir = "/tmp/weave-vnc-diagnostic"
	if err := os.MkdirAll(frameDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	vnc, err := weavevnc.DialVNC(ctx, match[2], port, match[1])
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer vnc.Close()
	t.Logf("connected: %dx%d framebuffer", vnc.Width, vnc.Height)

	// snapshot captures on a fresh connection (the server answers only one
	// update request per connection while the screen is static), saves the
	// frame and logs the OCR-visible text.
	snapshot := func(label string) {
		if err := vnc.Redial(ctx); err != nil {
			t.Fatalf("%s: redial: %v", label, err)
		}
		img, err := vnc.CaptureFramebuffer(ctx)
		if err != nil {
			t.Fatalf("%s: capture: %v", label, err)
		}
		path := fmt.Sprintf("%s/%s.png", frameDir, label)
		if file, err := os.Create(path); err == nil {
			_ = png.Encode(file, img)
			_ = file.Close()
		}
		observations, err := ocr.RecognizeText(img)
		if err != nil {
			t.Fatalf("%s: OCR: %v", label, err)
		}
		texts := make([]string, 0, len(observations))
		for _, observation := range observations {
			centre := observation.Center()
			texts = append(texts, fmt.Sprintf("%q@(%d,%d)", observation.Text, centre.X, centre.Y))
		}
		t.Logf("%s (saved %s): %s", label, path, strings.Join(texts, " "))
	}

	snapshot("000-initial")

	script := os.Getenv("WEAVE_VNC_PROBE_COMMANDS")
	if script == "" {
		return
	}

	automation := NewAutomation(vnc, false, "")
	for i, raw := range strings.Split(script, ";") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		command, err := ParseBootCommand(raw)
		if err != nil {
			t.Fatalf("command %d %q: %v", i+1, raw, err)
		}
		t.Logf("executing %d: %s", i+1, describeBootCommand(command))
		if err := automation.Execute(ctx, command); err != nil {
			snapshot(fmt.Sprintf("%03d-FAILED", i+1))
			t.Fatalf("command %d %q failed: %v", i+1, raw, err)
		}
		snapshot(fmt.Sprintf("%03d-after", i+1))
	}
}
