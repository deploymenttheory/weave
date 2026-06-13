package agentbin_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/deploymenttheory/weave/internal/guestagent/agent"
	"github.com/deploymenttheory/weave/internal/guestagent/agentbin"
	"github.com/deploymenttheory/weave/internal/guestagent/proto"
)

// TestEmbeddedAgentRoundTrip writes the embedded weave-guestd binary for the
// current platform to a temp file, runs it, and drives the real wire protocol
// over its stdio — the same path the host uses after deploying it into a guest.
// It validates the binary boots, completes the hello handshake, and answers a
// clipboard STAT. Skips when no binary is embedded for this platform (e.g. a
// plain `go build` without guestagent/build.sh).
func TestEmbeddedAgentRoundTrip(t *testing.T) {
	binary, ok := agentbin.Binary(runtime.GOOS, runtime.GOARCH)
	if !ok {
		t.Skipf("no embedded agent for %s/%s (run guestagent/build.sh)", runtime.GOOS, runtime.GOARCH)
	}

	path := filepath.Join(t.TempDir(), "weave-guestd")
	if err := os.WriteFile(path, binary, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	r := proto.NewBufferedReader(stdout)
	w := proto.NewBufferedWriter(stdin)

	// Hello handshake.
	if err := proto.WriteRequest(w, proto.Request{Module: agent.ModuleName, Op: agent.OpHello}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var hello proto.Response
	go func() {
		hello, err = proto.ReadResponse(r)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for hello response")
	}
	if err != nil {
		t.Fatalf("hello: %v", err)
	}
	var identity agent.Hello
	if jerr := json.Unmarshal(hello.Meta, &identity); jerr != nil {
		t.Fatal(jerr)
	}
	if identity.Version != agent.Version {
		t.Errorf("agent version = %q, want %q", identity.Version, agent.Version)
	}
	if identity.OS != runtime.GOOS {
		t.Errorf("agent OS = %q, want %q", identity.OS, runtime.GOOS)
	}

	// Clipboard STAT: must return a response (a module-level error is fine on a
	// headless guest with no clipboard tool; a transport error is not).
	if err := proto.WriteRequest(w, proto.Request{Module: "clipboard", Op: "stat"}); err != nil {
		t.Fatal(err)
	}
	if _, err := proto.ReadResponse(r); err != nil {
		t.Fatalf("clipboard stat transport error: %v", err)
	}
}
