// Port of lume's Unattended/AgentSetupRunner.swift: agent mode for
// unattended setup. Claude (computer-use tool) is shown JPEG screenshots of
// the VNC framebuffer downscaled to at most 1024x768 and drives the Setup
// Assistant by issuing screenshot/click/type/key actions, which are
// executed over the RFB client with coordinates scaled back up to the real
// framebuffer. Conversation history is bounded to the first message plus
// the last 20 (10 turns), as the Swift original does.
//
// The default system prompt creates a weave/weave account, matching the
// unattended presets and the ssh command defaults.
//go:build darwin

package unattended

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"strings"
	"time"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/screenviewer"
	weavevnc "github.com/deploymenttheory/weave/internal/vnc"
)

const (
	agentMaxAPIWidth  = 1024
	agentMaxAPIHeight = 768
)

const defaultAgentSystemPrompt = `You are automating the macOS Setup Assistant on a freshly installed virtual machine.
Your goal is to complete the setup as quickly as possible with these settings:

1. Language: English
2. Country: United States
3. Skip all Apple ID sign-in (look for "Set Up Later", "Skip", "Other Sign-In Options", or similar)
4. Create a local user account with:
   - Full Name: admin
   - Account Name: admin
   - Password: admin
5. Skip or decline all optional features (Siri, Analytics, Screen Time, Location Services, etc.)
6. Accept any privacy/terms screens by clicking Continue/Agree
7. Reach the macOS desktop and enable SSH

## Expected screen sequence (adapt as needed — screens may vary by macOS version):
1. "Hello" greeting animation → press Space or click to dismiss
2. Language selection → type "English", select it, press Enter
3. Country/Region → type "United States", press Enter, click Continue
4. Transfer Your Data → click "Set up as new", then Continue
5. Written and Spoken Languages → click Continue
6. Accessibility → click "Not Now"
7. Data & Privacy → click Continue
8. Create a Mac Account → fill Full Name "weave", Tab to skip Account Name (auto-fills), type password "weave", Tab, verify password "weave", Tab to skip Hint, Tab to checkbox, press Space to untoggle "Allow reset with Apple Account", click Continue
9. Apple Account sign-in → look for "Set Up Later" or "Other Sign-In Options" or "Skip". If a "Don't Skip" confirmation appears, Tab to "Skip" and press Enter
10. Terms and Conditions → click "Agree" button (at bottom, not in the text), then click "Agree" again in the confirmation popup
11. Enable Location Services → click Continue, then confirm "Don't Use" in popup
12. Select Your Time Zone → click Continue
13. Analytics → click Continue
14. Screen Time → click "Set Up Later"
15. Siri → click to enable/disable, then click Continue
16. FileVault → click "Not Now", then confirm in popup
17. Choose Your Look → click Continue
18. Update Mac Automatically → click Continue
19. Welcome/Get Started → click "Get Started"

## After reaching the desktop, enable SSH:
1. Open Spotlight (Cmd+Space), type "System Settings", press Enter
2. Search for "Remote Login" using Cmd+F, click "Remote Login"
3. Enable the Remote Login toggle, allow all users, click Done
4. Close System Settings (Cmd+Q)
5. The task is now complete.

## Important tips:
- The screenshot colors may appear tinted/shifted — this is a VNC artifact. Focus on shapes and layout.
- After each action, take a screenshot to verify the result before proceeding.
- If a button or text is not visible, try waiting 2-3 seconds, then take another screenshot.
- Use keyboard shortcuts when buttons are hard to click (Tab to navigate, Enter to confirm, Space to toggle).
- If you see a password confirmation field, enter "weave" again.
- When you see the macOS desktop with the Dock and menu bar, proceed to enable SSH.
- When you see the lock screen with the user "weave", the setup is complete — just stop.`

// AgentSetupRunner drives one agent-mode setup session.
type AgentSetupRunner struct {
	vnc           *weavevnc.VNCClient
	client        *AnthropicClient
	maxIterations int
	systemPrompt  string
	Viewer        *screenviewer.ScreenServer // optional view-only screen viewer

	apiWidth, apiHeight int // dimensions reported to the API
}

func NewAgentSetupRunner(vnc *weavevnc.VNCClient, apiKey string, model string, maxIterations int, systemPrompt string) *AgentSetupRunner {
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	if maxIterations <= 0 {
		maxIterations = 200
	}
	if systemPrompt == "" {
		systemPrompt = defaultAgentSystemPrompt
	}

	// Report scaled-down dimensions so screenshot payloads stay small;
	// incoming action coordinates are scaled back up to the framebuffer.
	apiWidth, apiHeight := vnc.Width, vnc.Height
	if apiWidth > agentMaxAPIWidth || apiHeight > agentMaxAPIHeight {
		scale := min(float64(agentMaxAPIWidth)/float64(apiWidth), float64(agentMaxAPIHeight)/float64(apiHeight))
		apiWidth = int(float64(apiWidth) * scale)
		apiHeight = int(float64(apiHeight) * scale)
	}

	return &AgentSetupRunner{
		vnc:           vnc,
		client:        NewAnthropicClient(apiKey, model, apiWidth, apiHeight),
		maxIterations: maxIterations,
		systemPrompt:  systemPrompt,
		apiWidth:      apiWidth,
		apiHeight:     apiHeight,
	}
}

// Run executes the computer-use loop until the model stops requesting
// actions or maxIterations is reached.
func (r *AgentSetupRunner) Run(ctx context.Context) error {
	initialScreenshot, err := r.captureScreenshot(ctx)
	if err != nil {
		return err
	}

	messages := []anthropicMessage{{
		Role: "user",
		Content: []anthropicContentBlock{
			anthropicTextBlock("Here is the current screen of the virtual machine. Please complete the macOS Setup Assistant."),
			anthropicImageBlock(initialScreenshot),
		},
	}}

	for iteration := 1; iteration <= r.maxIterations; iteration++ {
		fmt.Printf("Agent iteration %d/%d\n", iteration, r.maxIterations)

		response, err := r.client.SendMessage(ctx, r.systemPrompt, messages)
		if err != nil {
			return err
		}

		messages = append(messages, anthropicMessage{Role: "assistant", Content: response.Content})

		var toolResults []anthropicContentBlock
		for _, block := range response.Content {
			switch block.Type {
			case "text":
				if text := strings.TrimSpace(block.Text); text != "" {
					fmt.Println("Agent:", text)
				}
			case "tool_use":
				result := r.executeComputerAction(ctx, block.ID, block.Input)
				toolResults = append(toolResults, result)
			}
		}

		if len(toolResults) == 0 {
			fmt.Println("Agent finished (no further actions requested).")
			return nil
		}

		messages = append(messages, anthropicMessage{Role: "user", Content: toolResults})

		// Bound the history: first message (initial screenshot) + last 20.
		if len(messages) > 21 {
			first := messages[0]
			recent := messages[len(messages)-20:]
			messages = append([]anthropicMessage{first}, recent...)
		}
	}

	return weaveerrors.ErrUnattendedTimeout(fmt.Sprintf("agent did not finish within %d iterations", r.maxIterations))
}

// executeComputerAction performs one computer-use action and returns the
// tool_result block (with a fresh screenshot on success).
func (r *AgentSetupRunner) executeComputerAction(ctx context.Context, toolUseID string, input map[string]any) anthropicContentBlock {
	action, _ := input["action"].(string)
	fmt.Printf("Agent action: %s\n", action)

	failure := func(message string) anthropicContentBlock {
		return anthropicContentBlock{
			Type:      "tool_result",
			ToolUseID: toolUseID,
			IsError:   true,
			Content:   []anthropicContentBlock{anthropicTextBlock(message)},
		}
	}

	var actionErr error
	switch action {
	case "screenshot":
		// Falls through to the screenshot below.

	case "left_click", "right_click", "double_click", "mouse_move":
		x, y, ok := r.coordinateFromInput(input)
		if !ok {
			return failure("Missing coordinate for " + action)
		}
		switch action {
		case "left_click":
			actionErr = r.vnc.Click(x, y)
		case "right_click":
			if actionErr = r.vnc.PointerEvent(x, y, 0); actionErr == nil {
				time.Sleep(50 * time.Millisecond)
				if actionErr = r.vnc.PointerEvent(x, y, 4); actionErr == nil { // bit 2 = right button
					time.Sleep(80 * time.Millisecond)
					actionErr = r.vnc.PointerEvent(x, y, 0)
				}
			}
		case "double_click":
			actionErr = r.vnc.DoubleClick(x, y)
		case "mouse_move":
			actionErr = r.vnc.PointerEvent(x, y, 0)
		}
		time.Sleep(500 * time.Millisecond) // let the UI settle

	case "type":
		text, _ := input["text"].(string)
		if text == "" {
			return failure("Missing text for type action")
		}
		actionErr = r.vnc.TypeText(text, 0)
		time.Sleep(500 * time.Millisecond)

	case "key":
		text, _ := input["text"].(string)
		if text == "" {
			return failure("Missing text for key action")
		}
		actionErr = r.sendKeyChord(text)
		time.Sleep(500 * time.Millisecond)

	case "scroll", "wait":
		// Scroll support over RFB varies; both degrade to waiting and
		// re-screenshotting (AgentSetupRunner parity for scroll).
		time.Sleep(time.Second)

	default:
		return failure("Unsupported action: " + action)
	}

	if actionErr != nil {
		return failure(fmt.Sprintf("Action %q failed: %v", action, actionErr))
	}

	screenshot, err := r.captureScreenshot(ctx)
	if err != nil {
		return failure("Failed to capture screenshot: " + err.Error())
	}
	return anthropicContentBlock{
		Type:      "tool_result",
		ToolUseID: toolUseID,
		Content:   []anthropicContentBlock{anthropicImageBlock(screenshot)},
	}
}

// coordinateFromInput reads the API-space [x, y] coordinate and scales it
// to framebuffer pixels.
func (r *AgentSetupRunner) coordinateFromInput(input map[string]any) (int, int, bool) {
	raw, ok := input["coordinate"].([]any)
	if !ok || len(raw) != 2 {
		return 0, 0, false
	}
	apiX, okX := raw[0].(float64)
	apiY, okY := raw[1].(float64)
	if !okX || !okY {
		return 0, 0, false
	}
	x := int(apiX * float64(r.vnc.Width) / float64(r.apiWidth))
	y := int(apiY * float64(r.vnc.Height) / float64(r.apiHeight))
	return x, y, true
}

// sendKeyChord handles xdotool-style key strings from the computer-use
// "key" action, e.g. "Return", "cmd+space", "ctrl+shift+t".
func (r *AgentSetupRunner) sendKeyChord(chord string) error {
	parts := strings.Split(chord, "+")

	var modifiers []uint32
	for _, part := range parts[:len(parts)-1] {
		keysym := agentModifierKeysym(part)
		if keysym == 0 {
			return weaveerrors.ErrInputSimulationFailed("unknown modifier " + part)
		}
		modifiers = append(modifiers, keysym)
	}

	main := parts[len(parts)-1]
	keysym := agentKeyNameToKeysym(main)
	if keysym == 0 {
		if runes := []rune(main); len(runes) == 1 {
			if charKeysym, _, ok := weavevnc.KeysymForRune(runes[0]); ok {
				keysym = charKeysym
			}
		}
	}
	if keysym == 0 {
		return weaveerrors.ErrInputSimulationFailed("unknown key " + main)
	}

	if len(modifiers) == 0 {
		return r.vnc.PressKeysym(keysym)
	}
	return r.vnc.Hotkey(modifiers, keysym)
}

// agentModifierKeysym maps an xdotool-style modifier name to the keysym
// Apple's VNC server treats as that Mac modifier (Command via Alt_L, Option
// via Meta_L — the same inverted mapping as modifierKeysym).
func agentModifierKeysym(name string) uint32 {
	switch strings.ToLower(name) {
	case "cmd", "command", "super", "super_l", "meta":
		return weavevnc.KeysymAltL // Command
	case "alt", "option", "alt_l":
		return weavevnc.KeysymMetaL // Option
	case "ctrl", "control", "control_l":
		return weavevnc.KeysymControlL
	case "shift", "shift_l":
		return weavevnc.KeysymShiftL
	default:
		return 0
	}
}

// agentKeyNameToKeysym maps xdotool-style key names to keysyms.
func agentKeyNameToKeysym(name string) uint32 {
	switch strings.ToLower(name) {
	case "return", "enter", "kp_enter":
		return weavevnc.KeysymReturn
	case "tab":
		return weavevnc.KeysymTab
	case "escape", "esc":
		return weavevnc.KeysymEscape
	case "space":
		return weavevnc.KeysymSpace
	case "backspace":
		return weavevnc.KeysymBackSpace
	case "delete":
		return weavevnc.KeysymDelete
	case "up":
		return weavevnc.KeysymUp
	case "down":
		return weavevnc.KeysymDown
	case "left":
		return weavevnc.KeysymLeft
	case "right":
		return weavevnc.KeysymRight
	case "cmd", "command", "super", "super_l", "meta":
		return weavevnc.KeysymSuperL
	case "shift", "shift_l":
		return weavevnc.KeysymShiftL
	case "alt", "option", "alt_l":
		return weavevnc.KeysymAltL
	case "ctrl", "control", "control_l":
		return weavevnc.KeysymControlL
	default:
		if len(name) >= 2 && (name[0] == 'f' || name[0] == 'F') {
			var number int
			if _, err := fmt.Sscanf(strings.ToLower(name), "f%d", &number); err == nil && number >= 1 && number <= 12 {
				return weavevnc.KeysymF1 + uint32(number-1)
			}
		}
		return 0
	}
}

// captureScreenshot grabs the framebuffer, downscales it to the API
// dimensions and returns base64 JPEG (quality 80, AgentSetupRunner parity).
// The capture runs on a freshly redialed session: _VZVNCServer holds updates
// for an unchanged screen, so only a new connection guarantees a prompt,
// current frame.
func (r *AgentSetupRunner) captureScreenshot(ctx context.Context) (string, error) {
	if err := r.vnc.Redial(ctx); err != nil {
		return "", err
	}
	framebuffer, err := r.vnc.CaptureFramebuffer(ctx)
	if err != nil {
		return "", err
	}
	if r.Viewer != nil {
		r.Viewer.Push(framebuffer)
	}

	scaled := framebuffer
	if r.apiWidth != r.vnc.Width || r.apiHeight != r.vnc.Height {
		scaled = scaleRGBA(framebuffer, r.apiWidth, r.apiHeight)
	}

	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, scaled, &jpeg.Options{Quality: 80}); err != nil {
		return "", weaveerrors.ErrFramebufferCaptureFailed(err.Error())
	}
	return base64.StdEncoding.EncodeToString(buffer.Bytes()), nil
}

// scaleRGBA bilinearly resamples src to width x height (stdlib only).
func scaleRGBA(src *image.RGBA, width int, height int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	srcWidth := src.Bounds().Dx()
	srcHeight := src.Bounds().Dy()

	for y := 0; y < height; y++ {
		srcY := (float64(y) + 0.5) * float64(srcHeight) / float64(height)
		y0 := int(srcY - 0.5)
		fy := srcY - 0.5 - float64(y0)
		y1 := min(y0+1, srcHeight-1)
		y0 = max(y0, 0)

		for x := 0; x < width; x++ {
			srcX := (float64(x) + 0.5) * float64(srcWidth) / float64(width)
			x0 := int(srcX - 0.5)
			fx := srcX - 0.5 - float64(x0)
			x1 := min(x0+1, srcWidth-1)
			x0 = max(x0, 0)

			for channel := 0; channel < 4; channel++ {
				p00 := float64(src.Pix[(y0*srcWidth+x0)*4+channel])
				p10 := float64(src.Pix[(y0*srcWidth+x1)*4+channel])
				p01 := float64(src.Pix[(y1*srcWidth+x0)*4+channel])
				p11 := float64(src.Pix[(y1*srcWidth+x1)*4+channel])
				top := p00 + (p10-p00)*fx
				bottom := p01 + (p11-p01)*fx
				dst.Pix[(y*width+x)*4+channel] = uint8(top + (bottom-top)*fy + 0.5)
			}
		}
	}
	return dst
}
