// The tart codec: weave's native image encoding (ported from tart's
// VMDirectory+OCI.swift pull path). Config and nvram layers are verbatim
// blobs; the disk is LZ4-chunked disk.v2 layers reassembled by DiskV2.
//go:build darwin

package oci

import (
	"context"
	"fmt"
	"os"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/logging"
	"github.com/deploymenttheory/weave/internal/objcutil"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const legacyTartDiskV1MediaType = "application/vnd.cirruslabs.tart.disk.v1"

type tartCodec struct{}

func (tartCodec) Pull(ctx context.Context, source BlobSource, manifest OCIManifest,
	destination PullDestination, concurrency uint,
	localLayerCache *LocalLayerCache, deduplicate bool) (*VMDescription, error) {
	// Pull VM's config file layer and re-serialize it into a config file.
	var configLayers []OCIManifestLayer
	for _, layer := range manifest.Layers {
		if layer.MediaType == ConfigMediaType {
			configLayers = append(configLayers, layer)
		}
	}
	if len(configLayers) != 1 {
		return nil, ErrOCIShouldBeExactlyOneLayer
	}
	if err := pullBlobToPath(ctx, source, configLayers[0].Digest, destination.ConfigPath); err != nil {
		return nil, err
	}

	// Pull VM's disk layers and decompress them into a disk file.
	var diskLayers []OCIManifestLayer
	for _, layer := range manifest.Layers {
		if layer.MediaType == legacyTartDiskV1MediaType {
			return nil, weaveerrors.ErrGeneric("Pulling OCI images with legacy disk media type %s is no longer supported, please re-push the image using a current Tart version", legacyTartDiskV1MediaType)
		}
		if layer.MediaType == DiskV2MediaType {
			diskLayers = append(diskLayers, layer)
		}
	}
	if len(diskLayers) == 0 {
		return nil, ErrOCIShouldBeAtLeastOneLayer
	}

	var diskCompressedSize int64
	for _, layer := range diskLayers {
		diskCompressedSize += int64(layer.Size)
	}
	trace.SpanFromContext(ctx).SetAttributes(
		attribute.Int64("compressed_disk_size_bytes", diskCompressedSize))

	logging.DefaultLogger().AppendNewLine(fmt.Sprintf("pulling disk (%.1f GB compressed)...",
		float64(diskCompressedSize)/1_000_000_000.0))

	progress := logging.NewDownloadProgress(diskCompressedSize)
	logging.NewProgressObserver(progress).Log(logging.DefaultLogger())

	if err := (DiskV2{}).Pull(ctx, source, diskLayers, objcutil.NSURLFromPath(destination.DiskPath),
		concurrency, progress, localLayerCache, deduplicate); err != nil {
		return nil, err
	}

	// Pull VM's NVRAM file layer and store it in an NVRAM file.
	logging.DefaultLogger().AppendNewLine("pulling NVRAM...")

	var nvramLayers []OCIManifestLayer
	for _, layer := range manifest.Layers {
		if layer.MediaType == NvramMediaType {
			nvramLayers = append(nvramLayers, layer)
		}
	}
	if len(nvramLayers) != 1 {
		return nil, ErrOCIShouldBeExactlyOneLayer
	}
	return nil, pullBlobToPath(ctx, source, nvramLayers[0].Digest, destination.NvramPath)
}

// pullBlobToPath streams one blob into a freshly created file.
func pullBlobToPath(ctx context.Context, source BlobSource, digest string, path string) error {
	file, err := os.Create(path)
	if err != nil {
		return ErrOCIFailedToCreateVmFile
	}
	defer file.Close()

	if err := source.PullBlob(ctx, digest, 0, func(data []byte) error {
		_, err := file.Write(data)
		return err
	}); err != nil {
		return err
	}
	return file.Close()
}
