// Port of tart's Commands/Create.swift.
//go:build darwin

package command

import (
	"context"
	"runtime"
	"strings"

	"github.com/deploymenttheory/weave/internal/diskimage"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	weavelock "github.com/deploymenttheory/weave/internal/lock"
	weavenetwork "github.com/deploymenttheory/weave/internal/network"
	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/terminal"
	weavevm "github.com/deploymenttheory/weave/internal/vm"
	"github.com/deploymenttheory/weave/internal/vmconfig"
	"github.com/deploymenttheory/weave/internal/vmdirectory"
	"github.com/deploymenttheory/weave/internal/vmstorage"

	"github.com/ebitengine/purego/objc"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	virtualization "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/virtualization"
	"github.com/deploymenttheory/go-bindings-macosplatform/internal/pureobjc"
)

// CreateCommand ports the Create command.
type CreateCommand struct {
	Name       string
	FromIPSW   string
	Linux      bool
	DiskSize   uint16
	DiskFormat diskimage.DiskImageFormat
	// NetProfile optionally persists a default network profile into the new
	// VM's config (nat|internet-only|isolated|vm-lab|bridged). Empty leaves
	// the VM on the implicit single NAT NIC, overridable at run time.
	NetProfile string
}

func (c *CreateCommand) Validate() error {
	if c.FromIPSW == "" && !c.Linux {
		return weaveerrors.ErrGeneric("Please specify either a --from-ipsw or --linux option!")
	}
	if runtime.GOARCH != "arm64" && c.FromIPSW != "" {
		return weaveerrors.ErrGeneric("Only Linux VMs are supported on Intel!")
	}

	// Validate disk format support.
	if !c.DiskFormat.IsSupported() {
		return weaveerrors.ErrGeneric("Disk format '%s' is not supported on this system.", c.DiskFormat)
	}

	// Validate the network profile name up front.
	if c.NetProfile != "" {
		if _, err := weavenetwork.ExpandProfile(c.NetProfile, weavenetwork.ProfileOptions{}); err != nil {
			return err
		}
	}
	return nil
}

func (c *CreateCommand) Run(ctx context.Context) error {
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

	if c.FromIPSW != "" {
		var ipswURL *foundation.NSURL

		switch {
		case c.FromIPSW == "latest":
			spinner := terminal.NewSpinner("Looking up the latest supported IPSW")
			spinner.Start()
			image, err := FetchLatestSupportedRestoreImage(ctx)
			if err != nil {
				spinner.Fail("Failed to look up the latest supported IPSW")
				cleanup()
				return err
			}
			spinner.Success("Found the latest supported IPSW")
			ipswURL = image.URL()
		case strings.HasPrefix(c.FromIPSW, "http://") || strings.HasPrefix(c.FromIPSW, "https://"):
			ipswURL = foundation.NSURLURLWithString(objcutil.NSStr(c.FromIPSW))
		default:
			ipswURL = objcutil.NSURLFromPath(objcutil.ExpandTilde(c.FromIPSW))
		}

		if _, err := weavevm.NewVMInstallingFromIPSW(ctx, tmpVMDir, ipswURL, c.DiskSize, c.DiskFormat, weavevm.VMOptions{}); err != nil {
			cleanup()
			return err
		}
	}

	if c.Linux {
		if _, err := weavevm.VMLinux(tmpVMDir, c.DiskSize, c.DiskFormat); err != nil {
			cleanup()
			return err
		}
	}

	if err := c.persistNetworkProfile(tmpVMDir); err != nil {
		cleanup()
		return err
	}

	localStorage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		cleanup()
		return err
	}
	if err := localStorage.Move(c.Name, tmpVMDir); err != nil {
		cleanup()
		return err
	}

	return nil
}

// persistNetworkProfile writes the chosen --net-profile into the new VM's
// config as a persisted NIC topology. The primary NIC inherits the config's
// MAC; any secondary NICs get deterministic derived MACs. A no-op when no
// profile was requested.
func (c *CreateCommand) persistNetworkProfile(vmDir *vmdirectory.VMDirectory) error {
	if c.NetProfile == "" {
		return nil
	}

	config, err := vmconfig.NewVMConfigFromURL(vmDir.ConfigURL())
	if err != nil {
		return err
	}

	nics, err := weavenetwork.ExpandProfile(c.NetProfile, weavenetwork.ProfileOptions{})
	if err != nil {
		return err
	}

	primaryMAC := objcutil.GoStr(config.MACAddress.String())
	for i := range nics {
		switch {
		case nics[i].IsPrimary:
			nics[i].MACAddress = primaryMAC
		case nics[i].MACAddress == "":
			nics[i].MACAddress = vmconfig.DeriveMACAddress(primaryMAC, i)
		}
	}

	config.NICs = nics
	return config.Save(vmDir.ConfigURL())
}

// FetchLatestSupportedRestoreImage bridges
// VZMacOSRestoreImage.fetchLatestSupported() through a manual block.
func FetchLatestSupportedRestoreImage(ctx context.Context) (*virtualization.VZMacOSRestoreImage, error) {
	type result struct {
		image *virtualization.VZMacOSRestoreImage
		err   error
	}
	resultCh := make(chan result, 1)

	block := objc.NewBlock(func(_ objc.Block, imageID objc.ID, errID objc.ID) {
		if errID != 0 {
			resultCh <- result{err: pureobjc.NSErrorToError(errID)}
			return
		}
		resultCh <- result{image: virtualization.VZMacOSRestoreImageFromID(pureobjc.Retain(imageID))}
	})
	objc.ID(objc.GetClass("VZMacOSRestoreImage")).Send(
		objc.RegisterName("fetchLatestSupportedWithCompletionHandler:"), block)

	select {
	case r := <-resultCh:
		return r.image, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
