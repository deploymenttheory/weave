// Port of tart's VMStorageOCI.swift: the ~/.weave/cache/OCIs content store,
// where tag references are symlinks onto digest-named VM directories.
// Directory enumeration uses filepath.WalkDir (the NSFileManager enumerator
// binding requires an error-handler block).
//go:build darwin

package vmstorage

import (
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	weaveregistry "github.com/deploymenttheory/weave/internal/registry"

	weaveconfig "github.com/deploymenttheory/weave/internal/config"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	weavelock "github.com/deploymenttheory/weave/internal/lock"
	"github.com/deploymenttheory/weave/internal/logging"
	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/oci"
	"github.com/deploymenttheory/weave/internal/prune"
	"github.com/deploymenttheory/weave/internal/telemetry"
	"github.com/deploymenttheory/weave/internal/vmdirectory"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// VMStorageOCI ports tart's VMStorageOCI class.
type VMStorageOCI struct {
	BaseURL *foundation.NSURL
}

var _ prune.PrunableStorage = (*VMStorageOCI)(nil)

// NewVMStorageOCI ports VMStorageOCI.init().
func NewVMStorageOCI() (*VMStorageOCI, error) {
	config, err := weaveconfig.NewConfig()
	if err != nil {
		return nil, err
	}
	return &VMStorageOCI{
		BaseURL: config.WeaveCacheDir.URLByAppendingPathComponentIsDirectory(objcutil.NSStr("OCIs"), true),
	}, nil
}

// percentEncodeHost works around Swift URL's behaviour with ":" in path
// components; the on-disk layout percent-encodes the host's colon.
func percentEncodeHost(s string) string {
	return strings.ReplaceAll(s, ":", "%3A")
}

func percentDecodeName(s string) string {
	decoded, err := url.PathUnescape(s)
	if err != nil {
		return s
	}
	return decoded
}

func (s *VMStorageOCI) basePath() string {
	return objcutil.GoStr(s.BaseURL.Path())
}

// vmPath ports URL.appendingRemoteName(_:).
func (s *VMStorageOCI) vmPath(name oci.RemoteName) string {
	return filepath.Join(s.basePath(), percentEncodeHost(name.Host), filepath.FromSlash(name.Namespace), name.Reference.Value)
}

func (s *VMStorageOCI) vmURL(name oci.RemoteName) *foundation.NSURL {
	return objcutil.NSURLFromPath(s.vmPath(name))
}

// hostDirectoryPath ports URL.appendingHost(_:).
func (s *VMStorageOCI) hostDirectoryPath(name oci.RemoteName) string {
	return filepath.Join(s.basePath(), percentEncodeHost(name.Host))
}

// Exists ports VMStorageOCI.exists(_:).
func (s *VMStorageOCI) Exists(name oci.RemoteName) bool {
	return vmdirectory.NewVMDirectory(s.vmURL(name)).Initialized()
}

// Digest ports VMStorageOCI.digest(_:).
func (s *VMStorageOCI) Digest(name oci.RemoteName) (string, error) {
	resolved, err := filepath.EvalSymlinks(s.vmPath(name))
	if err != nil {
		resolved = s.vmPath(name)
	}
	digest := filepath.Base(resolved)

	if !strings.HasPrefix(digest, "sha256:") {
		return "", weaveerrors.ErrOCIStorageError(fmt.Sprintf("%s is not a digest and doesn't point to a digest", name))
	}
	return digest, nil
}

// Open ports VMStorageOCI.open(_:_:).
func (s *VMStorageOCI) Open(name oci.RemoteName, accessDate time.Time) (*vmdirectory.VMDirectory, error) {
	vmDir := vmdirectory.NewVMDirectory(s.vmURL(name))

	if err := vmDir.Validate(name.String()); err != nil {
		return nil, err
	}

	if err := prune.URLUpdateAccessDate(vmDir.BaseURL, accessDate); err != nil {
		return nil, err
	}

	return vmDir, nil
}

// Create ports VMStorageOCI.create(_:overwrite:).
func (s *VMStorageOCI) Create(name oci.RemoteName, overwrite bool) (*vmdirectory.VMDirectory, error) {
	vmDir := vmdirectory.NewVMDirectory(s.vmURL(name))
	if err := vmDir.Initialize(overwrite); err != nil {
		return nil, err
	}
	return vmDir, nil
}

// Move ports VMStorageOCI.move(_:from:).
func (s *VMStorageOCI) Move(name oci.RemoteName, from *vmdirectory.VMDirectory) error {
	targetPath := s.vmPath(name)

	// Pre-create intermediate directories (e.g. ~/.weave/cache/OCIs/
	// github.com/org/repo/ for github.com/org/repo:latest).
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}

	return FileManagerReplaceItem(objcutil.NSURLFromPath(targetPath), from.BaseURL)
}

// Delete ports VMStorageOCI.delete(_:).
func (s *VMStorageOCI) Delete(name oci.RemoteName) error {
	if err := os.RemoveAll(s.vmPath(name)); err != nil {
		return err
	}
	return s.GC()
}

// GC ports VMStorageOCI.gc(): removes tag symlinks with broken targets and
// digest directories with no incoming references.
func (s *VMStorageOCI) GC() error {
	refCounts := map[string]uint{}

	err := filepath.WalkDir(s.basePath(), func(path string, entry fs.DirEntry, err error) error {
		if err != nil || path == s.basePath() {
			return nil
		}

		isSymlink := entry.Type()&fs.ModeSymlink != 0

		resolved, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil {
			resolved = path
		}

		// Perform garbage collection for tag-based images with broken
		// outgoing references.
		if isSymlink && resolved == path {
			return os.Remove(path)
		}

		vmDir := vmdirectory.NewVMDirectory(objcutil.NSURLFromPath(resolved))
		if !vmDir.Initialized() {
			return nil
		}

		if isSymlink {
			refCounts[resolved]++
		} else if _, ok := refCounts[resolved]; !ok {
			refCounts[resolved] = 0
		}

		if !isSymlink {
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Perform garbage collection for digest-based images with no incoming
	// references.
	for basePath, incRefCount := range refCounts {
		vmDir := vmdirectory.NewVMDirectory(objcutil.NSURLFromPath(basePath))
		if !vmDir.IsExplicitlyPulled() && incRefCount == 0 {
			if err := os.RemoveAll(basePath); err != nil {
				return err
			}
		}
	}

	return nil
}

// OCIVMEntry is one element of VMStorageOCI.list()'s result tuple.
type OCIVMEntry struct {
	Name      string
	VMDir     *vmdirectory.VMDirectory
	IsSymlink bool
}

// List ports VMStorageOCI.list().
func (s *VMStorageOCI) List() ([]OCIVMEntry, error) {
	var result []OCIVMEntry

	base := s.basePath()
	err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || path == base {
			return nil
		}

		vmDir := vmdirectory.NewVMDirectory(objcutil.NSURLFromPath(path))
		if !vmDir.Initialized() {
			return nil
		}

		relative, relErr := filepath.Rel(base, path)
		if relErr != nil {
			return nil
		}

		// Join the relative VM path's directory and last component with
		// ":" for tags (symlinks) or "@" for digests.
		isSymlink := entry.Type()&fs.ModeSymlink != 0
		separator := "@"
		if isSymlink {
			separator = ":"
		}
		name := filepath.ToSlash(filepath.Dir(relative)) + separator + filepath.Base(relative)

		// Remove the percent-encoding, if any.
		result = append(result, OCIVMEntry{Name: percentDecodeName(name), VMDir: vmDir, IsSymlink: isSymlink})

		if !isSymlink {
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

// Prunables ports VMStorageOCI.prunables(): digest-based images only.
func (s *VMStorageOCI) Prunables() ([]prune.Prunable, error) {
	entries, err := s.List()
	if err != nil {
		return nil, err
	}
	var prunables []prune.Prunable
	for _, entry := range entries {
		if !entry.IsSymlink {
			prunables = append(prunables, entry.VMDir)
		}
	}
	return prunables, nil
}

// Pull ports VMStorageOCI.pull(_:registry:concurrency:deduplicate:).
func (s *VMStorageOCI) Pull(ctx context.Context, name oci.RemoteName, source weaveregistry.ImageSource, concurrency uint, deduplicate bool) error {
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("oci.image-name", name.String()))

	logging.DefaultLogger().AppendNewLine("pulling manifest...")

	manifest, manifestData, err := source.PullManifest(ctx, name.Reference.Value)
	if err != nil {
		return err
	}

	digestName := oci.RemoteName{Host: name.Host, Namespace: name.Namespace,
		Reference: oci.NewDigestReference(oci.DigestHash(manifestData))}

	if s.Exists(name) && s.Exists(digestName) && s.Linked(name, digestName) {
		// Optimistically check if we need to do anything at all before locking.
		logging.DefaultLogger().AppendNewLine(fmt.Sprintf("%s image is already cached and linked!", digestName))
		return nil
	}

	// Ensure that the host directory for the given RemoteName exists in OCI
	// storage, then lock it to prevent concurrent pulls for a single host.
	hostDirectoryPath := s.hostDirectoryPath(digestName)
	if err := os.MkdirAll(hostDirectoryPath, 0o755); err != nil {
		return err
	}

	lock, err := weavelock.NewFileLock(objcutil.NSURLFromPath(hostDirectoryPath))
	if err != nil {
		return err
	}
	defer lock.Close()

	locked, err := lock.Trylock()
	if err != nil {
		return err
	}
	if !locked {
		fmt.Println("waiting for lock...")
		if err := lock.Lock(); err != nil {
			return err
		}
	}
	defer func() { _ = lock.Unlock() }()

	if err := ctx.Err(); err != nil {
		return err
	}

	if !s.Exists(digestName) {
		ctx, span := telemetry.OTelShared().Tracer.Start(ctx, "pull")
		defer span.End()

		tmpVMDir, err := vmdirectory.VMDirectoryTemporaryDeterministic(name.String())
		if err != nil {
			return err
		}

		// Open an existing VM directory corresponding to this name, if any,
		// marking it as outdated to speed up garbage collection.
		_, _ = s.Open(name, time.Unix(0, 0))

		// Lock the temporary VM directory to prevent its garbage collection.
		tmpVMDirLock, err := weavelock.NewFileLock(tmpVMDir.BaseURL)
		if err != nil {
			return err
		}
		defer tmpVMDirLock.Close()
		if err := tmpVMDirLock.Lock(); err != nil {
			return err
		}

		// Before the first byte is transferred, make sure the host volume
		// can hold the image (any format) — reclaiming cache space first,
		// then refusing the download outright if still insufficient.
		if uncompressedDiskSize, ok := oci.EstimatedUncompressedDiskSize(manifest); ok {
			span.SetAttributes(attribute.Int64("oci.image-uncompressed-disk-size-bytes", int64(uncompressedDiskSize)))

			const otherVMFilesSize = 128 * 1024 * 1024
			if err := EnsureDiskSpace(uncompressedDiskSize+otherVMFilesSize, nil); err != nil {
				return err
			}
		}

		err = objcutil.RetryOnURLError(5, func() error {
			// Choose the best base image which has the most deduplication
			// ratio.
			localLayerCache, err := s.ChooseLocalLayerCache(ctx, name, manifest, source)
			if err != nil {
				return err
			}

			if localLayerCache != nil {
				deduplicatedHuman := vmdirectory.ByteCountString(int64(localLayerCache.DeduplicatedBytes))
				if deduplicate {
					logging.DefaultLogger().AppendNewLine(fmt.Sprintf(
						"found an image %s that will allow us to deduplicate %s, using it as a base...",
						localLayerCache.Name, deduplicatedHuman))
				} else {
					logging.DefaultLogger().AppendNewLine(fmt.Sprintf(
						"found an image %s that will allow us to avoid fetching %s, will try use it...",
						localLayerCache.Name, deduplicatedHuman))
				}
			}

			return tmpVMDir.PullFromRegistry(ctx, source, manifest, concurrency, localLayerCache, deduplicate)
		})
		if err != nil {
			_, _ = foundation.NSFileManagerDefaultManager().RemoveItemAtURLError(tmpVMDir.BaseURL)
			return err
		}

		if err := s.Move(digestName, tmpVMDir); err != nil {
			return err
		}
	} else {
		logging.DefaultLogger().AppendNewLine(fmt.Sprintf("%s image is already cached! creating a symlink...", digestName))
	}

	if name != digestName {
		// Create new or overwrite the old symbolic link.
		if err := s.Link(name, digestName); err != nil {
			return err
		}
	} else {
		// Ensure that images pulled by content digest are excluded from
		// garbage collection.
		vmdirectory.NewVMDirectory(s.vmURL(name)).MarkExplicitlyPulled()
	}

	// Explicitly mark the image as being accessed so it won't get pruned
	// immediately.
	freshStorage, err := NewVMStorageOCI()
	if err != nil {
		return err
	}
	_, err = freshStorage.Open(name, time.Now())
	return err
}

// Linked ports VMStorageOCI.linked(from:to:).
func (s *VMStorageOCI) Linked(from oci.RemoteName, to oci.RemoteName) bool {
	resolvedFrom, err := os.Readlink(s.vmPath(from))
	if err != nil {
		return false
	}
	return resolvedFrom == s.vmPath(to)
}

// Link ports VMStorageOCI.link(from:to:).
func (s *VMStorageOCI) Link(from oci.RemoteName, to oci.RemoteName) error {
	_ = os.Remove(s.vmPath(from))

	if err := os.Symlink(s.vmPath(to), s.vmPath(from)); err != nil {
		return err
	}

	return s.GC()
}

// ChooseLocalLayerCache ports VMStorageOCI.chooseLocalLayerCache(_:_:_:).
func (s *VMStorageOCI) ChooseLocalLayerCache(ctx context.Context, name oci.RemoteName, manifest oci.OCIManifest, source weaveregistry.ImageSource) (*oci.LocalLayerCache, error) {
	// Calculate how many bytes we'd deduplicate by re-using a manifest.
	target := map[string]int{}
	for _, layer := range manifest.Layers {
		target[layer.Digest] = layer.Size
	}
	calculateDeduplicatedBytes := func(candidate oci.OCIManifest) uint64 {
		var total uint64
		seen := map[string]bool{}
		for _, layer := range candidate.Layers {
			if seen[layer.Digest] {
				continue
			}
			seen[layer.Digest] = true
			if size, ok := target[layer.Digest]; ok {
				total += uint64(size)
			}
		}
		return total
	}

	type candidate struct {
		name              string
		vmDir             *vmdirectory.VMDirectory
		manifest          oci.OCIManifest
		deduplicatedBytes uint64
	}
	var candidates []candidate

	entries, err := s.List()
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsSymlink {
			continue
		}

		manifestJSON, err := os.ReadFile(objcutil.GoStr(entry.VMDir.ManifestURL().Path()))
		if err != nil {
			continue
		}
		candidateManifest, err := oci.NewOCIManifestFromJSON(manifestJSON)
		if err != nil {
			continue
		}

		candidates = append(candidates, candidate{
			name:              entry.Name,
			vmDir:             entry.VMDir,
			manifest:          candidateManifest,
			deduplicatedBytes: calculateDeduplicatedBytes(candidateManifest),
		})
	}

	// Previously we haven't stored the OCI VM image manifests, but still
	// fetched the VM image manifest when pulling a tagged image we already
	// had cached. Keep supporting this for backwards compatibility, but only
	// talk to the registry if we don't already have that manifest.
	if name.Reference.Type == oci.ReferenceTypeTag {
		if vmDir, err := s.Open(name, time.Now()); err == nil {
			if digest, err := s.Digest(name); err == nil {
				alreadyKnown := false
				for _, c := range candidates {
					if d, err := c.manifest.ManifestDigest(); err == nil && d == digest {
						alreadyKnown = true
						break
					}
				}
				if !alreadyKnown {
					if cachedManifest, _, err := source.PullManifest(ctx, digest); err == nil {
						candidates = append(candidates, candidate{
							name:              name.String(),
							vmDir:             vmDir,
							manifest:          cachedManifest,
							deduplicatedBytes: calculateDeduplicatedBytes(cachedManifest),
						})
					}
				}
			}
		}
	}

	// Find the best match based on how many bytes we'll deduplicate; it must
	// save at least 1 GB to qualify.
	var chosen *candidate
	for i := range candidates {
		if candidates[i].deduplicatedBytes <= 1024*1024*1024 {
			continue
		}
		if chosen == nil || candidates[i].deduplicatedBytes > chosen.deduplicatedBytes {
			chosen = &candidates[i]
		}
	}
	if chosen == nil {
		return nil, nil
	}

	return oci.NewLocalLayerCache(chosen.name, chosen.deduplicatedBytes, chosen.vmDir.DiskURL(), chosen.manifest)
}
