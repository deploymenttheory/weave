// Image-format detection and the codec seam. A registry host can serve VM
// images in several layer encodings (tart, the lume variants); the format is
// a property of the individual image, detected from its manifest — never
// configured per registry. See docs/registries-and-image-formats.md.
//go:build darwin

package oci

import (
	"context"
	"fmt"
	"mime"
	"sort"
	"strings"
)

// ImageFormat identifies how a VM is encoded into manifest layers.
type ImageFormat int

const (
	ImageFormatUnknown ImageFormat = iota
	// ImageFormatTart is the cirruslabs/tart encoding weave itself pushes:
	// LZ4-chunked disk.v2 layers plus config and nvram layers.
	ImageFormatTart
	// ImageFormatLumeChunked is lume HEAD's "OCI-compliant" encoding:
	// trycua media types, gzip chunks placed at part.offset annotations.
	ImageFormatLumeChunked
	// ImageFormatLumeSharded is what ghcr.io/trycua actually publishes:
	// raw uncompressed disk.img splits with part info embedded in the layer
	// media-type parameters, plus config.json and nvram.bin layers.
	ImageFormatLumeSharded
	// ImageFormatLumeLZ4 is lume's oldest encoding: sequential
	// application/octet-stream+lz4 parts.
	ImageFormatLumeLZ4
)

func (f ImageFormat) String() string {
	switch f {
	case ImageFormatTart:
		return "tart"
	case ImageFormatLumeChunked:
		return "lume-chunked"
	case ImageFormatLumeSharded:
		return "lume-sharded"
	case ImageFormatLumeLZ4:
		return "lume-lz4"
	default:
		return "unknown"
	}
}

// BlobSource is the read-side transport a codec needs: it is satisfied by
// *Registry and, later, by any other registry backend.
type BlobSource interface {
	PullBlob(ctx context.Context, digest string, rangeStart int64, handler func([]byte) error) error
}

// PullDestination names the files a codec materialises. It decouples codecs
// from the vmdirectory package (which imports oci and therefore cannot be
// imported back).
type PullDestination struct {
	ConfigPath string
	DiskPath   string
	NvramPath  string
}

// VMDescription carries VM metadata for image formats that do not embed a
// weave-schema config.json (the lume formats). The caller translates it into
// a real config file; the base64 fields are VZ dataRepresentation payloads
// passed through unmodified. A nil VMDescription means the codec wrote
// destination.ConfigPath itself (the tart codec).
type VMDescription struct {
	OS                  string // "darwin" or "linux"
	ECIDBase64          string // VZMacMachineIdentifier.dataRepresentation
	HardwareModelBase64 string // VZMacHardwareModel.dataRepresentation
	CPUCount            uint
	MemorySizeBytes     uint64
	MACAddress          string // empty: caller generates a random one
	Display             string // "WxH"
	DiskSizeBytes       uint64
}

// Codec turns a manifest plus blob source into VM files on disk.
type Codec interface {
	Pull(ctx context.Context, source BlobSource, manifest OCIManifest,
		destination PullDestination, concurrency uint,
		localLayerCache *LocalLayerCache, deduplicate bool) (*VMDescription, error)
}

// parseLayerMediaType splits a layer media type into its base type and
// parameters, tolerating the non-RFC parameter syntax lume uses
// ("application/vnd.oci.image.layer.v1.tar;part.number=1;part.total=164").
func parseLayerMediaType(mediaType string) (base string, params map[string]string) {
	base, params, err := mime.ParseMediaType(mediaType)
	if err == nil {
		return base, params
	}
	// Fall back to manual splitting for malformed parameter lists.
	parts := strings.Split(mediaType, ";")
	params = map[string]string{}
	for _, part := range parts[1:] {
		if key, value, ok := strings.Cut(strings.TrimSpace(part), "="); ok {
			params[strings.ToLower(key)] = value
		}
	}
	return strings.TrimSpace(parts[0]), params
}

// DetectImageFormat inspects layer media types and the config descriptor.
// Precedence: tart > lume-chunked > lume-sharded > lume-lz4 > unknown.
func DetectImageFormat(manifest OCIManifest) ImageFormat {
	var tarLayerCount, configLayerCount int
	var hasShardedPart, hasLZ4 bool

	for _, layer := range manifest.Layers {
		base, params := parseLayerMediaType(layer.MediaType)

		if strings.HasPrefix(base, "application/vnd.cirruslabs.tart.") {
			return ImageFormatTart
		}
		if base == LumeDiskMediaType {
			return ImageFormatLumeChunked
		}
		if _, ok := params["part.number"]; ok {
			hasShardedPart = true
		}
		switch base {
		case "application/vnd.oci.image.layer.v1.tar":
			tarLayerCount++
		case "application/vnd.oci.image.config.v1+json":
			configLayerCount++
		case "application/octet-stream+lz4":
			hasLZ4 = true
		}
	}

	if manifest.Config.MediaType == LumeConfigMediaType {
		return ImageFormatLumeChunked
	}
	if hasShardedPart || (tarLayerCount == 1 && configLayerCount == 1) {
		return ImageFormatLumeSharded
	}
	if hasLZ4 {
		return ImageFormatLumeLZ4
	}
	return ImageFormatUnknown
}

// CodecFor returns the codec for a detected format, or a descriptive error
// listing the manifest's media types so unsupported variants are reportable.
func CodecFor(format ImageFormat, manifest OCIManifest) (Codec, error) {
	switch format {
	case ImageFormatTart:
		return tartCodec{}, nil
	case ImageFormatLumeSharded:
		return lumeShardedCodec{}, nil
	case ImageFormatLumeChunked:
		return lumeChunkedCodec{}, nil
	case ImageFormatLumeLZ4:
		return lumeLZ4Codec{}, nil
	default:
		seen := map[string]bool{}
		var mediaTypes []string
		for _, layer := range manifest.Layers {
			if !seen[layer.MediaType] {
				seen[layer.MediaType] = true
				mediaTypes = append(mediaTypes, layer.MediaType)
			}
		}
		sort.Strings(mediaTypes)
		return nil, fmt.Errorf(
			"unsupported VM image format: no codec recognises this manifest (config descriptor %q; layer media types: %s)",
			manifest.Config.MediaType, strings.Join(mediaTypes, ", "))
	}
}

// EstimatedUncompressedDiskSize returns the best advance estimate of the
// disk bytes a pull will write, for the pre-download disk-space check. The
// lz4 legacy format carries no uncompressed sizes, so its compressed sum is
// returned as a lower bound.
func EstimatedUncompressedDiskSize(manifest OCIManifest) (uint64, bool) {
	switch DetectImageFormat(manifest) {
	case ImageFormatTart:
		return manifest.UncompressedDiskSize()

	case ImageFormatLumeSharded:
		parts, totalPartBytes, err := collectShardedDiskParts(manifest)
		if err != nil || len(parts) == 0 {
			return 0, false
		}
		return totalPartBytes, true

	case ImageFormatLumeChunked:
		chunks, err := collectChunkedDiskLayers(manifest)
		if err != nil || len(chunks) == 0 {
			return 0, false
		}
		size, err := chunkedDiskSize(manifest, chunks)
		if err != nil {
			return 0, false
		}
		return size, true

	case ImageFormatLumeLZ4:
		var compressedBytes uint64
		for _, layer := range manifest.Layers {
			if base, _ := parseLayerMediaType(layer.MediaType); base == lumeLZ4LayerMediaType {
				compressedBytes += uint64(layer.Size)
			}
		}
		return compressedBytes, compressedBytes > 0

	default:
		return 0, false
	}
}
