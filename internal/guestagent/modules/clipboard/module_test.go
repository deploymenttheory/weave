package clipguest

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/deploymenttheory/weave/internal/clipboard/wire"
	"github.com/deploymenttheory/weave/internal/guestagent/proto"
)

// fakeBackend is an in-memory clipboard for exercising the module's wire
// contract without a real pasteboard.
type fakeBackend struct {
	changeCount uint64
	content     wire.Payload
	lastWritten wire.Payload
	allowed     map[wire.Canonical]bool
}

func (f *fakeBackend) Stat() (uint64, error) { return f.changeCount, nil }
func (f *fakeBackend) Read(allowed map[wire.Canonical]bool) (wire.Payload, error) {
	f.allowed = allowed
	return f.content, nil
}
func (f *fakeBackend) Write(p wire.Payload) error {
	f.lastWritten = p
	f.changeCount++
	return nil
}

// withFake returns a module wired to a fake backend, bypassing newBackend.
func withFake(b backend) *Module {
	m := New()
	m.backend = b
	m.once.Do(func() {}) // mark initialised so ensure() returns the fake
	return m
}

func TestModuleStat(t *testing.T) {
	m := withFake(&fakeBackend{changeCount: 42})
	resp := serve(t, m, proto.Request{Module: wire.Module, Op: wire.OpStat}, nil)
	var meta wire.Meta
	if err := json.Unmarshal(resp.Meta, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.ChangeCount != 42 || meta.AgentVersion == "" {
		t.Errorf("stat meta = %+v, want changeCount 42 + version", meta)
	}
}

func TestModuleGet(t *testing.T) {
	content := wire.Payload{
		Items: []wire.DataItem{{Format: wire.CanonHTML, Data: []byte("<b>hi</b>")}},
		Files: []wire.DataFile{{Name: "a.txt", Data: []byte("file")}},
	}
	fake := &fakeBackend{content: content}
	m := withFake(fake)

	reqMeta, _ := json.Marshal(wire.Meta{Allowed: []wire.Canonical{wire.CanonHTML, wire.CanonFiles}})
	var out bytes.Buffer
	if err := m.Serve(proto.Request{Module: wire.Module, Op: wire.OpGet, Meta: reqMeta}, nil, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	// The backend must have received the allow-list.
	if !fake.allowed[wire.CanonHTML] || !fake.allowed[wire.CanonFiles] {
		t.Errorf("backend allowed = %v", fake.allowed)
	}

	resp, err := proto.ReadResponse(&out)
	if err != nil || resp.Err != "" {
		t.Fatalf("response err: %v / %s", err, resp.Err)
	}
	var meta wire.Meta
	if err := json.Unmarshal(resp.Meta, &meta); err != nil {
		t.Fatal(err)
	}
	got, err := wire.ReadBody(&out, meta, nil)
	if err != nil {
		t.Fatalf("ReadBody: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].Format != wire.CanonHTML || !bytes.Equal(got.Items[0].Data, []byte("<b>hi</b>")) {
		t.Errorf("get items mismatch: %+v", got.Items)
	}
	if len(got.Files) != 1 || got.Files[0].Name != "a.txt" {
		t.Errorf("get files mismatch: %+v", got.Files)
	}
}

func TestModuleSet(t *testing.T) {
	fake := &fakeBackend{}
	m := withFake(fake)

	payload := wire.Payload{
		Items: []wire.DataItem{{Format: wire.CanonPlainText, Data: []byte("hello")}},
	}
	meta, _ := json.Marshal(wire.MetaFor(payload))

	// The request meta frame is read by Serve; the body frames follow on the
	// same stream.
	var in bytes.Buffer
	if err := wire.WriteBody(&in, payload, nil); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := m.Serve(proto.Request{Module: wire.Module, Op: wire.OpSet, Meta: meta}, &in, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	resp, err := proto.ReadResponse(&out)
	if err != nil || resp.Err != "" {
		t.Fatalf("set response err: %v / %s", err, resp.Err)
	}
	if len(fake.lastWritten.Items) != 1 || !bytes.Equal(fake.lastWritten.Items[0].Data, []byte("hello")) {
		t.Errorf("backend wrote %+v", fake.lastWritten)
	}
}

func serve(t *testing.T, m *Module, req proto.Request, in *bytes.Buffer) proto.Response {
	t.Helper()
	var out bytes.Buffer
	if in == nil {
		in = &bytes.Buffer{}
	}
	if err := m.Serve(req, in, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	resp, err := proto.ReadResponse(&out)
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}
	return resp
}
