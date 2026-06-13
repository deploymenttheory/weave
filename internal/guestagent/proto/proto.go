// Package proto is the OS-neutral transport for the weave guest agent. It
// carries no build tag and imports nothing platform-specific so the agent
// binary builds for both macOS and Linux guests.
//
// The agent multiplexes independent feature modules (clipboard today; file
// transfer, exec, telemetry and more later) over a single SSH stdio channel.
// Every exchange is a request envelope naming a module and an operation,
// followed by zero or more length-prefixed data frames the module defines, then
// a response envelope and its own data frames. proto owns the framing and the
// envelopes; modules own the meaning of their meta and data frames.
package proto

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// MaxFrameSize caps a single frame to guard against a corrupt or hostile length
// prefix. Modules enforce their own, smaller, per-item limits.
const MaxFrameSize = 512 << 20 // 512 MiB

// Request routes an operation to a module. Meta is the module-specific control
// payload (opaque to proto); any data frames follow the request envelope.
type Request struct {
	Module string          `json:"module"`
	Op     string          `json:"op"`
	Meta   json.RawMessage `json:"meta,omitempty"`
}

// Response is the module's reply. A non-empty Err means the operation failed;
// Meta and any following data frames are module-specific.
type Response struct {
	Err  string          `json:"err,omitempty"`
	Meta json.RawMessage `json:"meta,omitempty"`
}

// WriteFrame writes a length-prefixed frame (4-byte big-endian length + bytes),
// flushing the writer if it is buffered.
func WriteFrame(w io.Writer, b []byte) error {
	if len(b) > MaxFrameSize {
		return fmt.Errorf("guestagent: frame of %d bytes exceeds max %d", len(b), MaxFrameSize)
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(b)))
	if _, err := w.Write(prefix[:]); err != nil {
		return err
	}
	if len(b) > 0 {
		if _, err := w.Write(b); err != nil {
			return err
		}
	}
	if f, ok := w.(interface{ Flush() error }); ok {
		return f.Flush()
	}
	return nil
}

// ReadFrame reads a single frame written by WriteFrame.
func ReadFrame(r io.Reader) ([]byte, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(prefix[:])
	if n > MaxFrameSize {
		return nil, fmt.Errorf("guestagent: frame of %d bytes exceeds max %d", n, MaxFrameSize)
	}
	if n == 0 {
		return []byte{}, nil
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// WriteRequest marshals and frames a request envelope.
func WriteRequest(w io.Writer, req Request) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return WriteFrame(w, b)
}

// ReadRequest reads and unmarshals a request envelope.
func ReadRequest(r io.Reader) (Request, error) {
	b, err := ReadFrame(r)
	if err != nil {
		return Request{}, err
	}
	var req Request
	if err := json.Unmarshal(b, &req); err != nil {
		return Request{}, err
	}
	return req, nil
}

// WriteResponse marshals and frames a response envelope.
func WriteResponse(w io.Writer, resp Response) error {
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	return WriteFrame(w, b)
}

// ReadResponse reads and unmarshals a response envelope.
func ReadResponse(r io.Reader) (Response, error) {
	b, err := ReadFrame(r)
	if err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.Unmarshal(b, &resp); err != nil {
		return Response{}, err
	}
	return resp, nil
}

// NewBufferedReader wraps r so frame reads do fewer syscalls over an SSH pipe.
func NewBufferedReader(r io.Reader) *bufio.Reader { return bufio.NewReader(r) }

// NewBufferedWriter wraps w so a frame's prefix and body coalesce; WriteFrame
// flushes after each frame.
func NewBufferedWriter(w io.Writer) *bufio.Writer { return bufio.NewWriter(w) }
