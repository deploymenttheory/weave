// Package agent is the guest-side framework for the weave guest agent. It runs
// the request/response loop over the SSH stdio channel and routes each request
// to a registered feature module by name. Modules (clipboard today; more to
// come) own the meaning of their meta and data frames; the framework owns only
// routing and lifecycle.
package agent

import (
	"encoding/json"
	"io"
	"runtime"

	"github.com/deploymenttheory/weave/internal/guestagent/proto"
)

// Version is reported to the host on the hello handshake so it can redeploy the
// agent when the embedded build no longer matches the resident one. Bump it
// whenever the transport or any module's wire contract changes incompatibly.
const Version = "weave-guestd/1"

// Reserved framework-level module and op for the host handshake. Handled by
// Serve itself, not a registered module, so the host can probe a freshly
// deployed agent (and verify its version) before talking to any module.
const (
	ModuleName = "agent"
	OpHello    = "hello"
)

// Hello is the handshake response meta: the agent identifies its build and the
// guest platform it is running on.
type Hello struct {
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

// Module is one feature of the guest agent. Serve handles a single request:
// req.Op selects the operation, req.Meta carries its control payload, in
// supplies any request data frames, and the module writes its full response
// (a proto.Response envelope via proto.WriteResponse, then any data frames) to
// out. Returning an error aborts the connection; per-operation failures should
// instead be reported as a Response with Err set.
type Module interface {
	Name() string
	Serve(req proto.Request, in io.Reader, out io.Writer) error
}

// Registry holds the modules an agent serves, keyed by name.
type Registry struct {
	modules map[string]Module
}

// NewRegistry builds a registry from the given modules.
func NewRegistry(modules ...Module) *Registry {
	r := &Registry{modules: make(map[string]Module, len(modules))}
	for _, m := range modules {
		r.modules[m.Name()] = m
	}
	return r
}

// Serve runs the dispatch loop until the host closes the connection (io.EOF).
// Each request is routed to its module; an unknown module yields a Response with
// Err set so the host can degrade gracefully without dropping the channel.
func Serve(in io.Reader, out io.Writer, registry *Registry) error {
	for {
		req, err := proto.ReadRequest(in)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		if req.Module == ModuleName {
			if err := serveHello(out); err != nil {
				return err
			}
			continue
		}

		module, ok := registry.modules[req.Module]
		if !ok {
			if err := proto.WriteResponse(out, proto.Response{Err: "unknown module: " + req.Module}); err != nil {
				return err
			}
			continue
		}

		if err := module.Serve(req, in, out); err != nil {
			return err
		}
	}
}

func serveHello(out io.Writer) error {
	raw, err := json.Marshal(Hello{Version: Version, OS: runtime.GOOS, Arch: runtime.GOARCH})
	if err != nil {
		return err
	}
	return proto.WriteResponse(out, proto.Response{Meta: raw})
}
