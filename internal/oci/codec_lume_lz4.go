// The lume lz4 legacy codec: lume's oldest encoding. Sequential
// application/octet-stream+lz4 disk parts (no offsets — strict layer order),
// config as an OCI-config-media-type layer, nvram as a bare octet-stream
// layer. Read-only and best-effort: kept simple, decompressing parts
// sequentially in memory the same way the tart DiskV2 path does.
//go:build darwin

package oci

import (
	"context"
	"os"

	"github.com/deploymenttheory/weave/internal/logging"
)

type lumeLZ4Codec struct{}

func (lumeLZ4Codec) Pull(ctx context.Context, source BlobSource, manifest OCIManifest,
	destination PullDestination, concurrency uint,
	localLayerCache *LocalLayerCache, deduplicate bool) (*VMDescription, error) {
	// Config layer → VM description.
	logging.DefaultLogger().AppendNewLine("pulling lume config...")
	config, err := decodeLumeConfigLayer(ctx, source, manifest)
	if err != nil {
		return nil, err
	}
	description, err := lumeVMDescriptionFromConfigJSON(config)
	if err != nil {
		return nil, err
	}

	// Disk parts: lz4 layers in manifest order.
	var diskLayers []OCIManifestLayer
	var compressedBytes int64
	for _, layer := range manifest.Layers {
		if base, _ := parseLayerMediaType(layer.MediaType); base == lumeLZ4LayerMediaType {
			diskLayers = append(diskLayers, layer)
			compressedBytes += int64(layer.Size)
		}
	}
	if len(diskLayers) == 0 {
		return nil, ErrOCIShouldBeAtLeastOneLayer
	}

	logging.DefaultLogger().AppendNewLine(describeDownloadSize(compressedBytes, len(diskLayers), "disk (lz4)"))
	progress := logging.NewDownloadProgress(compressedBytes)
	logging.NewProgressObserver(progress).Log(logging.DefaultLogger())

	if err := pullLZ4Disk(ctx, source, diskLayers, destination.DiskPath,
		description.DiskSizeBytes, progress); err != nil {
		return nil, err
	}

	// NVRAM.
	logging.DefaultLogger().AppendNewLine("pulling NVRAM...")
	nvramLayer, err := findSingleLumeNvramLayer(manifest)
	if err != nil {
		return nil, err
	}
	if err := pullBlobToPath(ctx, source, nvramLayer.Digest, destination.NvramPath); err != nil {
		return nil, err
	}

	return description, nil
}

// pullLZ4Disk fetches and decompresses parts sequentially (the format has no
// offsets, so order is the only placement information), writing each
// decompressed part at the running offset with zero-run skipping.
func pullLZ4Disk(ctx context.Context, source BlobSource, diskLayers []OCIManifestLayer,
	diskPath string, declaredDiskSize uint64, progress *logging.DownloadProgress) error {
	disk, err := os.Create(diskPath)
	if err != nil {
		return ErrOCIFailedToCreateVmFile
	}
	defer disk.Close()
	if declaredDiskSize > 0 {
		if err := disk.Truncate(int64(declaredDiskSize)); err != nil {
			return err
		}
	}

	var offset uint64
	for _, layer := range diskLayers {
		compressed := make([]byte, 0, layer.Size)
		if err := pullBlobVerified(ctx, source, layer.Digest, func(data []byte) error {
			compressed = append(compressed, data...)
			progress.Add(int64(len(data)))
			return nil
		}); err != nil {
			return err
		}

		decompressed, err := decompressLZ4(compressed)
		if err != nil {
			return err
		}

		writer := newSparseFileWriter(disk, offset)
		if _, err := writer.Write(decompressed); err != nil {
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}
		offset += uint64(len(decompressed))
	}

	// Grow the file if the parts exceeded the declared size.
	if offset > declaredDiskSize {
		if err := disk.Truncate(int64(offset)); err != nil {
			return err
		}
	}
	return disk.Close()
}
