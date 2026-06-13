// Port of tart's LocalLayerCache.swift: an mmap(2)-backed view of a cached
// VM disk, indexed by the layer digests recorded in its OCI manifest.
//go:build darwin

package oci

import (
	"os"
	"syscall"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	"github.com/deploymenttheory/weave/internal/objcutil"
)

// LocalLayerCacheDigestInfo ports LocalLayerCache.DigestInfo.
type LocalLayerCacheDigestInfo struct {
	RangeStart                uint64
	RangeEnd                  uint64
	CompressedDigest          string
	UncompressedContentDigest string
}

// LocalLayerCache ports tart's LocalLayerCache struct.
type LocalLayerCache struct {
	Name              string
	DeduplicatedBytes uint64
	DiskURL           *foundation.NSURL

	mappedDisk    []byte
	digestToRange map[string]LocalLayerCacheDigestInfo
	offsetToRange map[uint64]LocalLayerCacheDigestInfo
}

// NewLocalLayerCache ports LocalLayerCache.init?(_:_:_:_:); like the Swift
// failable initializer, it returns (nil, nil) when a disk layer is missing
// the uncompressed-size annotation.
func NewLocalLayerCache(name string, deduplicatedBytes uint64, diskURL *foundation.NSURL, manifest OCIManifest) (*LocalLayerCache, error) {
	cache := &LocalLayerCache{
		Name:              name,
		DeduplicatedBytes: deduplicatedBytes,
		DiskURL:           diskURL,
		digestToRange:     map[string]LocalLayerCacheDigestInfo{},
		offsetToRange:     map[uint64]LocalLayerCacheDigestInfo{},
	}

	// mmap(2) the disk that contains the layers from the manifest.
	file, err := os.Open(objcutil.GoStr(diskURL.Path()))
	if err != nil {
		return nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > 0 {
		mapped, err := syscall.Mmap(int(file.Fd()), 0, int(info.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
		if err != nil {
			return nil, err
		}
		cache.mappedDisk = mapped
	}

	// Record the ranges of the disk layers listed in the manifest.
	var offset uint64
	for _, layer := range manifest.Layers {
		if layer.MediaType != DiskV2MediaType {
			continue
		}

		uncompressedSize, ok := layer.UncompressedSize()
		if !ok {
			cache.Close()
			return nil, nil
		}
		uncompressedContentDigest, _ := layer.UncompressedContentDigest()

		digestInfo := LocalLayerCacheDigestInfo{
			RangeStart:                offset,
			RangeEnd:                  offset + uncompressedSize,
			CompressedDigest:          layer.Digest,
			UncompressedContentDigest: uncompressedContentDigest,
		}
		cache.digestToRange[layer.Digest] = digestInfo
		cache.offsetToRange[offset] = digestInfo

		offset += uncompressedSize
	}

	return cache, nil
}

// FindInfo ports LocalLayerCache.findInfo(digest:offsetHint:). Layers can
// share digests (e.g. empty ones), so the offset hint makes a better guess.
func (c *LocalLayerCache) FindInfo(digest string, offsetHint uint64) (LocalLayerCacheDigestInfo, bool) {
	if info, ok := c.offsetToRange[offsetHint]; ok && info.CompressedDigest == digest {
		return info, true
	}
	info, ok := c.digestToRange[digest]
	return info, ok
}

// Subdata ports LocalLayerCache.subdata(_:).
func (c *LocalLayerCache) Subdata(start uint64, end uint64) []byte {
	if end > uint64(len(c.mappedDisk)) {
		end = uint64(len(c.mappedDisk))
	}
	if start >= end {
		return nil
	}
	return append([]byte(nil), c.mappedDisk[start:end]...)
}

// Close unmaps the disk (Swift relies on Data's lifetime instead).
func (c *LocalLayerCache) Close() {
	if c.mappedDisk != nil {
		_ = syscall.Munmap(c.mappedDisk)
		c.mappedDisk = nil
	}
}
