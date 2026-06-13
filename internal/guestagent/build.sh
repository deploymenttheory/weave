#!/usr/bin/env bash
# Cross-compiles the weave guest agent (weave-guestd) into agentbin/dist/ so the
# host can embed and deploy the right binary per guest OS/arch. Run from the
# repository root or anywhere; paths are resolved relative to this script.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
out="${here}/agentbin/dist"
pkg="github.com/deploymenttheory/weave/internal/guestagent/cmd/weave-guestd"

mkdir -p "${out}"

build() {
  local goos="$1" goarch="$2"
  echo "building weave-guestd-${goos}-${goarch}"
  CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" \
    go build -trimpath -ldflags="-s -w" \
    -o "${out}/weave-guestd-${goos}-${goarch}" "${pkg}"
}

build darwin arm64
build linux arm64
build linux amd64

echo "done -> ${out}"
