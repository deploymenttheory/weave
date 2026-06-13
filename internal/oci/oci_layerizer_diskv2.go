// Port of tart's OCI/Layerizer/DiskV2.swift: splits a VM disk into LZ4
// layers on push and reassembles them on pull, skipping zero blocks and
// punching holes to keep disks sparse.
//
// LZ4 (de)compression goes through NSData's compressedData/decompressedData,
// which uses the same Apple Compression framing as the Swift original's
// NSData.compressed(using: .lz4) and OutputFilter(.lz4) — so layers remain
// byte-compatible with tart-pushed images. Unlike the Swift OutputFilter,
// decompression here is per-layer rather than streaming, costing up to one
// uncompressed layer (512 MiB) of memory per concurrent pull.
//go:build darwin

package oci

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"syscall"
	"unsafe"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/logging"
	"github.com/deploymenttheory/weave/internal/objcutil"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
)

const (
	diskV2LayerLimitBytes      = 512 * 1024 * 1024
	diskV2HoleGranularityBytes = 4 * 1024 * 1024
)

// diskV2ZeroChunk is a zero chunk for faster than byte-by-byte comparisons.
var diskV2ZeroChunk = make([]byte, diskV2HoleGranularityBytes)

// fpunchholeT mirrors fpunchhole_t from <sys/fcntl.h>.
type fpunchholeT struct {
	Flags    uint32
	Reserved uint32
	Offset   int64
	Length   int64
}

// fPunchhole is F_PUNCHHOLE from <sys/fcntl.h>.
const fPunchhole = 99

// DiskV2 ports tart's DiskV2 class.
type DiskV2 struct{}

var _ Disk = DiskV2{}

// compressLZ4 wraps NSData compressedDataUsingAlgorithm:.
func compressLZ4(data []byte) ([]byte, error) {
	compressed, err := objcutil.BytesToNSData(data).CompressedDataUsingAlgorithmError(foundation.NSDataCompressionAlgorithmLZ4)
	if err != nil {
		return nil, err
	}
	return objcutil.NSDataToBytes(compressed), nil
}

// decompressLZ4 wraps NSData decompressedDataUsingAlgorithm:.
func decompressLZ4(data []byte) ([]byte, error) {
	decompressed, err := objcutil.BytesToNSData(data).DecompressedDataUsingAlgorithmError(foundation.NSDataCompressionAlgorithmLZ4)
	if err != nil {
		return nil, err
	}
	return objcutil.NSDataToBytes(decompressed), nil
}

// Push ports DiskV2.push(diskURL:registry:chunkSizeMb:concurrency:progress:).
func (DiskV2) Push(ctx context.Context, diskURL *foundation.NSURL, registry *Registry,
	chunkSizeMb int, concurrency uint, progress *logging.DownloadProgress) ([]OCIManifestLayer, error) {
	// Open and map the disk file.
	file, err := os.Open(objcutil.GoStr(diskURL.Path()))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}

	var mappedDisk []byte
	if info.Size() > 0 {
		mappedDisk, err = syscall.Mmap(int(file.Fd()), 0, int(info.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
		if err != nil {
			return nil, err
		}
		defer func() { _ = syscall.Munmap(mappedDisk) }()
	}

	// Compress the disk file as multiple individually decompressible
	// streams, each diskV2LayerLimitBytes bytes or less.
	type indexedLayer struct {
		index int
		layer OCIManifestLayer
	}

	chunkCount := (len(mappedDisk) + diskV2LayerLimitBytes - 1) / diskV2LayerLimitBytes
	layers := make([]indexedLayer, 0, chunkCount)

	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	semaphore := make(chan struct{}, max(int(concurrency), 1))

	for index := 0; index < chunkCount; index++ {
		start := index * diskV2LayerLimitBytes
		end := min(start+diskV2LayerLimitBytes, len(mappedDisk))
		data := mappedDisk[start:end]

		semaphore <- struct{}{}
		wg.Go(func() {
			defer func() { <-semaphore }()

			pushedLayer, err := pushDiskLayer(ctx, data, registry, chunkSizeMb, progress)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			layers = append(layers, indexedLayer{index: index, layer: pushedLayer})
		})
	}
	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}

	sort.Slice(layers, func(i, j int) bool { return layers[i].index < layers[j].index })
	result := make([]OCIManifestLayer, 0, len(layers))
	for _, l := range layers {
		result = append(result, l.layer)
	}
	return result, nil
}

func pushDiskLayer(ctx context.Context, data []byte, registry *Registry,
	chunkSizeMb int, progress *logging.DownloadProgress) (OCIManifestLayer, error) {
	compressedData, err := compressLZ4(data)
	if err != nil {
		return OCIManifestLayer{}, err
	}
	compressedDataDigest := DigestHash(compressedData)

	err = objcutil.RetryOnURLError(5, func() error {
		exists, err := registry.BlobExists(ctx, compressedDataDigest)
		if err != nil {
			return err
		}
		if !exists {
			_, err = registry.PushBlob(ctx, compressedData, chunkSizeMb, compressedDataDigest)
		}
		return err
	})
	if err != nil {
		return OCIManifestLayer{}, err
	}

	// Update progress using a relative value.
	progress.Add(int64(len(data)))

	return NewOCIManifestLayer(DiskV2MediaType, len(compressedData), compressedDataDigest,
		uint64(len(data)), DigestHash(data)), nil
}

// Pull ports DiskV2.pull(registry:diskLayers:diskURL:concurrency:progress:
// localLayerCache:deduplicate:).
func (DiskV2) Pull(ctx context.Context, source BlobSource, diskLayers []OCIManifestLayer,
	diskURL *foundation.NSURL, concurrency uint, progress *logging.DownloadProgress,
	localLayerCache *LocalLayerCache, deduplicate bool) error {
	diskPath := objcutil.GoStr(diskURL.Path())

	// Support resumable pulls.
	pullResumed := foundation.NSFileManagerDefaultManager().FileExistsAtPath(diskURL.Path())

	if !pullResumed {
		if deduplicate && localLayerCache != nil {
			// Clone the local layer cache's disk and use it as a base,
			// potentially reducing the space usage since some blocks won't
			// be written at all.
			if _, err := foundation.NSFileManagerDefaultManager().
				CopyItemAtURLToURLError(localLayerCache.DiskURL, diskURL); err != nil {
				return err
			}
		} else {
			// Otherwise create an empty disk.
			if !foundation.NSFileManagerDefaultManager().
				CreateFileAtPathContentsAttributes(diskURL.Path(), objcutil.BytesToNSData(nil), nil) {
				return ErrOCIFailedToCreateVmFile
			}
		}
	}

	// Calculate the uncompressed disk size.
	var uncompressedDiskSize uint64
	for _, layer := range diskLayers {
		uncompressedLayerSize, ok := layer.UncompressedSize()
		if !ok {
			return errOCILayerMissingUncompressedSize
		}
		uncompressedDiskSize += uncompressedLayerSize
	}

	// Truncate the target disk file so that it will be able to accommodate
	// the uncompressed disk size.
	if err := os.Truncate(diskPath, int64(uncompressedDiskSize)); err != nil {
		return err
	}

	// Determine the file system block size.
	var st syscall.Stat_t
	if err := syscall.Stat(diskPath, &st); err != nil {
		return weaveerrors.ErrPullFailed("failed to stat(2) disk %s: %v", diskPath, err)
	}
	fsBlockSize := uint64(st.Blksize)

	// Concurrently fetch and decompress layers.
	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	semaphore := make(chan struct{}, max(int(concurrency), 1))

	var globalDiskWritingOffset uint64
	for index, diskLayer := range diskLayers {
		// Retrieve layer annotations.
		uncompressedLayerSize, ok := diskLayer.UncompressedSize()
		if !ok {
			return errOCILayerMissingUncompressedSize
		}
		uncompressedLayerContentDigest, ok := diskLayer.UncompressedContentDigest()
		if !ok {
			return errOCILayerMissingUncompressedDigest
		}

		// Capture the current disk writing offset.
		diskWritingOffset := globalDiskWritingOffset

		semaphore <- struct{}{}
		wg.Go(func() {
			defer func() { <-semaphore }()

			err := pullDiskLayer(ctx, source, diskLayer, diskPath, fsBlockSize, pullResumed,
				diskWritingOffset, uncompressedLayerSize, uncompressedLayerContentDigest,
				index, progress, localLayerCache, deduplicate, diskURL)

			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
		})

		globalDiskWritingOffset += uncompressedLayerSize
	}
	wg.Wait()

	return firstErr
}

func pullDiskLayer(ctx context.Context, source BlobSource, diskLayer OCIManifestLayer,
	diskPath string, fsBlockSize uint64, pullResumed bool,
	diskWritingOffset uint64, uncompressedLayerSize uint64, uncompressedLayerContentDigest string,
	index int, progress *logging.DownloadProgress, localLayerCache *LocalLayerCache, deduplicate bool,
	diskURL *foundation.NSURL) error {
	// No need to fetch and decompress anything if we've already done so.
	if pullResumed {
		hash, err := DigestHashURLChunk(diskURL, diskWritingOffset, uncompressedLayerSize)
		if err == nil && hash == uncompressedLayerContentDigest {
			progress.Add(int64(diskLayer.Size))
			return nil
		}
	}

	// Open the disk file for writing.
	disk, err := os.OpenFile(diskPath, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer disk.Close()

	// Also open the disk file for reading and verifying its contents in
	// case the local layer cache is used.
	var rdisk *os.File
	if deduplicate && localLayerCache != nil {
		rdisk, err = os.Open(diskPath)
		if err != nil {
			return err
		}
		defer rdisk.Close()
	}

	// Check if we already have this layer's contents in the local layer
	// cache, or perhaps even on the cloned disk (when deduplication is on).
	if localLayerCache != nil {
		if info, ok := localLayerCache.FindInfo(diskLayer.Digest, diskWritingOffset); ok &&
			info.UncompressedContentDigest == uncompressedLayerContentDigest {
			if deduplicate && info.RangeStart == diskWritingOffset {
				// Do nothing: the data is already on the inherited disk.
			} else {
				// Fulfil the layer contents from the local blob cache.
				data := localLayerCache.Subdata(info.RangeStart, info.RangeEnd)
				if _, err := zeroSkippingWrite(disk, rdisk, fsBlockSize, diskWritingOffset, data); err != nil {
					return err
				}
			}

			progress.Add(int64(diskLayer.Size))
			return nil
		}
	}

	// Pull the compressed layer (resuming from the last received byte on
	// network errors), then decompress it into its disk offset.
	var compressed []byte
	err = objcutil.RetryOnURLError(5, func() error {
		return source.PullBlob(ctx, diskLayer.Digest, int64(len(compressed)), func(data []byte) error {
			compressed = append(compressed, data...)
			progress.Add(int64(len(data)))
			return nil
		})
	})
	if err != nil {
		return weaveerrors.ErrPullFailed("error pulling disk layer %d: %v", index+1, err)
	}

	uncompressed, err := decompressLZ4(compressed)
	if err != nil {
		return weaveerrors.ErrPullFailed("failed to decompress disk: %v", err)
	}

	_, err = zeroSkippingWrite(disk, rdisk, fsBlockSize, diskWritingOffset, uncompressed)
	return err
}

// zeroSkippingWrite ports DiskV2.zeroSkippingWrite(_:_:_:_:_:).
func zeroSkippingWrite(disk *os.File, rdisk *os.File, fsBlockSize uint64, offset uint64, data []byte) (uint64, error) {
	for start := 0; start < len(data); start += diskV2HoleGranularityBytes {
		chunk := data[start:min(start+diskV2HoleGranularityBytes, len(data))]

		// If the local layer cache is used, only write chunks that differ,
		// since the base disk can contain anything at any position.
		if rdisk != nil {
			// F_PUNCHHOLE requires the holes to be aligned to file system
			// block boundaries.
			isHoleAligned := offset%fsBlockSize == 0 && uint64(len(chunk))%fsBlockSize == 0

			if isHoleAligned && bytes.Equal(chunk, diskV2ZeroChunk) {
				arg := fpunchholeT{Offset: int64(offset), Length: int64(len(chunk))}
				if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, disk.Fd(), fPunchhole,
					uintptr(unsafe.Pointer(&arg))); errno != 0 {
					return 0, weaveerrors.ErrPullFailed("failed to punch hole: %v", errno)
				}
			} else {
				actualContentsOnDisk := make([]byte, len(chunk))
				if _, err := rdisk.ReadAt(actualContentsOnDisk, int64(offset)); err != nil {
					return 0, err
				}
				if !bytes.Equal(chunk, actualContentsOnDisk) {
					if _, err := disk.WriteAt(chunk, int64(offset)); err != nil {
						return 0, err
					}
				}
			}

			offset += uint64(len(chunk))
			continue
		}

		// Otherwise, only write chunks that are not zero, since the base
		// disk is created from scratch and is zeroed via truncate(2).
		if !bytes.Equal(chunk, diskV2ZeroChunk) {
			if _, err := disk.WriteAt(chunk, int64(offset)); err != nil {
				return 0, err
			}
		}

		offset += uint64(len(chunk))
	}

	return offset, nil
}

var (
	ErrOCIShouldBeExactlyOneLayer        = fmt.Errorf("OCI manifest should contain exactly one layer of this type")
	ErrOCIShouldBeAtLeastOneLayer        = fmt.Errorf("OCI manifest should contain at least one layer of this type")
	ErrOCIFailedToCreateVmFile           = fmt.Errorf("failed to create VM file")
	errOCILayerMissingUncompressedSize   = fmt.Errorf("layer is missing uncompressed size annotation")
	errOCILayerMissingUncompressedDigest = fmt.Errorf("layer is missing uncompressed digest annotation")
)
