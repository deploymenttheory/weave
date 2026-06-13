// Port of lume's Unattended/VNCAutomation.swift: executes parsed boot
// commands against the RFB client, using OCR to locate text on screen.
// Behaviour ported: 2s OCR poll interval, click-on-text with index/offset
// selection, 200ms settle delay between commands, debug screenshots with a
// crosshair at the click point.
//go:build darwin

package unattended

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/logging"
	"github.com/deploymenttheory/weave/internal/ocr"
	"github.com/deploymenttheory/weave/internal/screenviewer"
	weavevnc "github.com/deploymenttheory/weave/internal/vnc"
)

const automationPollInterval = 2 * time.Second

// Automation drives one VNC session through a boot command list.
type Automation struct {
	vnc          *weavevnc.VNCClient
	debug        bool
	debugDir     string
	commandIndex int
	viewer       *screenviewer.ScreenServer // optional view-only screen viewer
}

func NewAutomation(vnc *weavevnc.VNCClient, debug bool, debugDir string) *Automation {
	if debug && debugDir == "" {
		debugDir = filepath.Join(os.TempDir(), fmt.Sprintf("unattended-%d", os.Getpid()))
	}
	return &Automation{vnc: vnc, debug: debug, debugDir: debugDir}
}

// logf writes to stdout and to the info log file so progress is visible both
// in the terminal and via `weave logs info -f`.
func (a *Automation) logf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Println(msg)
	logging.LogInfo("[automation] %s", msg)
}

// pushFrame forwards a freshly captured framebuffer to the screen viewer, if
// one is attached.
func (a *Automation) pushFrame(img *image.RGBA) {
	if a.viewer != nil {
		a.viewer.Push(img)
	}
}

// delay waits for d, pushing viewer frames every 2s on the automation's own
// (single-threaded) VNC connection. Each viewer capture uses a fresh
// connection (see captureWithRetry for why); errors are ignored — a missed
// viewer frame is cosmetic and must not distort the preset's timing.
func (a *Automation) delay(ctx context.Context, d time.Duration) error {
	deadline := time.Now().Add(d)
	lastCapture := time.Now()
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil
		}
		wait := min(remaining, 500*time.Millisecond)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		if a.viewer != nil && time.Since(lastCapture) >= automationPollInterval {
			lastCapture = time.Now()
			a.redial(ctx)
			if framebuffer, err := a.vnc.CaptureFramebuffer(ctx); err == nil {
				a.pushFrame(framebuffer)
			}
		}
	}
}

// ExecuteAll runs every command, pausing 200ms between commands for
// stability (VNCAutomation.executeAll).
func (a *Automation) ExecuteAll(ctx context.Context, commands []BootCommand) error {
	if a.debug {
		_ = os.MkdirAll(a.debugDir, 0o755)
		a.logf("debug mode enabled — saving screenshots to %s", a.debugDir)
	}

	for index, command := range commands {
		a.commandIndex = index + 1
		a.logf("[%d/%d] %s", a.commandIndex, len(commands), describeBootCommand(command))
		if err := a.Execute(ctx, command); err != nil {
			logging.LogError("[automation] command %d/%d failed: %s — %v",
				a.commandIndex, len(commands), describeBootCommand(command), err)
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return nil
}

// Execute runs one command.
func (a *Automation) Execute(ctx context.Context, command BootCommand) error {
	switch command.Kind {
	case BootCommandWaitForText:
		return a.waitForText(ctx, command.Text, command.Timeout)

	case BootCommandClickText:
		index := (*int)(nil)
		if command.HasIndex {
			index = &command.Index
		}
		return a.clickOnText(ctx, command.Text, command.XOffset, command.YOffset, index)

	case BootCommandClickAt:
		return a.vnc.Click(command.X, command.Y)

	case BootCommandTypeText:
		return a.vnc.TypeText(command.Text, command.DelayMS)

	case BootCommandKeyPress:
		keysym := specialKeyToKeysym(command.Key)
		if keysym == 0 {
			return weaveerrors.ErrInputSimulationFailed(fmt.Sprintf("unknown key %q", command.Key))
		}
		return a.vnc.PressKeysym(keysym)

	case BootCommandHotkey:
		modifiers := make([]uint32, 0, len(command.Mods))
		for _, modifier := range command.Mods {
			modifiers = append(modifiers, modifierKeysym(modifier))
		}
		main := specialKeyToKeysym(command.Key)
		if command.Char != 0 {
			keysym, _, ok := weavevnc.KeysymForRune(command.Char)
			if !ok {
				return weaveerrors.ErrInputSimulationFailed(fmt.Sprintf("untypeable hotkey character %q", command.Char))
			}
			main = keysym
		}
		return a.vnc.Hotkey(modifiers, main)

	case BootCommandDelay:
		return a.delay(ctx, command.Duration)

	default:
		return weaveerrors.ErrInputSimulationFailed("unknown boot command kind")
	}
}

func (a *Automation) waitForText(ctx context.Context, text string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		remaining := int(time.Until(deadline).Seconds())
		// Fresh connection per poll: a held connection only receives an
		// update when the screen changes, so polling it would stall for the
		// whole capture timeout on every unchanged frame.
		a.redial(ctx)
		framebuffer, err := a.vnc.CaptureFramebuffer(ctx)
		if err != nil {
			logging.LogInfo("[automation] waitForText %q: capture error (%ds remaining): %v", text, remaining, err)
		} else {
			a.pushFrame(framebuffer)
			observations, ocrErr := ocr.RecognizeText(framebuffer)
			if ocrErr != nil {
				logging.LogInfo("[automation] waitForText %q: OCR error (%ds remaining): %v", text, remaining, ocrErr)
			} else if _, found := ocr.FindText(text, observations); found {
				a.logf("text %q found on screen", text)
				return nil
			} else {
				// Log every visible word so the caller can diagnose a stall.
				visible := make([]string, 0, len(observations))
				for _, obs := range observations {
					visible = append(visible, fmt.Sprintf("%q", obs.Text))
				}
				logging.LogInfo("[automation] waitForText %q: not found (%ds remaining); screen shows: %s",
					text, remaining, strings.Join(visible, ", "))
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(automationPollInterval):
		}
	}

	return weaveerrors.ErrTextNotFound(text, int(timeout.Seconds()))
}

// captureWithRetry captures a framebuffer on a freshly redialed session,
// retrying a few times on VNC errors. _VZVNCServer answers exactly one
// FramebufferUpdateRequest per connection while the screen is static (later
// requests are held until the content changes), so the only way to get a
// guaranteed-current frame is a fresh connection per capture — the same
// pattern the MCP screen tools use.
func (a *Automation) captureWithRetry(ctx context.Context) (*image.RGBA, error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			logging.LogInfo("[automation] captureWithRetry: attempt %d/4 after error: %v", attempt+1, lastErr)
		}
		a.redial(ctx)
		framebuffer, err := a.vnc.CaptureFramebuffer(ctx)
		if err == nil {
			a.pushFrame(framebuffer)
			return framebuffer, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	logging.LogError("[automation] captureWithRetry: all 4 attempts failed; last error: %v", lastErr)
	return nil, lastErr
}

// redial re-establishes the automation's VNC session. Called before every
// capture (see captureWithRetry), so success is silent; a failed redial is
// logged and left for the following capture attempt to surface.
func (a *Automation) redial(ctx context.Context) {
	if err := a.vnc.Redial(ctx); err != nil {
		logging.LogError("[automation] VNC redial failed: %v", err)
	}
}

// clickOnText locates text via OCR and clicks its centre — but only once the
// match's position is stable across two consecutive captures. Freshly
// rendered UI (a search-results dropdown sliding in, a list reflowing as
// entries load) moves between the OCR frame and the pointer event, so a
// single capture→click silently misses; both Setup Assistant failures of
// 2026-06-11 (Language list, System Settings search results) were this race.
func (a *Automation) clickOnText(ctx context.Context, text string, xOffset int, yOffset int, index *int) error {
	const stabilityAttempts = 5
	const stabilityTolerance = 3 // pixels

	var framebuffer *image.RGBA
	var observation *ocr.TextObservation
	var lastCenter image.Point
	var seenOnce bool

	for attempt := 1; attempt <= stabilityAttempts; attempt++ {
		captured, err := a.captureWithRetry(ctx)
		if err != nil {
			return err
		}
		observations, err := ocr.RecognizeText(captured)
		if err != nil {
			return err
		}

		candidate := selectMatch(ocr.FindAllText(text, observations), index)
		if candidate == nil {
			if attempt == stabilityAttempts {
				visible := make([]string, 0, len(observations))
				for _, obs := range observations {
					visible = append(visible, fmt.Sprintf("%q", obs.Text))
				}
				logging.LogError("[automation] clickOnText: %q not found; screen shows: %s", text, strings.Join(visible, ", "))
				if a.debug {
					a.saveDebugScreenshot(captured, nil, text, true)
				}
				return weaveerrors.ErrTextNotFound(text, 0)
			}
		} else {
			center := candidate.Center()
			if seenOnce && abs(center.X-lastCenter.X) <= stabilityTolerance && abs(center.Y-lastCenter.Y) <= stabilityTolerance {
				framebuffer, observation = captured, candidate
				break
			}
			if seenOnce {
				logging.LogInfo("[automation] clickOnText: %q moved from (%d,%d) to (%d,%d); waiting for it to settle",
					text, lastCenter.X, lastCenter.Y, center.X, center.Y)
			}
			lastCenter, seenOnce = center, true
			if attempt == stabilityAttempts {
				// Never settled: click the last seen position as a best effort.
				framebuffer, observation = captured, candidate
				logging.LogInfo("[automation] clickOnText: %q did not settle after %d captures; clicking anyway", text, stabilityAttempts)
				break
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(400 * time.Millisecond):
		}
	}

	center := observation.Center()
	clickPoint := image.Pt(center.X+xOffset, center.Y+yOffset)
	a.logf("clicking %q at (%d, %d)", text, clickPoint.X, clickPoint.Y)

	if a.debug {
		a.saveDebugScreenshot(framebuffer, &clickPoint, text, false)
	}
	return a.vnc.Click(clickPoint.X, clickPoint.Y)
}

// selectMatch picks the match addressed by index: nil index = first match,
// non-negative = that position, negative = from the end (-1 = last).
func selectMatch(matches []ocr.TextObservation, index *int) *ocr.TextObservation {
	switch {
	case len(matches) == 0:
		return nil
	case index == nil:
		return &matches[0]
	case *index >= 0:
		if *index < len(matches) {
			return &matches[*index]
		}
		return nil
	default:
		if positive := len(matches) + *index; positive >= 0 {
			return &matches[positive]
		}
		return nil
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// saveDebugScreenshot writes the framebuffer with a red crosshair at the
// click point (when any) to the debug directory.
func (a *Automation) saveDebugScreenshot(framebuffer *image.RGBA, clickPoint *image.Point, text string, failed bool) {
	marked := image.NewRGBA(framebuffer.Bounds())
	copy(marked.Pix, framebuffer.Pix)

	if clickPoint != nil {
		red := color.RGBA{R: 0xFF, A: 0xFF}
		for d := -10; d <= 10; d++ {
			if x := clickPoint.X + d; x >= 0 && x < marked.Bounds().Dx() && clickPoint.Y >= 0 && clickPoint.Y < marked.Bounds().Dy() {
				marked.SetRGBA(x, clickPoint.Y, red)
			}
			if y := clickPoint.Y + d; y >= 0 && y < marked.Bounds().Dy() && clickPoint.X >= 0 && clickPoint.X < marked.Bounds().Dx() {
				marked.SetRGBA(clickPoint.X, y, red)
			}
		}
	}

	status := "click"
	if failed {
		status = "notfound"
	}
	path := filepath.Join(a.debugDir, fmt.Sprintf("%03d-%s-%s.png", a.commandIndex, status, sanitizeFileName(text)))
	file, err := os.Create(path)
	if err != nil {
		return
	}
	defer file.Close()
	_ = png.Encode(file, marked)
}

func sanitizeFileName(name string) string {
	sanitized := make([]rune, 0, len(name))
	for _, character := range name {
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9':
			sanitized = append(sanitized, character)
		default:
			sanitized = append(sanitized, '_')
		}
		if len(sanitized) >= 32 {
			break
		}
	}
	return string(sanitized)
}

func describeBootCommand(command BootCommand) string {
	switch command.Kind {
	case BootCommandWaitForText:
		return fmt.Sprintf("wait for %q (timeout: %ds)", command.Text, int(command.Timeout.Seconds()))
	case BootCommandClickText:
		if command.HasIndex {
			return fmt.Sprintf("click %q (index: %d, xoffset: %d, yoffset: %d)", command.Text, command.Index, command.XOffset, command.YOffset)
		}
		if command.XOffset != 0 || command.YOffset != 0 {
			return fmt.Sprintf("click %q (xoffset: %d, yoffset: %d)", command.Text, command.XOffset, command.YOffset)
		}
		return fmt.Sprintf("click %q", command.Text)
	case BootCommandClickAt:
		return fmt.Sprintf("click at (%d, %d)", command.X, command.Y)
	case BootCommandTypeText:
		return fmt.Sprintf("type %q", command.Text)
	case BootCommandKeyPress:
		return fmt.Sprintf("press <%s>", command.Key)
	case BootCommandHotkey:
		if command.Char != 0 {
			return fmt.Sprintf("hotkey %v+%c", command.Mods, command.Char)
		}
		return fmt.Sprintf("hotkey %v+%s", command.Mods, command.Key)
	case BootCommandDelay:
		return fmt.Sprintf("delay %.1fs", command.Duration.Seconds())
	default:
		return "unknown"
	}
}
