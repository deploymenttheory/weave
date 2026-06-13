// Pre-download disk-space guard: every multi-gigabyte download (OCI pulls in
// any image format, IPSW fetches) checks the host volume through the
// framework before the first byte is transferred — first reclaiming prunable
// cache entries, then failing with an actionable error instead of running
// the disk to ENOSPC mid-transfer.
//
// Capacity is read against the actual filesystem via the Foundation
// framework: the idiomatic URL wrapper (opinionated/idiomatic/foundation) is
// unwrapped onto NSURL's volume resource values, preferring
// NSURLVolumeAvailableCapacityForImportantUsageKey — Apple's recommended
// "how much can a user-initiated operation really write" figure, which
// accounts for purgeable space that statfs(2) cannot see.
//go:build darwin

package vmstorage

import (
	weaveconfig "github.com/deploymenttheory/weave/internal/config"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/prune"
	"github.com/deploymenttheory/weave/internal/vmdirectory"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	"github.com/deploymenttheory/go-bindings-macosplatform/internal/pureobjc"
	idiomaticfoundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/foundation"
)

// AvailableCapacityBytes returns the volume capacity available for a
// user-initiated write at path, queried through the framework.
func AvailableCapacityBytes(path string) (uint64, error) {
	url := idiomaticfoundation.NewURLFileURLWithPathIsDirectory(path, true).Unwrap()

	available, err := objcutil.URLResourceValue(url, objcutil.WrapperID(foundation.NSURLVolumeAvailableCapacityKey()))
	if err != nil {
		return 0, err
	}
	availableImportant, err := objcutil.URLResourceValue(url, objcutil.WrapperID(foundation.NSURLVolumeAvailableCapacityForImportantUsageKey()))
	if err != nil {
		return 0, err
	}

	var capacity uint64
	if available != 0 {
		capacity = uint64(foundation.NSNumberFromID(pureobjc.Retain(available)).IntegerValue())
	}
	if availableImportant != 0 {
		if v := uint64(foundation.NSNumberFromID(pureobjc.Retain(availableImportant)).UnsignedLongLongValue()); v > capacity {
			capacity = v
		}
	}
	return capacity, nil
}

// EnsureDiskSpace verifies the volume hosting weave's cache directory can
// hold requiredBytes before a download starts. Prunable cache entries are
// reclaimed first (unless WEAVE_NO_AUTO_PRUNE is set); if space is still
// insufficient the download is refused up front.
func EnsureDiskSpace(requiredBytes uint64, initiator prune.Prunable) error {
	if requiredBytes == 0 {
		return nil
	}

	config, err := weaveconfig.NewConfig()
	if err != nil {
		return err
	}
	cachePath := objcutil.GoStr(config.WeaveCacheDir.Path())

	// Make room if we can.
	if err := ReclaimIfNeeded(requiredBytes, initiator); err != nil {
		return err
	}

	available, err := AvailableCapacityBytes(cachePath)
	if err != nil {
		return err
	}
	if available == 0 {
		// The framework could not determine capacity (e.g. exotic volumes);
		// proceed rather than block — matching ReclaimIfNeeded's behaviour.
		return nil
	}
	if available < requiredBytes {
		return weaveerrors.ErrGeneric(
			"not enough disk space for this download: %s required, %s available on the volume hosting %s\n"+
				"(free up space or prune cached images: weave prune --entries caches)",
			vmdirectory.ByteCountString(int64(requiredBytes)),
			vmdirectory.ByteCountString(int64(available)),
			cachePath)
	}
	return nil
}
