// Package wire holds the OS-neutral clipboard format vocabulary and the framed
// transport shared between the host engine and the guest agent. It carries no
// build tag and imports nothing platform-specific so the guest agent compiles
// for both macOS and Linux guests against the exact types the macOS host emits.
//
// The canonical formats are weave's analogue of Citrix's system-defined
// clipboard formats (CF_TEXT, CF_HTML, CF_DIB, …) plus the CFX_FILE file
// channel: a small, stable, MIME-shaped vocabulary that each side translates to
// its native clipboard type (macOS UTIs on the host, X11/Wayland MIME targets
// on a Linux guest).
package wire

// Canonical is an OS-neutral clipboard content type carried on the wire.
type Canonical string

const (
	CanonPlainText Canonical = "text/plain"
	CanonRTF       Canonical = "text/rtf"
	CanonHTML      Canonical = "text/html"
	CanonPNG       Canonical = "image/png"
	CanonTIFF      Canonical = "image/tiff"
	CanonPDF       Canonical = "application/pdf"
	CanonFiles     Canonical = "files"
)

// FormatClass groups canonical formats into the policy categories the user
// toggles (plain text, rich text, image) plus the independent file channel.
type FormatClass int

const (
	ClassOther FormatClass = iota
	ClassPlainText
	ClassRichText
	ClassImage
	ClassFile
)

// canonicalToUTI is the preferred macOS UTI to write for each canonical format
// when applying a payload to the host pasteboard. Literal strings are used
// deliberately: the appkit NSPasteboardType* externs resolve to the symbol
// address rather than the NSString they point to (see the historical
// workaround in clipboardwatcher.go), so the constants cannot be read at run
// time.
var canonicalToUTI = map[Canonical]string{
	CanonPlainText: "public.utf8-plain-text",
	CanonRTF:       "public.rtf",
	CanonHTML:      "public.html",
	CanonPNG:       "public.png",
	CanonTIFF:      "public.tiff",
	CanonPDF:       "com.adobe.pdf",
	CanonFiles:     "public.file-url",
}

// utiToCanonical maps every host UTI weave understands back to its canonical
// format. Several UTIs collapse to one canonical form (com.apple.flat-rtfd, a
// macOS bundle, degrades to text/rtf so it survives a Linux round-trip).
var utiToCanonical = map[string]Canonical{
	"public.utf8-plain-text":  CanonPlainText,
	"public.utf16-plain-text": CanonPlainText,
	"public.plain-text":       CanonPlainText,
	"NSStringPboardType":      CanonPlainText,
	"public.rtf":              CanonRTF,
	"com.apple.flat-rtfd":     CanonRTF,
	"public.rtfd":             CanonRTF,
	"public.html":             CanonHTML,
	"public.png":              CanonPNG,
	"public.tiff":             CanonTIFF,
	"com.adobe.pdf":           CanonPDF,
	"public.file-url":         CanonFiles,
}

// linuxMIME maps each canonical format to the X11/Wayland selection target a
// Linux guest backend reads and writes via xclip / wl-clipboard.
var linuxMIME = map[Canonical]string{
	CanonPlainText: "text/plain;charset=utf-8",
	CanonRTF:       "text/rtf",
	CanonHTML:      "text/html",
	CanonPNG:       "image/png",
	CanonTIFF:      "image/tiff",
	CanonPDF:       "application/pdf",
	CanonFiles:     "text/uri-list",
}

// ClassOf returns the policy category a canonical format belongs to.
func ClassOf(c Canonical) FormatClass {
	switch c {
	case CanonPlainText:
		return ClassPlainText
	case CanonRTF, CanonHTML:
		return ClassRichText
	case CanonPNG, CanonTIFF, CanonPDF:
		return ClassImage
	case CanonFiles:
		return ClassFile
	default:
		return ClassOther
	}
}

// CanonicalForUTI resolves a host pasteboard UTI to its canonical format.
func CanonicalForUTI(uti string) (Canonical, bool) {
	c, ok := utiToCanonical[uti]
	return c, ok
}

// UTIForCanonical returns the macOS UTI to write for a canonical format.
func UTIForCanonical(c Canonical) (string, bool) {
	uti, ok := canonicalToUTI[c]
	return uti, ok
}

// LinuxMIMEForCanonical returns the X11/Wayland target for a canonical format.
func LinuxMIMEForCanonical(c Canonical) (string, bool) {
	mime, ok := linuxMIME[c]
	return mime, ok
}

// CanonicalForLinuxMIME resolves an X11/Wayland target back to a canonical
// format (the reverse of LinuxMIMEForCanonical).
func CanonicalForLinuxMIME(mime string) (Canonical, bool) {
	for canon, m := range linuxMIME {
		if m == mime {
			return canon, true
		}
	}
	return "", false
}

// AllCanonical lists every canonical format weave can carry, richest first so
// callers that prefer fidelity (rich text before plain text) can iterate in
// order.
func AllCanonical() []Canonical {
	return []Canonical{
		CanonRTF, CanonHTML, CanonPlainText,
		CanonPNG, CanonTIFF, CanonPDF,
		CanonFiles,
	}
}
