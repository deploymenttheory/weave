// Wire-format constants and metadata schemas for cua/lume images, plus the
// shared streaming helpers their codecs use. Three encodings exist in the
// wild — see docs/registries-and-image-formats.md for the full
// reference and oci/testdata/ for live-captured fixtures. The registry is
// normative: the published images (sharded) diverge from lume HEAD's
// primary format (chunked-gzip).
//go:build darwin

package oci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"os"
	"strconv"
	"strings"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
)

// Media types of the lume chunked-gzip format (lume HEAD's pushOCI/pullOCI).
const (
	LumeConfigMediaType = "application/vnd.trycua.lume.config.v1+json"
	LumeDiskMediaType   = "application/vnd.trycua.lume.disk.v1"
	LumeNvramMediaType  = "application/vnd.trycua.lume.nvram.v1"
)

// Base media types of the lume sharded format (the published trycua images)
// and the lz4 legacy format.
const (
	lumeShardedLayerMediaType = "application/vnd.oci.image.layer.v1.tar"
	lumeLZ4LayerMediaType     = "application/octet-stream+lz4"
	octetStreamMediaType      = "application/octet-stream"
)

// Layer annotations of the chunked-gzip format.
const (
	lumeAnnotationPartNumber       = "org.trycua.lume.part.number"
	lumeAnnotationPartOffset       = "org.trycua.lume.part.offset"
	lumeAnnotationUncompressedSize = "org.trycua.lume.content.uncompressed-size"
)

// Manifest annotations of the chunked-gzip format.
const (
	lumeAnnotationCPUCount      = "org.trycua.lume.cpu-count"
	lumeAnnotationMemorySize    = "org.trycua.lume.memory-size"
	lumeAnnotationDisplay       = "org.trycua.lume.display"
	lumeAnnotationDiskSize      = "org.trycua.lume.disk-size"
	lumeAnnotationTotalDiskSize = "org.trycua.lume.total-uncompressed-size"
)

// imageTitleAnnotation names the original file of a layer in the sharded
// format ("config.json", "nvram.bin", "disk.img.part.aa", ...).
const imageTitleAnnotation = "org.opencontainers.image.title"

// Defaults applied when a lume image omits a value (see the translation table in docs/registries-and-image-formats.md §3.1).
const (
	lumeDefaultCPUCount        = 4
	lumeDefaultMemorySizeBytes = 4 * 1024 * 1024 * 1024
	lumeShardedDefaultDisplay  = "1024x768"
	lumeChunkedDefaultDisplay  = "1920x1080"
)

// LumeConfigJSON is the config-*layer* schema used by the sharded and lz4
// legacy formats (lume's FileSystem/VMConfig.swift CodingKeys). Validated
// against a live blob: oci/testdata/lume-sharded-vanilla-config.json.
type LumeConfigJSON struct {
	OS                string `json:"os"`
	CPUCount          uint   `json:"cpuCount"`
	MemorySize        uint64 `json:"memorySize"`
	DiskSize          uint64 `json:"diskSize"`
	MACAddress        string `json:"macAddress"`
	Display           string `json:"display"`
	HardwareModel     string `json:"hardwareModel"`     // base64 VZMacHardwareModel.dataRepresentation
	MachineIdentifier string `json:"machineIdentifier"` // base64 VZMacMachineIdentifier.dataRepresentation
}

// LumeOCIConfig is the config-*descriptor* schema of the chunked-gzip format
// (lume's LumeOCIConfig struct).
type LumeOCIConfig struct {
	MediaType         string `json:"mediatype,omitempty"`
	OS                string `json:"os"`
	HardwareModel     string `json:"hardwareModel"`
	MachineIdentifier string `json:"machineIdentifier"`
	Storage           []struct {
		MediaType string `json:"mediatype"`
		File      string `json:"file"`
	} `json:"storage,omitempty"`
}

// lumeGuestOS maps lume OS names onto weave/tart OS names.
func lumeGuestOS(osName string) (string, error) {
	switch strings.ToLower(osName) {
	case "macos", "darwin":
		return "darwin", nil
	case "linux":
		return "linux", nil
	default:
		return "", weaveerrors.ErrPullFailed("lume image declares unsupported guest OS %q", osName)
	}
}

// lumePartNumber extracts the 1-based part number of a sharded or chunked
// disk layer. Sharded images carry it as a media-type parameter, chunked
// ones as a layer annotation. Strict by design: a disk layer that looks like
// a part but has an unparsable number must fail the pull, never default to 0.
func lumePartNumber(layer OCIManifestLayer) (int, bool, error) {
	_, params := parseLayerMediaType(layer.MediaType)
	value, ok := params["part.number"]
	if !ok {
		value, ok = layer.Annotations[lumeAnnotationPartNumber]
	}
	if !ok {
		return 0, false, nil
	}
	number, err := strconv.Atoi(value)
	if err != nil || number < 1 {
		return 0, true, weaveerrors.ErrPullFailed("lume disk layer %s has malformed part number %q", layer.Digest, value)
	}
	return number, true, nil
}

// pullBlobVerified streams one blob through handler while checking its
// sha256 against the manifest digest. handler receives the raw chunks.
func pullBlobVerified(ctx context.Context, source BlobSource, digest string,
	handler func([]byte) error) error {
	var hasher hash.Hash
	expected, ok := strings.CutPrefix(digest, "sha256:")
	if ok {
		hasher = sha256.New()
	}
	if err := source.PullBlob(ctx, digest, 0, func(data []byte) error {
		if hasher != nil {
			_, _ = hasher.Write(data)
		}
		return handler(data)
	}); err != nil {
		return err
	}
	if hasher != nil {
		if actual := hex.EncodeToString(hasher.Sum(nil)); actual != expected {
			return weaveerrors.ErrPullFailed("digest mismatch for layer %s: got sha256:%s", digest, actual)
		}
	}
	return nil
}

// pullBlobToMemory fetches a small blob (config layers) entirely, verified.
func pullBlobToMemory(ctx context.Context, source BlobSource, layer OCIManifestLayer) ([]byte, error) {
	data := make([]byte, 0, layer.Size)
	if err := pullBlobVerified(ctx, source, layer.Digest, func(chunk []byte) error {
		data = append(data, chunk...)
		return nil
	}); err != nil {
		return nil, err
	}
	return data, nil
}

// sparseFileWriter writes a byte stream into a pre-truncated file starting
// at a fixed offset, skipping all-zero runs so filesystem holes survive.
// Buffering to the hole granularity keeps the zero check effective even when
// the source delivers small chunks.
type sparseFileWriter struct {
	file   *os.File
	offset uint64
	buffer []byte
}

func newSparseFileWriter(file *os.File, offset uint64) *sparseFileWriter {
	return &sparseFileWriter{file: file, offset: offset, buffer: make([]byte, 0, diskV2HoleGranularityBytes)}
}

func (w *sparseFileWriter) Write(data []byte) (int, error) {
	written := len(data)
	for len(data) > 0 {
		space := diskV2HoleGranularityBytes - len(w.buffer)
		take := min(space, len(data))
		w.buffer = append(w.buffer, data[:take]...)
		data = data[take:]
		if len(w.buffer) == diskV2HoleGranularityBytes {
			if err := w.flush(); err != nil {
				return 0, err
			}
		}
	}
	return written, nil
}

// Close flushes the trailing partial block.
func (w *sparseFileWriter) Close() error {
	return w.flush()
}

func (w *sparseFileWriter) flush() error {
	if len(w.buffer) == 0 {
		return nil
	}
	if !allZeroes(w.buffer) {
		if _, err := w.file.WriteAt(w.buffer, int64(w.offset)); err != nil {
			return err
		}
	}
	w.offset += uint64(len(w.buffer))
	w.buffer = w.buffer[:0]
	return nil
}

func allZeroes(data []byte) bool {
	for _, b := range data {
		if b != 0 {
			return false
		}
	}
	return true
}

// decodeLumeConfigLayer finds and parses the config layer of the sharded and
// lz4 legacy formats.
func decodeLumeConfigLayer(ctx context.Context, source BlobSource, manifest OCIManifest) (*LumeConfigJSON, error) {
	var configLayers []OCIManifestLayer
	for _, layer := range manifest.Layers {
		base, _ := parseLayerMediaType(layer.MediaType)
		if base == OCIConfigMediaType {
			configLayers = append(configLayers, layer)
		}
	}
	if len(configLayers) != 1 {
		return nil, weaveerrors.ErrPullFailed("lume image has %d config layers, expected exactly one", len(configLayers))
	}
	data, err := pullBlobToMemory(ctx, source, configLayers[0])
	if err != nil {
		return nil, err
	}
	config := &LumeConfigJSON{}
	if err := json.Unmarshal(data, config); err != nil {
		return nil, weaveerrors.ErrPullFailed("failed to parse lume config layer: %v", err)
	}
	return config, nil
}

// lumeVMDescriptionFromConfigJSON applies the translation table for formats
// whose metadata lives in the config layer.
func lumeVMDescriptionFromConfigJSON(config *LumeConfigJSON) (*VMDescription, error) {
	guestOS, err := lumeGuestOS(config.OS)
	if err != nil {
		return nil, err
	}
	description := &VMDescription{
		OS:                  guestOS,
		ECIDBase64:          config.MachineIdentifier,
		HardwareModelBase64: config.HardwareModel,
		CPUCount:            config.CPUCount,
		MemorySizeBytes:     config.MemorySize,
		MACAddress:          config.MACAddress,
		Display:             config.Display,
		DiskSizeBytes:       config.DiskSize,
	}
	if description.CPUCount == 0 {
		description.CPUCount = lumeDefaultCPUCount
	}
	if description.MemorySizeBytes == 0 {
		description.MemorySizeBytes = lumeDefaultMemorySizeBytes
	}
	if description.Display == "" {
		description.Display = lumeShardedDefaultDisplay
	}
	return description, nil
}

// findSingleLumeNvramLayer locates the nvram layer for the sharded and lz4
// formats: an octet-stream layer that is not a disk part, preferring an
// explicit "nvram.bin" title.
func findSingleLumeNvramLayer(manifest OCIManifest) (OCIManifestLayer, error) {
	var candidates []OCIManifestLayer
	for _, layer := range manifest.Layers {
		base, params := parseLayerMediaType(layer.MediaType)
		if base != octetStreamMediaType {
			continue
		}
		if _, isPart := params["part.number"]; isPart {
			continue
		}
		if layer.Annotations[imageTitleAnnotation] == "nvram.bin" {
			return layer, nil
		}
		candidates = append(candidates, layer)
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return OCIManifestLayer{}, weaveerrors.ErrPullFailed(
		"lume image has %d nvram layer candidates, expected exactly one", len(candidates))
}

// describeDownloadSize logs the total bytes about to be fetched — sharded
// images are raw splits, so this is the honest full size up front.
func describeDownloadSize(totalBytes int64, partCount int, what string) string {
	return fmt.Sprintf("pulling %s (%.1f GB in %d parts)...", what,
		float64(totalBytes)/1_000_000_000.0, partCount)
}
