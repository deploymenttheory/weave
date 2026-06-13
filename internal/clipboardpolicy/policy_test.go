package clipboardpolicy

import (
	"testing"

	"github.com/deploymenttheory/weave/internal/clipboard/wire"
)

func ptr[T any](v T) *T { return &v }

func TestDirectionGating(t *testing.T) {
	cases := []struct {
		dir     Direction
		enabled bool
		wantH2G bool
		wantG2H bool
	}{
		{DirectionBidirectional, true, true, true},
		{DirectionHostToGuest, true, true, false},
		{DirectionGuestToHost, true, false, true},
		{DirectionDisabled, true, false, false},
		{DirectionBidirectional, false, false, false}, // master switch off
	}
	for _, c := range cases {
		p := Policy{Enabled: c.enabled, Direction: c.dir}
		if got := p.AllowHostToGuest(); got != c.wantH2G {
			t.Errorf("dir=%s enabled=%v AllowHostToGuest=%v want %v", c.dir, c.enabled, got, c.wantH2G)
		}
		if got := p.AllowGuestToHost(); got != c.wantG2H {
			t.Errorf("dir=%s enabled=%v AllowGuestToHost=%v want %v", c.dir, c.enabled, got, c.wantG2H)
		}
	}
}

func TestActive(t *testing.T) {
	if !Default().Active() {
		t.Error("Default() should be Active")
	}
	if (Policy{Enabled: true, Direction: DirectionDisabled}).Active() {
		t.Error("disabled direction should not be Active")
	}
	if (Policy{Enabled: false, Direction: DirectionBidirectional}).Active() {
		t.Error("disabled master switch should not be Active")
	}
}

func TestBytesPerSec(t *testing.T) {
	cases := []struct {
		mbps, pct int
		want      int64
	}{
		{0, 50, 0},             // no declared budget → unlimited
		{100, 0, 0},            // no percent → unlimited
		{100, 20, 2_500_000},   // 100 Mbps * 20% = 20 Mbps = 2.5 MB/s
		{100, 200, 12_500_000}, // percent clamps to 100 → 12.5 MB/s
	}
	for _, c := range cases {
		p := Policy{SessionMbps: c.mbps, BandwidthPct: c.pct}
		if got := p.BytesPerSec(); got != c.want {
			t.Errorf("mbps=%d pct=%d BytesPerSec=%d want %d", c.mbps, c.pct, got, c.want)
		}
	}
}

func TestAllowedCanonicalCategories(t *testing.T) {
	p := Policy{
		Enabled:      true,
		Formats:      Formats{PlainText: true, RichText: true, Image: false},
		FileTransfer: false,
	}
	allowed := p.AllowedCanonical()
	if !allowed[wire.CanonPlainText] || !allowed[wire.CanonRTF] || !allowed[wire.CanonHTML] {
		t.Errorf("text categories should be allowed: %v", allowed)
	}
	if allowed[wire.CanonPNG] || allowed[wire.CanonTIFF] {
		t.Errorf("image disabled but present: %v", allowed)
	}
	if allowed[wire.CanonFiles] {
		t.Errorf("file transfer off but files allowed: %v", allowed)
	}
}

func TestAllowedCanonicalFileIndependence(t *testing.T) {
	// All format categories off but file transfer on: only files allowed.
	p := Policy{Enabled: true, FileTransfer: true}
	allowed := p.AllowedCanonical()
	if !allowed[wire.CanonFiles] {
		t.Error("file transfer on but files not allowed")
	}
	if allowed[wire.CanonPlainText] {
		t.Error("plain text off but allowed")
	}
}

func TestAllowedCanonicalExplicitList(t *testing.T) {
	// Explicit allow-list overrides category toggles (which are all true here).
	p := Policy{
		Enabled:      true,
		Formats:      Formats{PlainText: true, RichText: true, Image: true},
		AllowedTypes: []string{"text/html"},
		FileTransfer: false,
	}
	allowed := p.AllowedCanonical()
	if !allowed[wire.CanonHTML] {
		t.Error("explicitly allowed html missing")
	}
	if allowed[wire.CanonPlainText] || allowed[wire.CanonPNG] {
		t.Errorf("explicit list should exclude non-listed formats: %v", allowed)
	}
}

func TestResolvePrecedence(t *testing.T) {
	settings := &Policy{Enabled: true, Direction: DirectionBidirectional, FileTransfer: true}
	perVM := &Policy{Enabled: true, Direction: DirectionHostToGuest, FileTransfer: true}

	// perVM beats settings.
	got := Resolve(settings, perVM, Override{})
	if got.Direction != DirectionHostToGuest {
		t.Errorf("perVM should win: got %s", got.Direction)
	}

	// CLI beats perVM, per field.
	got = Resolve(settings, perVM, Override{Direction: ptr(DirectionGuestToHost), FileTransfer: ptr(false)})
	if got.Direction != DirectionGuestToHost {
		t.Errorf("CLI direction should win: got %s", got.Direction)
	}
	if got.FileTransfer {
		t.Error("CLI FileTransfer=false should win")
	}

	// Nothing set → Default().
	got = Resolve(nil, nil, Override{})
	if got.Direction != DirectionBidirectional || !got.FileTransfer {
		t.Errorf("expected defaults, got %+v", got)
	}
}
