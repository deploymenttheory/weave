//go:build darwin

package macpb

import (
	"bytes"
	"os"
	"testing"

	"github.com/deploymenttheory/weave/internal/clipboard/wire"
)

// TestRoundTrip writes multiple representations to the real host pasteboard and
// reads them back, asserting full format fidelity. It clobbers the clipboard,
// so it runs only when WEAVE_PASTEBOARD_TEST=1. NSPasteboard is called directly
// (no main-thread dispatch): a test binary has no main run loop, and the simple
// read/write operations are safe off the main thread.
func TestRoundTrip(t *testing.T) {
	if os.Getenv("WEAVE_PASTEBOARD_TEST") == "" {
		t.Skip("set WEAVE_PASTEBOARD_TEST=1 to run (clobbers the host clipboard)")
	}

	allowed := map[wire.Canonical]bool{
		wire.CanonPlainText: true,
		wire.CanonRTF:       true,
		wire.CanonHTML:      true,
	}
	want := wire.Payload{Items: []wire.DataItem{
		{Format: wire.CanonRTF, Data: []byte(`{\rtf1\ansi\ansicpg1252 hello}`)},
		{Format: wire.CanonHTML, Data: []byte("<b>hello</b>")},
		{Format: wire.CanonPlainText, Data: []byte("hello")},
	}}

	if err := Write(want, t.TempDir()); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := Read(allowed, 0)
	byFormat := map[wire.Canonical][]byte{}
	for _, item := range got.Items {
		byFormat[item.Format] = item.Data
	}
	for _, item := range want.Items {
		data, ok := byFormat[item.Format]
		if !ok {
			t.Errorf("format %q missing after round-trip", item.Format)
			continue
		}
		if !bytes.Equal(data, item.Data) {
			t.Errorf("format %q: got %q, want %q", item.Format, data, item.Data)
		}
	}
}

// TestFileRoundTrip stages a file on the pasteboard and reads it back.
func TestFileRoundTrip(t *testing.T) {
	if os.Getenv("WEAVE_PASTEBOARD_TEST") == "" {
		t.Skip("set WEAVE_PASTEBOARD_TEST=1 to run (clobbers the host clipboard)")
	}

	want := wire.Payload{Files: []wire.DataFile{
		{Name: "hello.txt", Data: []byte("file contents")},
		{Name: "second.bin", Data: bytes.Repeat([]byte{0x07}, 2048)},
	}}
	if err := Write(want, t.TempDir()); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := Read(map[wire.Canonical]bool{wire.CanonFiles: true}, 0)
	if len(got.Files) != 2 {
		t.Fatalf("got %d files, want 2", len(got.Files))
	}
	if got.Files[0].Name != "hello.txt" || !bytes.Equal(got.Files[0].Data, []byte("file contents")) {
		t.Errorf("file 0 mismatch: %+v", got.Files[0])
	}
	if len(got.Files[1].Data) != 2048 {
		t.Errorf("file 1 size = %d, want 2048", len(got.Files[1].Data))
	}
}
