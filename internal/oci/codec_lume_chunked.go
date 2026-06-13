// The lume chunked-gzip codec: lume HEAD's "OCI-compliant" format (pushOCI/
// pullOCI in ImageContainerRegistry.swift). Not yet observed on published
// trycua images, but newly pushed cua images will use it. The disk is gzip
// chunks placed at explicit byte offsets (org.trycua.lume.part.offset), with
// VM metadata split between the manifest config descriptor and manifest
// annotations.
//go:build darwin

package oci

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"sort"
	"strconv"
	"sync"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/logging"
)

type lumeChunkedCodec struct{}

// chunkedDiskLayer is one gzip chunk with its placement metadata.
type chunkedDiskLayer struct {
	layer  OCIManifestLayer
	number int
	offset uint64
}

func (lumeChunkedCodec) Pull(ctx context.Context, source BlobSource, manifest OCIManifest,
	destination PullDestination, concurrency uint,
	localLayerCache *LocalLayerCache, deduplicate bool) (*VMDescription, error) {
	// VM description from the config descriptor + manifest annotations.
	description, err := lumeVMDescriptionFromDescriptor(ctx, source, manifest)
	if err != nil {
		return nil, err
	}

	// Disk chunks.
	chunks, err := collectChunkedDiskLayers(manifest)
	if err != nil {
		return nil, err
	}
	if len(chunks) == 0 {
		return nil, ErrOCIShouldBeAtLeastOneLayer
	}

	diskSize, err := chunkedDiskSize(manifest, chunks)
	if err != nil {
		return nil, err
	}
	if description.DiskSizeBytes < diskSize {
		description.DiskSizeBytes = diskSize
	}

	var compressedBytes int64
	for _, chunk := range chunks {
		compressedBytes += int64(chunk.layer.Size)
	}
	logging.DefaultLogger().AppendNewLine(describeDownloadSize(compressedBytes, len(chunks), "disk (compressed)"))
	progress := logging.NewDownloadProgress(compressedBytes)
	logging.NewProgressObserver(progress).Log(logging.DefaultLogger())

	if err := pullChunkedDisk(ctx, source, chunks, destination.DiskPath, description.DiskSizeBytes,
		concurrency, progress); err != nil {
		return nil, err
	}

	// NVRAM.
	logging.DefaultLogger().AppendNewLine("pulling NVRAM...")
	var nvramLayers []OCIManifestLayer
	for _, layer := range manifest.Layers {
		if layer.MediaType == LumeNvramMediaType {
			nvramLayers = append(nvramLayers, layer)
		}
	}
	if len(nvramLayers) != 1 {
		return nil, weaveerrors.ErrPullFailed("lume image has %d nvram layers, expected exactly one", len(nvramLayers))
	}
	if err := pullBlobToPath(ctx, source, nvramLayers[0].Digest, destination.NvramPath); err != nil {
		return nil, err
	}

	return description, nil
}

// lumeVMDescriptionFromDescriptor applies the chunked-format translation:
// identity from the config descriptor, sizing from manifest annotations.
func lumeVMDescriptionFromDescriptor(ctx context.Context, source BlobSource,
	manifest OCIManifest) (*VMDescription, error) {
	if manifest.Config.Digest == "" {
		return nil, weaveerrors.ErrPullFailed("lume image manifest has no config descriptor")
	}
	configLayer := OCIManifestLayer{
		Digest:    manifest.Config.Digest,
		MediaType: manifest.Config.MediaType,
		Size:      manifest.Config.Size,
	}
	data, err := pullBlobToMemory(ctx, source, configLayer)
	if err != nil {
		return nil, err
	}
	descriptor := &LumeOCIConfig{}
	if err := json.Unmarshal(data, descriptor); err != nil {
		return nil, weaveerrors.ErrPullFailed("failed to parse lume config descriptor: %v", err)
	}
	guestOS, err := lumeGuestOS(descriptor.OS)
	if err != nil {
		return nil, err
	}
	if len(descriptor.Storage) > 0 {
		logging.DefaultLogger().AppendNewLine("warning: lume image declares extra storage items; only the primary disk is pulled")
	}

	description := &VMDescription{
		OS:                  guestOS,
		ECIDBase64:          descriptor.MachineIdentifier,
		HardwareModelBase64: descriptor.HardwareModel,
		CPUCount:            lumeDefaultCPUCount,
		MemorySizeBytes:     lumeDefaultMemorySizeBytes,
		Display:             lumeChunkedDefaultDisplay,
	}
	annotations := manifest.Annotations
	if value, err := strconv.ParseUint(annotations[lumeAnnotationCPUCount], 10, 32); err == nil && value > 0 {
		description.CPUCount = uint(value)
	}
	if value, err := strconv.ParseUint(annotations[lumeAnnotationMemorySize], 10, 64); err == nil && value > 0 {
		description.MemorySizeBytes = value
	}
	if display := annotations[lumeAnnotationDisplay]; display != "" {
		description.Display = display
	}
	if value, err := strconv.ParseUint(annotations[lumeAnnotationDiskSize], 10, 64); err == nil {
		description.DiskSizeBytes = value
	}
	return description, nil
}

// collectChunkedDiskLayers orders the trycua disk layers. Chunked images
// carry part annotations; a single layer without them is the whole disk.
func collectChunkedDiskLayers(manifest OCIManifest) ([]chunkedDiskLayer, error) {
	var chunks []chunkedDiskLayer
	for _, layer := range manifest.Layers {
		if layer.MediaType != LumeDiskMediaType {
			continue
		}
		number, isPart, err := lumePartNumber(layer)
		if err != nil {
			return nil, err
		}
		chunk := chunkedDiskLayer{layer: layer, number: number}
		if isPart {
			offsetValue, ok := layer.Annotations[lumeAnnotationPartOffset]
			if !ok {
				return nil, weaveerrors.ErrPullFailed("lume disk chunk %s has no part offset", layer.Digest)
			}
			offset, err := strconv.ParseUint(offsetValue, 10, 64)
			if err != nil {
				return nil, weaveerrors.ErrPullFailed("lume disk chunk %s has malformed part offset %q", layer.Digest, offsetValue)
			}
			chunk.offset = offset
		} else if len(chunks) > 0 {
			return nil, weaveerrors.ErrPullFailed("lume image mixes annotated and unannotated disk chunks")
		}
		chunks = append(chunks, chunk)
	}
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].number < chunks[j].number })
	return chunks, nil
}

// chunkedDiskSize resolves the uncompressed disk size: the manifest total
// annotation, else the sum of per-chunk uncompressed sizes.
func chunkedDiskSize(manifest OCIManifest, chunks []chunkedDiskLayer) (uint64, error) {
	if value, err := strconv.ParseUint(manifest.Annotations[lumeAnnotationTotalDiskSize], 10, 64); err == nil && value > 0 {
		return value, nil
	}
	var total uint64
	for _, chunk := range chunks {
		value, err := strconv.ParseUint(chunk.layer.Annotations[lumeAnnotationUncompressedSize], 10, 64)
		if err != nil {
			return 0, weaveerrors.ErrPullFailed(
				"lume image declares no total disk size and chunk %s has no uncompressed size", chunk.layer.Digest)
		}
		total += value
	}
	return total, nil
}

// pullChunkedDisk downloads chunks concurrently, gunzip-streaming each into
// its offset with zero-run skipping.
func pullChunkedDisk(ctx context.Context, source BlobSource, chunks []chunkedDiskLayer,
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

	for _, chunk := range chunks {
		if failed() {
			break
		}
		semaphore <- struct{}{}
		wg.Add(1)
		go func(chunk chunkedDiskLayer) {
			defer wg.Done()
			defer func() { <-semaphore }()
			if failed() {
				return
			}
			if err := pullGzipChunk(ctx, source, chunk, disk, progress); err != nil {
				setErr(err)
			}
		}(chunk)
	}
	wg.Wait()

	if firstErr != nil {
		return firstErr
	}
	return disk.Close()
}

// pullGzipChunk streams one compressed chunk through gzip into the disk at
// the chunk's offset, verifying the compressed digest on the way.
func pullGzipChunk(ctx context.Context, source BlobSource, chunk chunkedDiskLayer,
	disk *os.File, progress *logging.DownloadProgress) error {
	pipeReader, pipeWriter := io.Pipe()

	done := make(chan error, 1)
	go func() {
		gzipReader, err := gzip.NewReader(pipeReader)
		if err != nil {
			_ = pipeReader.CloseWithError(err)
			done <- err
			return
		}
		writer := newSparseFileWriter(disk, chunk.offset)
		if _, err := io.Copy(writer, gzipReader); err == nil {
			err = writer.Close()
		}
		_ = pipeReader.CloseWithError(err)
		done <- err
	}()

	pullErr := pullBlobVerified(ctx, source, chunk.layer.Digest, func(data []byte) error {
		if _, err := pipeWriter.Write(data); err != nil {
			return err
		}
		progress.Add(int64(len(data)))
		return nil
	})
	_ = pipeWriter.CloseWithError(pullErr)

	decompressErr := <-done
	if pullErr != nil {
		return pullErr
	}
	return decompressErr
}
