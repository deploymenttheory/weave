package proto

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payloads := [][]byte{[]byte("hello"), {}, bytes.Repeat([]byte{0xAB}, 70000)}
	for _, p := range payloads {
		if err := WriteFrame(&buf, p); err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
	}
	for i, want := range payloads {
		got, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("ReadFrame %d: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("frame %d: got %d bytes, want %d", i, len(got), len(want))
		}
	}
}

func TestRequestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	want := Request{Module: "clipboard", Op: "get", Meta: json.RawMessage(`{"allowed":["text/plain"]}`)}
	if err := WriteRequest(&buf, want); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}
	got, err := ReadRequest(&buf)
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}
	if got.Module != "clipboard" || got.Op != "get" || string(got.Meta) != `{"allowed":["text/plain"]}` {
		t.Errorf("request round-trip mismatch: %+v", got)
	}
}

func TestResponseRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteResponse(&buf, Response{Err: "boom"}); err != nil {
		t.Fatalf("WriteResponse: %v", err)
	}
	got, err := ReadResponse(&buf)
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}
	if got.Err != "boom" {
		t.Errorf("response round-trip mismatch: %+v", got)
	}
}
