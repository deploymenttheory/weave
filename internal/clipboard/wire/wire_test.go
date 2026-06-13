package wire

import (
	"bytes"
	"testing"
)

func TestClassOf(t *testing.T) {
	cases := map[Canonical]FormatClass{
		CanonPlainText: ClassPlainText,
		CanonRTF:       ClassRichText,
		CanonHTML:      ClassRichText,
		CanonPNG:       ClassImage,
		CanonTIFF:      ClassImage,
		CanonPDF:       ClassImage,
		CanonFiles:     ClassFile,
		Canonical("x"): ClassOther,
	}
	for canon, want := range cases {
		if got := ClassOf(canon); got != want {
			t.Errorf("ClassOf(%q) = %d, want %d", canon, got, want)
		}
	}
}

func TestUTIRoundTrip(t *testing.T) {
	for _, canon := range AllCanonical() {
		uti, ok := UTIForCanonical(canon)
		if !ok {
			t.Fatalf("no UTI for canonical %q", canon)
		}
		back, ok := CanonicalForUTI(uti)
		if !ok || back != canon {
			t.Errorf("CanonicalForUTI(%q) = %q,%v want %q", uti, back, ok, canon)
		}
	}
}

func TestRTFDDegradesToRTF(t *testing.T) {
	got, ok := CanonicalForUTI("com.apple.flat-rtfd")
	if !ok || got != CanonRTF {
		t.Errorf("flat-rtfd mapped to %q,%v want %q", got, ok, CanonRTF)
	}
}

func TestLinuxMIMERoundTrip(t *testing.T) {
	for _, canon := range AllCanonical() {
		mime, ok := LinuxMIMEForCanonical(canon)
		if !ok {
			t.Fatalf("no linux MIME for %q", canon)
		}
		back, ok := CanonicalForLinuxMIME(mime)
		if !ok || back != canon {
			t.Errorf("CanonicalForLinuxMIME(%q) = %q,%v want %q", mime, back, ok, canon)
		}
	}
}

func TestPayloadBodyRoundTrip(t *testing.T) {
	want := Payload{
		Items: []DataItem{
			{Format: CanonRTF, Data: []byte("{\\rtf1 hi}")},
			{Format: CanonPlainText, Data: []byte("hi")},
		},
		Files: []DataFile{
			{Name: "a.txt", Data: bytes.Repeat([]byte{0x01}, 1000)},
			{Name: "b.bin", Data: []byte{}},
		},
	}
	meta := MetaFor(want)

	var buf bytes.Buffer
	if err := WriteBody(&buf, want, nil); err != nil {
		t.Fatalf("WriteBody: %v", err)
	}
	got, err := ReadBody(&buf, meta, nil)
	if err != nil {
		t.Fatalf("ReadBody: %v", err)
	}
	if len(got.Items) != 2 || got.Items[0].Format != CanonRTF || !bytes.Equal(got.Items[1].Data, []byte("hi")) {
		t.Errorf("items round-trip mismatch: %+v", got.Items)
	}
	if len(got.Files) != 2 || got.Files[0].Name != "a.txt" || len(got.Files[0].Data) != 1000 {
		t.Errorf("files round-trip mismatch: %+v", got.Files)
	}
}

func TestPayloadBodyGate(t *testing.T) {
	// The gate must be called once per data frame (items + files) with the
	// right sizes, in order.
	p := Payload{
		Items: []DataItem{{Format: CanonPlainText, Data: []byte("abc")}},
		Files: []DataFile{{Name: "f", Data: []byte("de")}},
	}
	var sizes []int
	gate := func(n int) error { sizes = append(sizes, n); return nil }

	var buf bytes.Buffer
	if err := WriteBody(&buf, p, gate); err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 2 || sizes[0] != 3 || sizes[1] != 2 {
		t.Errorf("gate sizes = %v, want [3 2]", sizes)
	}
}
