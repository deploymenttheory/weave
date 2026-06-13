// Port of tart's Commands/Clone.swift.
//go:build darwin

package command

import (
	"context"
	"strings"

	weaveregistry "github.com/deploymenttheory/weave/internal/registry"

	weaveconfig "github.com/deploymenttheory/weave/internal/config"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	weavelock "github.com/deploymenttheory/weave/internal/lock"
	"github.com/deploymenttheory/weave/internal/oci"
	"github.com/deploymenttheory/weave/internal/vmdirectory"
	"github.com/deploymenttheory/weave/internal/vmstorage"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
)

// CloneCommand ports the Clone command.
type CloneCommand struct {
	SourceName  string
	NewName     string
	Registry    string // registry profile name for a remote source
	Insecure    bool
	Concurrency uint
	Deduplicate bool
	PruneLimit  uint
}

func (c *CloneCommand) Validate() error {
	if strings.Contains(c.NewName, "/") {
		return weaveerrors.ErrGeneric("<new-name> should be a local name")
	}
	if c.Concurrency < 1 {
		return weaveerrors.ErrGeneric("network concurrency cannot be less than 1")
	}
	return nil
}

func (c *CloneCommand) Run(ctx context.Context) error {
	ociStorage, err := vmstorage.NewVMStorageOCI()
	if err != nil {
		return err
	}
	localStorage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return err
	}

	// A remote source: either an explicit --registry profile, or a
	// fully-qualified reference. Pull it into the OCI cache if missing.
	sourceName := c.SourceName
	if _, parseErr := oci.NewRemoteName(c.SourceName); parseErr == nil || c.Registry != "" {
		settings, err := weaveconfig.LoadSettings()
		if err != nil {
			return err
		}
		client, remoteName, err := weaveregistry.Resolve(c.SourceName, c.Registry, c.Insecure, settings)
		if err != nil {
			return err
		}
		sourceName = remoteName.String()
		if !ociStorage.Exists(remoteName) {
			if err := ociStorage.Pull(ctx, remoteName, client, c.Concurrency, c.Deduplicate); err != nil {
				return err
			}
		}
	}

	sourceVM, err := vmstorage.VMStorageHelperOpen(sourceName)
	if err != nil {
		return err
	}
	tmpVMDir, err := vmdirectory.VMDirectoryTemporary()
	if err != nil {
		return err
	}

	// Lock the temporary VM directory to prevent its garbage collection.
	tmpVMDirLock, err := weavelock.NewFileLock(tmpVMDir.BaseURL)
	if err != nil {
		return err
	}
	defer tmpVMDirLock.Close()
	if err := tmpVMDirLock.Lock(); err != nil {
		return err
	}

	cleanup := func() {
		_, _ = foundation.NSFileManagerDefaultManager().RemoveItemAtURLError(tmpVMDir.BaseURL)
	}

	// Acquire a global lock.
	config, err := weaveconfig.NewConfig()
	if err != nil {
		cleanup()
		return err
	}
	globalLock, err := weavelock.NewFileLock(config.WeaveHomeDir)
	if err != nil {
		cleanup()
		return err
	}
	defer globalLock.Close()
	if err := globalLock.Lock(); err != nil {
		cleanup()
		return err
	}

	sourceMAC, err := sourceVM.MACAddress()
	if err != nil {
		cleanup()
		return err
	}
	hasCollision, err := localStorage.HasVMsWithMACAddress(sourceMAC)
	if err != nil {
		cleanup()
		return err
	}
	sourceState, err := sourceVM.State()
	if err != nil {
		cleanup()
		return err
	}
	generateMAC := hasCollision && sourceState != vmdirectory.VMDirectoryStateSuspended

	if err := sourceVM.Clone(tmpVMDir, generateMAC); err != nil {
		cleanup()
		return err
	}

	if err := localStorage.Move(c.NewName, tmpVMDir); err != nil {
		cleanup()
		return err
	}

	if err := globalLock.Unlock(); err != nil {
		return err
	}

	// APFS is doing copy-on-write, so the above cloning operation (just
	// copying files on disk) is not actually claiming new space until the VM
	// is started and writes something to disk. So, once we clone the VM,
	// try to claim the rest of the space for the VM to run without errors.
	sizeBytes, err := sourceVM.SizeBytes()
	if err != nil {
		return err
	}
	allocatedSizeBytes, err := sourceVM.AllocatedSizeBytes()
	if err != nil {
		return err
	}
	unallocatedBytes := sizeBytes - allocatedSizeBytes
	// Avoid reclaiming an excessive amount of disk space.
	reclaimBytes := min(unallocatedBytes, int(c.PruneLimit)*1024*1024*1024)
	if reclaimBytes > 0 {
		return vmstorage.ReclaimIfNeeded(uint64(reclaimBytes), sourceVM)
	}

	return nil
}
