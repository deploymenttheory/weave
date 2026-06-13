// Port of tart's Commands/Import.swift.
//go:build darwin

package command

import (
	"context"
	"fmt"
	"strings"

	weaveconfig "github.com/deploymenttheory/weave/internal/config"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	weavelock "github.com/deploymenttheory/weave/internal/lock"
	"github.com/deploymenttheory/weave/internal/vmdirectory"
	"github.com/deploymenttheory/weave/internal/vmstorage"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
)

// ImportCommand ports the Import command.
type ImportCommand struct {
	Path string
	Name string
}

func (c *ImportCommand) Validate() error {
	if strings.Contains(c.Name, "/") {
		return weaveerrors.ErrGeneric("<name> should be a local name")
	}
	return nil
}

func (c *ImportCommand) Run(ctx context.Context) error {
	localStorage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return err
	}

	// Create a temporary VM directory to which we will load the export file,
	// and lock it to prevent garbage collection while we're running.
	tmpVMDir, err := vmdirectory.VMDirectoryTemporary()
	if err != nil {
		return err
	}
	tmpVMDirLock, err := weavelock.NewFileLock(tmpVMDir.BaseURL)
	if err != nil {
		return err
	}
	defer tmpVMDirLock.Close()
	if err := tmpVMDirLock.Lock(); err != nil {
		return err
	}

	// Populate the temporary VM directory with the export file contents.
	fmt.Println("importing...")
	if err := tmpVMDir.ImportFromArchive(c.Path); err != nil {
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
	defer func() { _ = globalLock.Unlock() }()

	// Re-generate the VM's MAC address when importing it would result in an
	// address collision.
	mac, err := tmpVMDir.MACAddress()
	if err != nil {
		cleanup()
		return err
	}
	hasCollision, err := localStorage.HasVMsWithMACAddress(mac)
	if err != nil {
		cleanup()
		return err
	}
	if hasCollision {
		if err := tmpVMDir.RegenerateMACAddress(); err != nil {
			cleanup()
			return err
		}
	}

	if err := localStorage.Move(c.Name, tmpVMDir); err != nil {
		cleanup()
		return err
	}

	return nil
}
