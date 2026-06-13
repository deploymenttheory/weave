// Port of tart's Commands/Prune.swift: the prune command plus the automatic
// cache-reclaim helpers used by VMStorageOCI during pulls.
//go:build darwin

package command

import (
	"time"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/prune"
	"github.com/deploymenttheory/weave/internal/vmstorage"
)

// PruneCommand ports the Prune AsyncParsableCommand's options.
type PruneCommand struct {
	Entries     string // "caches" or "vms"
	OlderThan   *uint
	SpaceBudget *uint
	GC          bool
}

// Validate ports Prune.validate().
func (c *PruneCommand) Validate() error {
	if c.OlderThan == nil && c.SpaceBudget == nil && !c.GC {
		return weaveerrors.ErrGeneric("at least one pruning criteria must be specified")
	}
	return nil
}

// Run ports Prune.run().
func (c *PruneCommand) Run() error {
	if c.GC {
		storage, err := vmstorage.NewVMStorageOCI()
		if err != nil {
			return err
		}
		if err := storage.GC(); err != nil {
			return err
		}
	}

	// Build a list of prunable storages to prune based on the user's request.
	prunableStorages, err := vmstorage.PruneStoragesFor(c.Entries)
	if err != nil {
		return err
	}

	// Clean up cache entries based on the last accessed date.
	if c.OlderThan != nil {
		olderThanDate := time.Now().AddDate(0, 0, -int(*c.OlderThan))
		if err := pruneOlderThan(prunableStorages, olderThanDate); err != nil {
			return err
		}
	}

	// Clean up cache entries based on the imposed cache size limit and the
	// entry's last accessed date.
	if c.SpaceBudget != nil {
		if err := pruneSpaceBudget(prunableStorages, uint64(*c.SpaceBudget)*1024*1024*1024); err != nil {
			return err
		}
	}

	return nil
}

// pruneOlderThan ports Prune.pruneOlderThan(prunableStorages:olderThanDate:).
func pruneOlderThan(prunableStorages []prune.PrunableStorage, olderThanDate time.Time) error {
	prunables, err := vmstorage.CollectPrunables(prunableStorages)
	if err != nil {
		return err
	}

	for _, prunable := range prunables {
		accessDate, err := prunable.AccessDate()
		if err != nil {
			return err
		}
		if !accessDate.After(olderThanDate) {
			if err := prunable.Delete(); err != nil {
				return err
			}
		}
	}
	return nil
}

// pruneSpaceBudget ports Prune.pruneSpaceBudget(prunableStorages:
// spaceBudgetBytes:): keep the most recently used entries that fit.
func pruneSpaceBudget(prunableStorages []prune.PrunableStorage, spaceBudgetBytes uint64) error {
	prunables, err := vmstorage.CollectPrunables(prunableStorages)
	if err != nil {
		return err
	}
	if err := vmstorage.SortPrunablesByAccessDate(prunables, true); err != nil {
		return err
	}

	var prunablesToDelete []prune.Prunable
	for _, prunable := range prunables {
		sizeBytes, err := prunable.AllocatedSizeBytes()
		if err != nil {
			return err
		}

		if uint64(sizeBytes) <= spaceBudgetBytes {
			// Don't mark for deletion: there's budget available.
			spaceBudgetBytes -= uint64(sizeBytes)
		} else {
			// Mark for deletion.
			prunablesToDelete = append(prunablesToDelete, prunable)
		}
	}

	for _, prunable := range prunablesToDelete {
		if err := prunable.Delete(); err != nil {
			return err
		}
	}
	return nil
}
