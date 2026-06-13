// The lume sharded codec: what ghcr.io/trycua actually publishes (validated
// live, see oci/testdata/lume-sharded-*.json). The disk is raw, uncompressed
// 500 MiB splits of disk.img carried as layers whose media type embeds the
// part order ("application/vnd.oci.image.layer.v1.tar;part.number=N;
// part.total=M"); some repos label parts "+lzfse" although the content is
// raw (lume quirk) — both spellings land here. Reassembly is ordered
// concatenation: each part's absolute offset is the sum of the preceding
// parts' manifest sizes, so parts download and write concurrently.
//go:build darwin

package oci

import (
	"context"
	"os"
	"sort"
	"sync"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/logging"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type lumeShardedCodec struct{}

// shardedDiskPart is one raw split with its computed absolute offset.
type shardedDiskPart struct {
	layer  OCIManifestLayer
	number int
	offset uint64
}

func (lumeShardedCodec) Pull(ctx context.Context, source BlobSource, manifest OCIManifest,
	destination PullDestination, concurrency uint,
	localLayerCache *LocalLayerCache, deduplicate bool) (*VMDescription, error) {
	// Config layer → VM description (the caller writes the weave config).
	logging.DefaultLogger().AppendNewLine("pulling lume config...")
	config, err := decodeLumeConfigLayer(ctx, source, manifest)
	if err != nil {
		return nil, err
	}
	description, err := lumeVMDescriptionFromConfigJSON(config)
	if err != nil {
		return nil, err
	}

	// Collect and order the disk parts.
	parts, totalPartBytes, err := collectShardedDiskParts(manifest)
	if err != nil {
		return nil, err
	}

	trace.SpanFromContext(ctx).SetAttributes(
		attribute.Int64("disk_size_bytes", int64(totalPartBytes)),
		attribute.Int("disk_part_count", len(parts)))

	// The disk file's logical size: the config's diskSize when sane,
	// otherwise the concatenation length (they match on observed images).
	diskSize := description.DiskSizeBytes
	if diskSize < totalPartBytes {
		diskSize = totalPartBytes
	}
	description.DiskSizeBytes = diskSize

	logging.DefaultLogger().AppendNewLine(describeDownloadSize(int64(totalPartBytes), len(parts), "disk"))
	progress := logging.NewDownloadProgress(int64(totalPartBytes))
	logging.NewProgressObserver(progress).Log(logging.DefaultLogger())

	if err := pullShardedDisk(ctx, source, parts, destination.DiskPath, diskSize, concurrency, progress); err != nil {
		return nil, err
	}

	// NVRAM layer.
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

// collectShardedDiskParts orders the part layers and computes each part's
// absolute disk offset from the preceding parts' sizes.
func collectShardedDiskParts(manifest OCIManifest) ([]shardedDiskPart, uint64, error) {
	var parts []shardedDiskPart
	var singleTarLayers []OCIManifestLayer

	for _, layer := range manifest.Layers {
		number, isPart, err := lumePartNumber(layer)
		if err != nil {
			return nil, 0, err
		}
		if isPart {
			parts = append(parts, shardedDiskPart{layer: layer, number: number})
			continue
		}
		if base, _ := parseLayerMediaType(layer.MediaType); base == lumeShardedLayerMediaType {
			singleTarLayers = append(singleTarLayers, layer)
		}
	}

	// Single-file variant: one unsharded .tar-labelled disk layer.
	if len(parts) == 0 {
		if len(singleTarLayers) != 1 {
			return nil, 0, weaveerrors.ErrPullFailed(
				"lume sharded image has no disk parts and %d whole-disk layers", len(singleTarLayers))
		}
		parts = append(parts, shardedDiskPart{layer: singleTarLayers[0], number: 1})
	}

	sort.Slice(parts, func(i, j int) bool { return parts[i].number < parts[j].number })

	var offset uint64
	for index := range parts {
		if parts[index].number != index+1 {
			return nil, 0, weaveerrors.ErrPullFailed(
				"lume sharded image disk parts are not contiguous: expected part %d, found part %d",
				index+1, parts[index].number)
		}
		parts[index].offset = offset
		offset += uint64(parts[index].layer.Size)
	}
	return parts, offset, nil
}

// pullShardedDisk downloads every part concurrently, writing each at its
// fixed offset with zero-run skipping so the file stays sparse.
func pullShardedDisk(ctx context.Context, source BlobSource, parts []shardedDiskPart,
	diskPath string, diskSize uint64, concurrency uint, progress *logging.DownloadProgress) error {
	disk, err := os.Create(diskPath)
	if err != nil {
		return ErrOCIFailedToCreateVmFile
	}
	defer disk.Close()
	if err := disk.Truncate(int64(diskSize)); err != nil {
		return err
	}

	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	semaphore := make(chan struct{}, max(int(concurrency), 1))

	setErr := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = err
		}
	}
	failed := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return firstErr != nil
	}

	for _, part := range parts {
		if failed() {
			break
		}
		semaphore <- struct{}{}
		wg.Add(1)
		go func(part shardedDiskPart) {
			defer wg.Done()
			defer func() { <-semaphore }()
			if failed() {
				return
			}

			writer := newSparseFileWriter(disk, part.offset)
			err := pullBlobVerified(ctx, source, part.layer.Digest, func(data []byte) error {
				if _, err := writer.Write(data); err != nil {
					return err
				}
				progress.Add(int64(len(data)))
				return nil
			})
			if err == nil {
				err = writer.Close()
			}
			if err != nil {
				setErr(err)
			}
		}(part)
	}
	wg.Wait()

	if firstErr != nil {
		return firstErr
	}
	return disk.Close()
}
