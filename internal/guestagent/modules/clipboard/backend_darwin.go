//go:build darwin

package clipguest

import (
	"fmt"
	"os"

	"github.com/deploymenttheory/weave/internal/clipboard/macpb"
	"github.com/deploymenttheory/weave/internal/clipboard/wire"
)

// darwinBackend drives the macOS guest pasteboard via the shared macpb package.
// The agent is a CLI with no running main run loop, so it calls NSPasteboard
// directly (the simple read/write/enumerate operations used here are safe off
// the main thread).
type darwinBackend struct {
	stageDir string
}

func newBackend() (backend, error) {
	dir, err := os.MkdirTemp("", "weave-clipboard-")
	if err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}
	return &darwinBackend{stageDir: dir}, nil
}

func (b *darwinBackend) Stat() (uint64, error) {
	return macpb.ChangeCount(), nil
}

func (b *darwinBackend) Read(allowed map[wire.Canonical]bool) (wire.Payload, error) {
	return macpb.Read(allowed, 0), nil
}

func (b *darwinBackend) Write(p wire.Payload) error {
	return macpb.Write(p, b.stageDir)
}
