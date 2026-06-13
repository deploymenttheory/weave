// Package agentbin embeds the cross-compiled weave-guestd binaries so the host
// can deploy the right one into a guest without a separate artifact. The
// binaries are produced by guestagent/build.sh into the dist/ directory and
// named weave-guestd-<goos>-<goarch>. When a target's binary is absent (e.g. a
// plain `go build` without the build step), Binary reports false and the host
// engine falls back to legacy text-only clipboard sync.
package agentbin

import (
	"embed"
	"fmt"
)

//go:embed dist
var dist embed.FS

// Binary returns the embedded weave-guestd binary for the given GOOS/GOARCH.
func Binary(goos, goarch string) ([]byte, bool) {
	data, err := dist.ReadFile(fmt.Sprintf("dist/weave-guestd-%s-%s", goos, goarch))
	if err != nil {
		return nil, false
	}
	return data, true
}
