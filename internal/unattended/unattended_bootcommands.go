// Port of lume's Unattended/BootCommandParser.swift: the packer-style boot
// command DSL used by unattended setup presets, e.g. <wait 'English'>,
// <click 'Continue', yoffset=10>, <type 'hello', delay=100>, <cmd+space>,
// <enter>, <delay 2>.
//go:build darwin

package unattended

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
)

// bootCommandDefaultWaitTimeout mirrors BootCommandParser.defaultWaitTimeout.
const bootCommandDefaultWaitTimeout = 120 * time.Second

// SpecialKey mirrors lume's SpecialKey enum (raw values preserved).
type SpecialKey string

const (
	SpecialKeyEnter     SpecialKey = "enter"
	SpecialKeyTab       SpecialKey = "tab"
	SpecialKeyEscape    SpecialKey = "esc"
	SpecialKeyLeftSuper SpecialKey = "leftSuper"
	SpecialKeyLeftShift SpecialKey = "leftShift"
	SpecialKeyLeftAlt   SpecialKey = "leftAlt"
	SpecialKeyLeftCtrl  SpecialKey = "leftCtrl"
	SpecialKeyBackspace SpecialKey = "backspace"
	SpecialKeyDelete    SpecialKey = "delete"
	SpecialKeyUp        SpecialKey = "up"
	SpecialKeyDown      SpecialKey = "down"
	SpecialKeyLeft      SpecialKey = "left"
	SpecialKeyRight     SpecialKey = "right"
	SpecialKeySpace     SpecialKey = "space"
	SpecialKeyF1        SpecialKey = "f1"
	SpecialKeyF2        SpecialKey = "f2"
	SpecialKeyF3        SpecialKey = "f3"
	SpecialKeyF4        SpecialKey = "f4"
	SpecialKeyF5        SpecialKey = "f5"
	SpecialKeyF6        SpecialKey = "f6"
	SpecialKeyF7        SpecialKey = "f7"
	SpecialKeyF8        SpecialKey = "f8"
	SpecialKeyF9        SpecialKey = "f9"
	SpecialKeyF10       SpecialKey = "f10"
	SpecialKeyF11       SpecialKey = "f11"
	SpecialKeyF12       SpecialKey = "f12"
)

// BootCommandKind discriminates BootCommand (Go stand-in for the Swift enum
// with associated values).
type BootCommandKind int

const (
	BootCommandWaitForText BootCommandKind = iota
	BootCommandClickText
	BootCommandClickAt
	BootCommandTypeText
	BootCommandKeyPress
	BootCommandHotkey
	BootCommandDelay
)

// BootCommand is one parsed automation step.
type BootCommand struct {
	Kind BootCommandKind

	Text     string        // waitForText, clickText, typeText
	Timeout  time.Duration // waitForText
	HasIndex bool          // clickText: whether Index applies
	Index    int           // clickText: 0 = first match, -1 = last
	XOffset  int           // clickText
	YOffset  int           // clickText
	X, Y     int           // clickAt
	DelayMS  int           // typeText inter-character delay (0 = none)
	Key      SpecialKey    // keyPress / hotkey main key (when HotkeyChar is 0)
	Mods     []SpecialKey  // hotkey modifiers
	Char     rune          // hotkey character main key (0 when Key is used)
	Duration time.Duration // delay
}

var (
	bootWaitPattern      = regexp.MustCompile(`^<wait\s+['"](.+?)['"]\s*(?:,\s*timeout\s*=\s*(\d+))?\s*>$`)
	bootClickTextPattern = regexp.MustCompile(`^<click\s+['"](.+?)['"]\s*>$`)
	bootClickParamsText  = regexp.MustCompile(`^<click\s+['"](.+?)['"]`)
	bootXOffsetPattern   = regexp.MustCompile(`xoffset\s*=\s*(-?\d+)`)
	bootYOffsetPattern   = regexp.MustCompile(`yoffset\s*=\s*(-?\d+)`)
	bootIndexPattern     = regexp.MustCompile(`index\s*=\s*(-?\d+)`)
	bootOffsetPattern    = regexp.MustCompile(`offset\s*=\s*(-?\d+)`)
	bootClickAtPattern   = regexp.MustCompile(`^<click_at\s+(\d+)\s*,\s*(\d+)\s*>$`)
	bootTypePattern      = regexp.MustCompile(`^<type\s+['"](.*)['"](?:\s*,\s*delay\s*=\s*(\d+))?\s*>$`)
	bootDelayPattern     = regexp.MustCompile(`^<delay\s+([\d.]+)\s*>$`)
	bootHotkeyPattern    = regexp.MustCompile(`^<([\w+]+)>$`)
	bootKeyPattern       = regexp.MustCompile(`^<(\w+)>$`)
)

// bootKeyMap maps DSL key names to SpecialKey (BootCommandParser.swift's
// keyMap; the hotkey and single-key variants are merged — they only differ
// by the leftsuper/leftshift/... aliases, which are harmless for hotkeys).
var bootKeyMap = map[string]SpecialKey{
	"enter": SpecialKeyEnter, "return": SpecialKeyEnter,
	"tab": SpecialKeyTab,
	"esc": SpecialKeyEscape, "escape": SpecialKeyEscape,
	"leftsuper": SpecialKeyLeftSuper, "command": SpecialKeyLeftSuper,
	"cmd": SpecialKeyLeftSuper, "super": SpecialKeyLeftSuper,
	"leftshift": SpecialKeyLeftShift, "shift": SpecialKeyLeftShift,
	"leftalt": SpecialKeyLeftAlt, "option": SpecialKeyLeftAlt, "alt": SpecialKeyLeftAlt,
	"leftctrl": SpecialKeyLeftCtrl, "ctrl": SpecialKeyLeftCtrl, "control": SpecialKeyLeftCtrl,
	"backspace": SpecialKeyBackspace,
	"delete":    SpecialKeyDelete,
	"up":        SpecialKeyUp, "down": SpecialKeyDown,
	"left": SpecialKeyLeft, "right": SpecialKeyRight,
	"space": SpecialKeySpace,
	"f1":    SpecialKeyF1, "f2": SpecialKeyF2, "f3": SpecialKeyF3, "f4": SpecialKeyF4,
	"f5": SpecialKeyF5, "f6": SpecialKeyF6, "f7": SpecialKeyF7, "f8": SpecialKeyF8,
	"f9": SpecialKeyF9, "f10": SpecialKeyF10, "f11": SpecialKeyF11, "f12": SpecialKeyF12,
}

var bootModifierKeys = map[string]bool{
	"command": true, "cmd": true, "super": true, "shift": true,
	"option": true, "alt": true, "ctrl": true, "control": true,
}

// ParseBootCommand parses one DSL string.
func ParseBootCommand(commandString string) (BootCommand, error) {
	input := strings.TrimSpace(commandString)

	if match := bootWaitPattern.FindStringSubmatch(input); match != nil {
		command := BootCommand{Kind: BootCommandWaitForText, Text: match[1], Timeout: bootCommandDefaultWaitTimeout}
		if match[2] != "" {
			seconds, _ := strconv.Atoi(match[2])
			command.Timeout = time.Duration(seconds) * time.Second
		}
		return command, nil
	}

	// <click 'text', xoffset=…, yoffset=…, index=…> (parameters require a comma).
	if strings.Contains(input, ",") {
		if match := bootClickParamsText.FindStringSubmatch(input); match != nil {
			command := BootCommand{Kind: BootCommandClickText, Text: match[1]}
			if m := bootXOffsetPattern.FindStringSubmatch(input); m != nil {
				command.XOffset, _ = strconv.Atoi(m[1])
			}
			if m := bootYOffsetPattern.FindStringSubmatch(input); m != nil {
				command.YOffset, _ = strconv.Atoi(m[1])
			}
			if m := bootIndexPattern.FindStringSubmatch(input); m != nil {
				command.Index, _ = strconv.Atoi(m[1])
				command.HasIndex = true
			}
			// Legacy <click 'text', offset=N> means a Y offset.
			if !command.HasIndex && command.XOffset == 0 && command.YOffset == 0 {
				if m := bootOffsetPattern.FindStringSubmatch(input); m != nil {
					command.YOffset, _ = strconv.Atoi(m[1])
				}
			}
			if command.HasIndex || command.XOffset != 0 || command.YOffset != 0 {
				return command, nil
			}
		}
	}

	if match := bootClickTextPattern.FindStringSubmatch(input); match != nil {
		return BootCommand{Kind: BootCommandClickText, Text: match[1]}, nil
	}

	if match := bootClickAtPattern.FindStringSubmatch(input); match != nil {
		x, _ := strconv.Atoi(match[1])
		y, _ := strconv.Atoi(match[2])
		return BootCommand{Kind: BootCommandClickAt, X: x, Y: y}, nil
	}

	if match := bootTypePattern.FindStringSubmatch(input); match != nil {
		command := BootCommand{Kind: BootCommandTypeText, Text: match[1]}
		if match[2] != "" {
			command.DelayMS, _ = strconv.Atoi(match[2])
		}
		return command, nil
	}

	if match := bootDelayPattern.FindStringSubmatch(input); match != nil {
		seconds, err := strconv.ParseFloat(match[1], 64)
		if err == nil {
			return BootCommand{Kind: BootCommandDelay, Duration: time.Duration(seconds * float64(time.Second))}, nil
		}
	}

	// Hotkey combination, e.g. <cmd+space>, <shift+cmd+3>, <cmd+q>.
	if match := bootHotkeyPattern.FindStringSubmatch(input); match != nil && strings.Contains(match[1], "+") {
		parts := strings.Split(strings.ToLower(match[1]), "+")
		if len(parts) >= 2 {
			command := BootCommand{Kind: BootCommandHotkey}
			valid := true
			for i, part := range parts {
				if i == len(parts)-1 {
					if key, ok := bootKeyMap[part]; ok {
						command.Key = key
					} else if len(part) == 1 {
						command.Char = rune(part[0])
					} else {
						valid = false
					}
				} else {
					if key, ok := bootKeyMap[part]; ok && bootModifierKeys[part] {
						command.Mods = append(command.Mods, key)
					} else {
						valid = false
					}
				}
			}
			if valid {
				return command, nil
			}
		}
	}

	// Single special key, e.g. <enter>, <space>, <f2>.
	if match := bootKeyPattern.FindStringSubmatch(input); match != nil {
		if key, ok := bootKeyMap[strings.ToLower(match[1])]; ok {
			return BootCommand{Kind: BootCommandKeyPress, Key: key}, nil
		}
	}

	return BootCommand{}, weaveerrors.UnattendedErrorf(weaveerrors.UnattendedErrorCommandExecutionFailed, "Unknown boot command: %s", input)
}

// ParseBootCommands parses a preset's boot_commands list.
func ParseBootCommands(commands []string) ([]BootCommand, error) {
	parsed := make([]BootCommand, 0, len(commands))
	for _, command := range commands {
		bootCommand, err := ParseBootCommand(command)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, bootCommand)
	}
	return parsed, nil
}
