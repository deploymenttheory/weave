// Package clipguest is the clipboard module of the weave guest agent. It runs
// inside the guest, registered into the agent framework, and reads/writes the
// guest clipboard with full format fidelity through a platform backend
// (NSPasteboard on macOS, xclip/wl-clipboard on Linux).
//
// The module applies no policy of its own: the host sends an explicit allow-list
// on every GET and only ever sends permitted formats on SET. Directionality and
// bandwidth throttling live in the host engine.
package clipguest

import (
	"encoding/json"
	"io"
	"runtime"
	"sync"

	"github.com/deploymenttheory/weave/internal/clipboard/wire"
	"github.com/deploymenttheory/weave/internal/guestagent/agent"
	"github.com/deploymenttheory/weave/internal/guestagent/proto"
)

// backend is the per-OS guest clipboard.
type backend interface {
	Stat() (uint64, error)
	Read(allowed map[wire.Canonical]bool) (wire.Payload, error)
	Write(p wire.Payload) error
}

// Module implements agent.Module for the clipboard. The backend is initialised
// lazily so a guest without a usable clipboard (e.g. Linux with no display
// server) still runs the agent and reports a per-operation error rather than
// failing to start.
type Module struct {
	once    sync.Once
	backend backend
	initErr error
}

// New returns the clipboard module.
func New() *Module { return &Module{} }

// Name identifies the module on the wire.
func (m *Module) Name() string { return wire.Module }

func (m *Module) ensure() (backend, error) {
	m.once.Do(func() { m.backend, m.initErr = newBackend() })
	return m.backend, m.initErr
}

// Serve handles one clipboard request.
func (m *Module) Serve(req proto.Request, in io.Reader, out io.Writer) error {
	b, err := m.ensure()
	if err != nil {
		return proto.WriteResponse(out, proto.Response{Err: err.Error()})
	}

	var meta wire.Meta
	if len(req.Meta) > 0 {
		if err := json.Unmarshal(req.Meta, &meta); err != nil {
			return proto.WriteResponse(out, proto.Response{Err: err.Error()})
		}
	}

	switch req.Op {
	case wire.OpStat:
		cc, serr := b.Stat()
		if serr != nil {
			return proto.WriteResponse(out, proto.Response{Err: serr.Error()})
		}
		return writeMeta(out, wire.Meta{ChangeCount: cc, AgentVersion: agent.Version, OS: runtime.GOOS}, nil)

	case wire.OpGet:
		payload, gerr := b.Read(canonicalSet(meta.Allowed))
		if gerr != nil {
			return proto.WriteResponse(out, proto.Response{Err: gerr.Error()})
		}
		return writeMeta(out, wire.MetaFor(payload), &payload)

	case wire.OpSet:
		payload, rerr := wire.ReadBody(in, meta, nil)
		if rerr != nil {
			return rerr
		}
		resp := proto.Response{}
		if werr := b.Write(payload); werr != nil {
			resp.Err = werr.Error()
		}
		return proto.WriteResponse(out, resp)

	default:
		return proto.WriteResponse(out, proto.Response{Err: "unknown clipboard op: " + req.Op})
	}
}

// writeMeta writes a successful response envelope carrying meta, then, when a
// payload is given, its data frames.
func writeMeta(out io.Writer, meta wire.Meta, payload *wire.Payload) error {
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if err := proto.WriteResponse(out, proto.Response{Meta: raw}); err != nil {
		return err
	}
	if payload != nil {
		return wire.WriteBody(out, *payload, nil)
	}
	return nil
}

func canonicalSet(list []wire.Canonical) map[wire.Canonical]bool {
	m := make(map[wire.Canonical]bool, len(list))
	for _, c := range list {
		m[c] = true
	}
	return m
}
