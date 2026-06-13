// X11 keysym constants and character mapping for the RFB client. Raw RFB
// speaks X11 keysyms. Apple's _VZVNCServer (derived from OSXvnc) applies a
// non-obvious, inverted modifier mapping — Command is delivered as Alt_L and
// Option as Meta_L; the unattended package's modifierKeysym encodes it.
// Mirrors lume's VNC/X11Keysyms.swift.
//go:build darwin

package vnc

const (
	KeysymBackSpace uint32 = 0xFF08
	KeysymTab       uint32 = 0xFF09
	KeysymReturn    uint32 = 0xFF0D
	KeysymEscape    uint32 = 0xFF1B
	KeysymDelete    uint32 = 0xFFFF
	KeysymLeft      uint32 = 0xFF51
	KeysymUp        uint32 = 0xFF52
	KeysymRight     uint32 = 0xFF53
	KeysymDown      uint32 = 0xFF54
	KeysymShiftL    uint32 = 0xFFE1
	KeysymControlL  uint32 = 0xFFE3
	KeysymMetaL     uint32 = 0xFFE7 // delivered as Option on Apple's VNC server
	KeysymAltL      uint32 = 0xFFE9 // delivered as Command on Apple's VNC server
	KeysymSuperL    uint32 = 0xFFEB // NOT treated as Command by Apple's server
	KeysymSpace     uint32 = 0x0020
	KeysymF1        uint32 = 0xFFBE
)

// shiftedSymbolKeysym maps shifted ASCII symbols to the keysym of the
// physical (unshifted) key that produces them with Shift held.
var shiftedSymbolKeysym = map[rune]uint32{
	'!': 0x31, '@': 0x32, '#': 0x33, '$': 0x34, '%': 0x35,
	'^': 0x36, '&': 0x37, '*': 0x38, '(': 0x39, ')': 0x30,
	'_': 0x2d, '+': 0x3d, '{': 0x5b, '}': 0x5d, '|': 0x5c,
	':': 0x3b, '"': 0x27, '<': 0x2c, '>': 0x2e, '?': 0x2f,
	'~': 0x60,
}

// KeysymForRune returns the keysym for a typed character, whether Shift must
// be held, and whether it is typeable. Uppercase letters and shifted symbols
// are sent as the unshifted key's keysym with Shift held, which is how a
// physical keyboard produces them — sending the already-shifted keysym while
// also holding Shift produces the wrong character on Apple's server.
// (Matches lume's charToKeysym.)
func KeysymForRune(character rune) (keysym uint32, needShift bool, ok bool) {
	switch character {
	case '\n', '\r':
		return KeysymReturn, false, true
	case '\t':
		return KeysymTab, false, true
	case ' ':
		return KeysymSpace, false, true
	}
	if character >= 0x20 && character <= 0x7E {
		if character >= 'A' && character <= 'Z' {
			return uint32(character - 'A' + 'a'), true, true
		}
		if unshifted, isShifted := shiftedSymbolKeysym[character]; isShifted {
			return unshifted, true, true
		}
		return uint32(character), false, true
	}
	if character >= 0xA0 && character <= 0xFF {
		return uint32(character), false, true
	}
	return 0, false, false
}
