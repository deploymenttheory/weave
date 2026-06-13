// Port of tart's OCI/Layerizer/Disk.swift.
//go:build darwin

package oci

import (
	"context"

	"github.com/deploymenttheory/weave/internal/logging"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
)

// Disk ports tart's Disk protocol. The static protocol requirements become
// methods on a stateless implementation value (DiskV2).
type Disk interface {
	Push(ctx context.Context, diskURL *foundation.NSURL, registry *Registry, chunkSizeMb int, concurrency uint, progress *logging.DownloadProgress) ([]OCIManifestLayer, error)
	Pull(ctx context.Context, source BlobSource, diskLayers []OCIManifestLayer, diskURL *foundation.NSURL, concurrency uint, progress *logging.DownloadProgress, localLayerCache *LocalLayerCache, deduplicate bool) error
}
