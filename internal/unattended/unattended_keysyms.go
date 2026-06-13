// SpecialKey → X11 keysym mapping for the boot command DSL. Lives here (not
// in the vnc package) because SpecialKey is part of the boot-command model;
// the raw keysym constants live in vnc. Apple's _VZVNCServer (OSXvnc-derived)
// uses an inverted modifier mapping that modifierKeysym encodes — see its
// comment. Mirrors lume's VNC/X11Keysyms.swift.
//go:build darwin

package unattended

import (
	weavevnc "github.com/deploymenttheory/weave/internal/vnc"
)

// fKeysym maps F1..F12 (contiguous from 0xFFBE).
var fKeysym = map[SpecialKey]uint32{
	SpecialKeyF1: weavevnc.KeysymF1, SpecialKeyF2: weavevnc.KeysymF1 + 1, SpecialKeyF3: weavevnc.KeysymF1 + 2,
	SpecialKeyF4: weavevnc.KeysymF1 + 3, SpecialKeyF5: weavevnc.KeysymF1 + 4, SpecialKeyF6: weavevnc.KeysymF1 + 5,
	SpecialKeyF7: weavevnc.KeysymF1 + 6, SpecialKeyF8: weavevnc.KeysymF1 + 7, SpecialKeyF9: weavevnc.KeysymF1 + 8,
	SpecialKeyF10: weavevnc.KeysymF1 + 9, SpecialKeyF11: weavevnc.KeysymF1 + 10, SpecialKeyF12: weavevnc.KeysymF1 + 11,
}

// specialKeyToKeysym maps a non-modifier SpecialKey (the main key of a key
// press or hotkey) to its X11 keysym.
func specialKeyToKeysym(key SpecialKey) uint32 {
	switch key {
	case SpecialKeyEnter:
		return weavevnc.KeysymReturn
	case SpecialKeyTab:
		return weavevnc.KeysymTab
	case SpecialKeyEscape:
		return weavevnc.KeysymEscape
	case SpecialKeyLeftSuper:
		return weavevnc.KeysymSuperL
	case SpecialKeyLeftShift:
		return weavevnc.KeysymShiftL
	case SpecialKeyLeftAlt:
		return weavevnc.KeysymAltL
	case SpecialKeyLeftCtrl:
		return weavevnc.KeysymControlL
	case SpecialKeyBackspace:
		return weavevnc.KeysymBackSpace
	case SpecialKeyDelete:
		return weavevnc.KeysymDelete
	case SpecialKeyUp:
		return weavevnc.KeysymUp
	case SpecialKeyDown:
		return weavevnc.KeysymDown
	case SpecialKeyLeft:
		return weavevnc.KeysymLeft
	case SpecialKeyRight:
		return weavevnc.KeysymRight
	case SpecialKeySpace:
		return weavevnc.KeysymSpace
	default:
		if keysym, ok := fKeysym[key]; ok {
			return keysym
		}
		return 0
	}
}

// modifierKeysym maps a modifier SpecialKey to the X11 keysym that Apple's
// _VZVNCServer interprets as the corresponding Mac modifier. The server
// (OSXvnc-derived) uses an inverted mapping: Command is delivered as Alt_L
// and Option as Meta_L; Super_L is NOT treated as Command. Sending Super_L
// for Command — as a naive RFB client would — makes every Cmd shortcut a
// no-op, which is why Spotlight (Cmd+Space) and the SSH-enable sequence
// silently fail. (Matches lume's VNCService.sendKeyPress.)
func modifierKeysym(key SpecialKey) uint32 {
	switch key {
	case SpecialKeyLeftSuper: // Command
		return weavevnc.KeysymAltL
	case SpecialKeyLeftAlt: // Option
		return weavevnc.KeysymMetaL
	case SpecialKeyLeftShift:
		return weavevnc.KeysymShiftL
	case SpecialKeyLeftCtrl:
		return weavevnc.KeysymControlL
	default:
		return specialKeyToKeysym(key)
	}
}
