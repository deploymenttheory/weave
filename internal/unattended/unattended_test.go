//go:build darwin

package unattended

import (
	"testing"
	"time"
)

func TestParseBootCommands(t *testing.T) {
	cases := []struct {
		input string
		check func(BootCommand) bool
	}{
		{"<wait 'English'>", func(c BootCommand) bool {
			return c.Kind == BootCommandWaitForText && c.Text == "English" && c.Timeout == 120*time.Second
		}},
		{"<wait 'Continue', timeout=300>", func(c BootCommand) bool {
			return c.Kind == BootCommandWaitForText && c.Timeout == 300*time.Second
		}},
		{"<click 'Continue'>", func(c BootCommand) bool {
			return c.Kind == BootCommandClickText && c.Text == "Continue" && !c.HasIndex
		}},
		{"<click 'Continue', yoffset=12>", func(c BootCommand) bool {
			return c.Kind == BootCommandClickText && c.YOffset == 12 && c.XOffset == 0
		}},
		{"<click 'Agree', index=-1, xoffset=-5>", func(c BootCommand) bool {
			return c.Kind == BootCommandClickText && c.HasIndex && c.Index == -1 && c.XOffset == -5
		}},
		{"<click 'Skip', offset=20>", func(c BootCommand) bool {
			return c.Kind == BootCommandClickText && c.YOffset == 20 // legacy offset = Y
		}},
		{"<click_at 100, 200>", func(c BootCommand) bool {
			return c.Kind == BootCommandClickAt && c.X == 100 && c.Y == 200
		}},
		{"<type 'hello world'>", func(c BootCommand) bool {
			return c.Kind == BootCommandTypeText && c.Text == "hello world" && c.DelayMS == 0
		}},
		{"<type 'admin', delay=100>", func(c BootCommand) bool {
			return c.Kind == BootCommandTypeText && c.DelayMS == 100
		}},
		{"<delay 2.5>", func(c BootCommand) bool {
			return c.Kind == BootCommandDelay && c.Duration == 2500*time.Millisecond
		}},
		{"<enter>", func(c BootCommand) bool {
			return c.Kind == BootCommandKeyPress && c.Key == SpecialKeyEnter
		}},
		{"<space>", func(c BootCommand) bool {
			return c.Kind == BootCommandKeyPress && c.Key == SpecialKeySpace
		}},
		{"<cmd+space>", func(c BootCommand) bool {
			return c.Kind == BootCommandHotkey && len(c.Mods) == 1 &&
				c.Mods[0] == SpecialKeyLeftSuper && c.Key == SpecialKeySpace
		}},
		{"<cmd+q>", func(c BootCommand) bool {
			return c.Kind == BootCommandHotkey && c.Char == 'q'
		}},
		{"<shift+cmd+3>", func(c BootCommand) bool {
			return c.Kind == BootCommandHotkey && len(c.Mods) == 2 && c.Char == '3'
		}},
	}

	for _, testCase := range cases {
		command, err := ParseBootCommand(testCase.input)
		if err != nil {
			t.Errorf("%q: unexpected error: %v", testCase.input, err)
			continue
		}
		if !testCase.check(command) {
			t.Errorf("%q: parsed to unexpected %+v", testCase.input, command)
		}
	}

	if _, err := ParseBootCommand("<frobnicate 'x'>"); err == nil {
		t.Error("expected error for unknown command")
	}
}

func TestEmbeddedPresetsLoad(t *testing.T) {
	presets := AvailableUnattendedPresets()
	if len(presets) < 2 {
		t.Fatalf("expected at least sequoia and tahoe presets, got %v", presets)
	}
	for _, preset := range presets {
		config, err := LoadUnattendedConfig(preset)
		if err != nil {
			t.Errorf("preset %q failed to load: %v", preset, err)
			continue
		}
		if len(config.BootCommands) == 0 {
			t.Errorf("preset %q has no boot commands", preset)
		}
		if _, err := ParseBootCommands(config.BootCommands); err != nil {
			t.Errorf("preset %q has unparseable boot commands: %v", preset, err)
		}
	}
}

func TestModifierKeysym(t *testing.T) {
	cases := map[SpecialKey]uint32{
		SpecialKeyLeftSuper: 0xFFE9, // Command -> Alt_L
		SpecialKeyLeftAlt:   0xFFE7, // Option  -> Meta_L
		SpecialKeyLeftShift: 0xFFE1,
		SpecialKeyLeftCtrl:  0xFFE3,
	}
	for key, want := range cases {
		if got := modifierKeysym(key); got != want {
			t.Errorf("modifierKeysym(%s) = %#x, want %#x", key, got, want)
		}
	}
	for _, name := range []string{"cmd", "command", "super"} {
		if got := agentModifierKeysym(name); got != 0xFFE9 {
			t.Errorf("agentModifierKeysym(%q) = %#x, want 0xFFE9", name, got)
		}
	}
}

// TestKeysymForRune pins that uppercase letters and shifted symbols are sent
// as the unshifted keysym with Shift held (matching a real keyboard).
func TestSpecialKeyToKeysym(t *testing.T) {
	cases := map[SpecialKey]uint32{
		SpecialKeyEnter:     0xFF0D,
		SpecialKeyEscape:    0xFF1B,
		SpecialKeyLeftSuper: 0xFFEB,
		SpecialKeySpace:     0x0020,
		SpecialKeyF1:        0xFFBE,
		SpecialKeyF12:       0xFFC9,
	}
	for key, want := range cases {
		if got := specialKeyToKeysym(key); got != want {
			t.Errorf("%s: keysym = %#x, want %#x", key, got, want)
		}
	}
}
