// Prunable-storage helpers shared by the prune command and the OCI storage
// auto-prune (extracted from the prune command when the monolith was split:
// they construct every cache storage, so they live with the storages).
//go:build darwin

package vmstorage

import (
	"fmt"
	"os"
	"sort"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"

	weaveconfig "github.com/deploymenttheory/weave/internal/config"
	"github.com/deploymenttheory/weave/internal/ipsw"
	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/prune"
)

func PruneStoragesFor(entries string) ([]prune.PrunableStorage, error) {
	switch entries {
	case "caches":
		ociStorage, err := NewVMStorageOCI()
		if err != nil {
			return nil, err
		}
		ipswCache, err := ipsw.NewIPSWCache()
		if err != nil {
			return nil, err
		}
		return []prune.PrunableStorage{ociStorage, ipswCache}, nil
	case "vms":
		localStorage, err := NewVMStorageLocal()
		if err != nil {
			return nil, err
		}
		return []prune.PrunableStorage{localStorage}, nil
	default:
		return nil, weaveerrors.ErrGeneric("unsupported --entries value, please specify either \"caches\" or \"vms\"")
	}
}

// ReclaimIfNeeded ports Prune.reclaimIfNeeded(_:_:): frees cache space
// when the volume cannot accommodate requiredBytes.
func ReclaimIfNeeded(requiredBytes uint64, initiator prune.Prunable) error {
	if _, ok := objcutil.EnvironmentValue("WEAVE_NO_AUTO_PRUNE"); ok {
		return nil
	}

	// Figure out how much disk space is available (framework-queried; see
	// AvailableCapacityBytes).
	config, err := weaveconfig.NewConfig()
	if err != nil {
		return err
	}
	volumeAvailableCapacityCalculated, err := AvailableCapacityBytes(objcutil.GoStr(config.WeaveCacheDir.Path()))
	if err != nil {
		return err
	}

	if volumeAvailableCapacityCalculated == 0 {
		return nil
	}

	// Now that we know how much free space is left, check if we even need
	// to reclaim anything.
	if requiredBytes < volumeAvailableCapacityCalculated {
		return nil
	}

	return reclaimIfPossible(requiredBytes-volumeAvailableCapacityCalculated, initiator)
}

// reclaimIfPossible ports Prune.reclaimIfPossible(_:_:).
func reclaimIfPossible(reclaimBytes uint64, initiator prune.Prunable) error {
	storages, err := PruneStoragesFor("caches")
	if err != nil {
		return err
	}
	prunables, err := CollectPrunables(storages)
	if err != nil {
		return err
	}
	if err := SortPrunablesByAccessDate(prunables, false); err != nil {
		return err
	}

	// Does it even make sense to start?
	var cacheUsedBytes uint64
	for _, prunable := range prunables {
		sizeBytes, err := prunable.AllocatedSizeBytes()
		if err != nil {
			return err
		}
		cacheUsedBytes += uint64(sizeBytes)
	}
	if cacheUsedBytes < reclaimBytes {
		return nil
	}

	var initiatorPath string
	if initiator != nil {
		if resolved, err := os.Readlink(objcutil.GoStr(initiator.URL().Path())); err == nil {
			initiatorPath = resolved
		} else {
			initiatorPath = objcutil.GoStr(initiator.URL().Path())
		}
	}

	var cacheReclaimedBytes uint64
	for _, prunable := range prunables {
		if cacheReclaimedBytes > reclaimBytes {
			break
		}

		// Do not prune the initiator.
		if initiatorPath != "" && objcutil.GoStr(prunable.URL().Path()) == initiatorPath {
			continue
		}

		allocatedSizeBytes, err := prunable.AllocatedSizeBytes()
		if err != nil {
			return err
		}
		cacheReclaimedBytes += uint64(allocatedSizeBytes)

		if err := prunable.Delete(); err != nil {
			return err
		}
	}

	return nil
}

func CollectPrunables(storages []prune.PrunableStorage) ([]prune.Prunable, error) {
	var prunables []prune.Prunable
	for _, storage := range storages {
		entries, err := storage.Prunables()
		if err != nil {
			return nil, err
		}
		prunables = append(prunables, entries...)
	}
	return prunables, nil
}

// SortPrunablesByAccessDate sorts prunables by access date — ascending
// (least recently used first) by default, descending when newestFirst.
func SortPrunablesByAccessDate(prunables []prune.Prunable, newestFirst bool) error {
	var sortErr error
	sort.SliceStable(prunables, func(i, j int) bool {
		a, errA := prunables[i].AccessDate()
		b, errB := prunables[j].AccessDate()
		if errA != nil && sortErr == nil {
			sortErr = errA
		}
		if errB != nil && sortErr == nil {
			sortErr = errB
		}
		if newestFirst {
			return a.After(b)
		}
		return a.Before(b)
	})
	if sortErr != nil {
		return fmt.Errorf("failed to sort prunables: %w", sortErr)
	}
	return nil
}
