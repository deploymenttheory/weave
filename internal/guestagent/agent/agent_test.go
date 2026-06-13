package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"github.com/deploymenttheory/weave/internal/guestagent/proto"
)

// echoModule echoes the request meta back in its response.
type echoModule struct{}

func (echoModule) Name() string { return "echo" }
func (echoModule) Serve(req proto.Request, _ io.Reader, out io.Writer) error {
	return proto.WriteResponse(out, proto.Response{Meta: req.Meta})
}

func TestServeHelloAndDispatch(t *testing.T) {
	var in bytes.Buffer
	// A hello handshake, an echo request, then an unknown-module request.
	mustWriteReq(t, &in, proto.Request{Module: ModuleName, Op: OpHello})
	mustWriteReq(t, &in, proto.Request{Module: "echo", Op: "x", Meta: json.RawMessage(`{"k":1}`)})
	mustWriteReq(t, &in, proto.Request{Module: "nope", Op: "x"})

	var out bytes.Buffer
	if err := Serve(&in, &out, NewRegistry(echoModule{})); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	// Hello response carries the version/os/arch.
	hello := mustReadResp(t, &out)
	if hello.Err != "" {
		t.Fatalf("hello err: %s", hello.Err)
	}
	var h Hello
	if err := json.Unmarshal(hello.Meta, &h); err != nil {
		t.Fatal(err)
	}
	if h.Version != Version || h.OS == "" || h.Arch == "" {
		t.Errorf("hello = %+v, want version %q + non-empty os/arch", h, Version)
	}

	// Echo response returns the request meta verbatim.
	echo := mustReadResp(t, &out)
	if string(echo.Meta) != `{"k":1}` {
		t.Errorf("echo meta = %s, want {\"k\":1}", echo.Meta)
	}

	// Unknown module yields an error response, not a dropped connection.
	unknown := mustReadResp(t, &out)
	if unknown.Err == "" {
		t.Error("expected error for unknown module")
	}
}

func mustWriteReq(t *testing.T, w io.Writer, req proto.Request) {
	t.Helper()
	if err := proto.WriteRequest(w, req); err != nil {
		t.Fatal(err)
	}
}

func mustReadResp(t *testing.T, r io.Reader) proto.Response {
	t.Helper()
	resp, err := proto.ReadResponse(r)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
