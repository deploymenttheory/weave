// The transport axis of image distribution: where manifests and blobs live
// and how weave authenticates to them. Implementations are swappable per
// registry profile; the image *format* is the orthogonal axis, detected from
// each manifest by the oci package's codecs. See
// docs/registries-and-image-formats.md.
//go:build darwin

package registry

import (
	"context"

	"github.com/deploymenttheory/weave/internal/oci"
)

// ImageSource is the read side of a registry: everything a pull needs.
type ImageSource interface {
	PullManifest(ctx context.Context, reference string) (oci.OCIManifest, []byte, error)
	PullBlob(ctx context.Context, digest string, rangeStart int64, handler func([]byte) error) error
	Host() string
}

// ImageSink is the write side: everything a push needs.
type ImageSink interface {
	PushBlob(ctx context.Context, data []byte, chunkSizeMb int, digest string) (string, error)
	PushManifest(ctx context.Context, reference string, manifest oci.OCIManifest) (string, error)
}

// TagLister backs "weave images".
type TagLister interface {
	TagsList(ctx context.Context) ([]string, error)
}

// Client is a full registry connection bound to one repository.
type Client interface {
	ImageSource
	ImageSink
	TagLister
}

// The generic OCI-distribution client covers ghcr.io and private registries.
var _ Client = (*oci.Registry)(nil)

// NewOCIClient opens an OCI-distribution connection to host/namespace using
// the standard per-host credentials chain (keychain → docker config →
// environment → prompt).
func NewOCIClient(host string, namespace string, insecure bool) (Client, error) {
	return oci.NewRegistry(host, namespace, insecure, nil)
}
