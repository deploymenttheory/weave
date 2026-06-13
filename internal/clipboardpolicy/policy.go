// Package clipboardpolicy defines weave's enterprise clipboard policy: the
// configurable, Citrix-style controls over guest⇄host clipboard redirection.
// It is a pure-Go leaf package (no build tag, no platform imports) so it can be
// shared by the VM config, the global settings, the CLI, and the host engine
// without creating an import cycle through the clipboard package.
//
// Citrix mapping:
//   - Client Clipboard Redirection (Enabled/Prohibited) → Enabled
//   - Restrict client clipboard write (no session→client) → DirectionHostToGuest
//   - Restrict session clipboard write (no client→session) → DirectionGuestToHost
//   - Per-format allow-list (CF_TEXT, CF_HTML, CF_DIB…) → Formats + AllowedTypes
//   - CFX_RICHTEXT (preserve Office formatting) → Formats.RichText
//   - CFX_FILE (independent of format rules) → FileTransfer
//   - Transfer size limit → MaxContentBytes
package clipboardpolicy

import "github.com/deploymenttheory/weave/internal/clipboard/wire"

// Direction is the permitted flow of clipboard data between host and guest.
type Direction string

const (
	// DirectionDisabled blocks the clipboard entirely (Citrix "Prohibited").
	DirectionDisabled Direction = "disabled"
	// DirectionBidirectional permits both host→guest and guest→host (default).
	DirectionBidirectional Direction = "bidirectional"
	// DirectionHostToGuest permits only host→guest (Citrix "restrict client
	// clipboard write": the session cannot write back to the client).
	DirectionHostToGuest Direction = "hostToGuest"
	// DirectionGuestToHost permits only guest→host (Citrix "restrict session
	// clipboard write": the client cannot write into the session).
	DirectionGuestToHost Direction = "guestToHost"
)

// Formats toggles the clipboard content categories that may be redirected. The
// file channel is governed separately by Policy.FileTransfer (matching Citrix's
// independent CFX_FILE control).
type Formats struct {
	PlainText bool `json:"plainText" yaml:"plainText"`
	RichText  bool `json:"richText" yaml:"richText"` // RTF + RTFD + HTML
	Image     bool `json:"image" yaml:"image"`       // PNG + TIFF + PDF
}

// Policy is the full clipboard policy for one VM session.
type Policy struct {
	Enabled         bool      `json:"enabled" yaml:"enabled"`
	Direction       Direction `json:"direction" yaml:"direction"`
	Formats         Formats   `json:"formats" yaml:"formats"`
	FileTransfer    bool      `json:"fileTransfer" yaml:"fileTransfer"`
	AllowedTypes    []string  `json:"allowedTypes,omitempty" yaml:"allowedTypes,omitempty"`
	MaxContentBytes int64     `json:"maxContentBytes,omitempty" yaml:"maxContentBytes,omitempty"`
	SessionMbps     int       `json:"sessionMbps,omitempty" yaml:"sessionMbps,omitempty"`
	BandwidthPct    int       `json:"bandwidthPct,omitempty" yaml:"bandwidthPct,omitempty"`
}

// DefaultMaxContentBytes is the per-item/file transfer cap when unset.
const DefaultMaxContentBytes int64 = 50 << 20 // 50 MiB

// Default returns the permissive baseline: enabled, bidirectional, all content
// categories and file transfer on, a 50 MiB cap, and no bandwidth limit.
func Default() Policy {
	return Policy{
		Enabled:         true,
		Direction:       DirectionBidirectional,
		Formats:         Formats{PlainText: true, RichText: true, Image: true},
		FileTransfer:    true,
		MaxContentBytes: DefaultMaxContentBytes,
	}
}

// Active reports whether the engine should run: the policy is enabled and at
// least one direction is permitted.
func (p Policy) Active() bool {
	return p.Enabled && p.Direction != DirectionDisabled
}

// AllowHostToGuest reports whether host→guest transfers are permitted.
func (p Policy) AllowHostToGuest() bool {
	return p.Enabled && (p.Direction == DirectionBidirectional || p.Direction == DirectionHostToGuest)
}

// AllowGuestToHost reports whether guest→host transfers are permitted.
func (p Policy) AllowGuestToHost() bool {
	return p.Enabled && (p.Direction == DirectionBidirectional || p.Direction == DirectionGuestToHost)
}

// MaxBytes returns the effective per-item/file size cap.
func (p Policy) MaxBytes() int64 {
	if p.MaxContentBytes > 0 {
		return p.MaxContentBytes
	}
	return DefaultMaxContentBytes
}

// BytesPerSec returns the clipboard's bandwidth budget in bytes per second, or
// 0 for unlimited. It is BandwidthPct of the declared SessionMbps.
func (p Policy) BytesPerSec() int64 {
	if p.SessionMbps <= 0 || p.BandwidthPct <= 0 {
		return 0
	}
	pct := min(p.BandwidthPct, 100)
	// SessionMbps is megabits/sec → bytes/sec is *1e6/8, then take the percent.
	return int64(p.SessionMbps) * 1_000_000 / 8 * int64(pct) / 100
}

// AllowedCanonical expands the category toggles and any explicit AllowedTypes
// allow-list into the set of canonical formats this policy permits. AllowedTypes
// (canonical strings, e.g. "text/html") is authoritative when non-empty,
// matching Citrix's fine-grained per-format control; otherwise the category
// toggles drive the set. The file channel is included only when FileTransfer is
// on, regardless of the format toggles (Citrix CFX_FILE independence).
func (p Policy) AllowedCanonical() map[wire.Canonical]bool {
	allowed := map[wire.Canonical]bool{}

	if len(p.AllowedTypes) > 0 {
		for _, t := range p.AllowedTypes {
			canon := wire.Canonical(t)
			if wire.ClassOf(canon) == wire.ClassFile {
				continue // the file channel is governed by FileTransfer only
			}
			if _, ok := wire.UTIForCanonical(canon); ok {
				allowed[canon] = true
			}
		}
	} else {
		for _, canon := range wire.AllCanonical() {
			switch wire.ClassOf(canon) {
			case wire.ClassPlainText:
				if p.Formats.PlainText {
					allowed[canon] = true
				}
			case wire.ClassRichText:
				if p.Formats.RichText {
					allowed[canon] = true
				}
			case wire.ClassImage:
				if p.Formats.Image {
					allowed[canon] = true
				}
			}
		}
	}

	if p.FileTransfer {
		allowed[wire.CanonFiles] = true
	}
	return allowed
}

// Override carries optional per-field CLI overrides. A nil pointer leaves the
// underlying value untouched; AllowedTypes overrides only when non-nil.
type Override struct {
	Enabled         *bool
	Direction       *Direction
	PlainText       *bool
	RichText        *bool
	Image           *bool
	FileTransfer    *bool
	MaxContentBytes *int64
	SessionMbps     *int
	BandwidthPct    *int
	AllowedTypes    []string
}

// apply layers the override's set fields onto p.
func (o Override) apply(p Policy) Policy {
	if o.Enabled != nil {
		p.Enabled = *o.Enabled
	}
	if o.Direction != nil {
		p.Direction = *o.Direction
	}
	if o.PlainText != nil {
		p.Formats.PlainText = *o.PlainText
	}
	if o.RichText != nil {
		p.Formats.RichText = *o.RichText
	}
	if o.Image != nil {
		p.Formats.Image = *o.Image
	}
	if o.FileTransfer != nil {
		p.FileTransfer = *o.FileTransfer
	}
	if o.MaxContentBytes != nil {
		p.MaxContentBytes = *o.MaxContentBytes
	}
	if o.SessionMbps != nil {
		p.SessionMbps = *o.SessionMbps
	}
	if o.BandwidthPct != nil {
		p.BandwidthPct = *o.BandwidthPct
	}
	if o.AllowedTypes != nil {
		p.AllowedTypes = o.AllowedTypes
	}
	return p
}

// Resolve computes the effective policy with precedence
// CLI override > per-VM config > settings default > Default(). A non-nil perVM
// or settingsDefault is authoritative as a whole (like a NIC topology), then
// the CLI override layers individual fields on top.
func Resolve(settingsDefault, perVM *Policy, cli Override) Policy {
	base := Default()
	if settingsDefault != nil {
		base = *settingsDefault
	}
	if perVM != nil {
		base = *perVM
	}
	return cli.apply(base)
}
