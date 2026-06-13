// Port of tart's Commands/Set.swift.
//go:build darwin

package command

import (
	"context"
	"runtime"
	"strconv"
	"strings"

	"github.com/deploymenttheory/weave/internal/vmconfig"

	weaveconfig "github.com/deploymenttheory/weave/internal/config"
	"github.com/deploymenttheory/weave/internal/diskimage"
	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/vmstorage"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	virtualization "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/virtualization"
)

// SetCommand ports the Set command.
type SetCommand struct {
	Name         string
	CPU          *uint16
	Memory       *uint64
	Display      *vmconfig.VMDisplayConfig
	DisplayRefit *bool
	RandomMAC    bool
	RandomSerial bool
	Disk         string
	DiskSize     *uint16
}

func (c *SetCommand) Run(ctx context.Context) error {
	storage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return err
	}
	vmDir, err := storage.Open(c.Name)
	if err != nil {
		return err
	}
	vmConfig, err := vmconfig.NewVMConfigFromURL(vmDir.ConfigURL())
	if err != nil {
		return err
	}

	if c.CPU != nil {
		if err := vmConfig.SetCPU(int(*c.CPU)); err != nil {
			return err
		}
	}

	if c.Memory != nil {
		if err := vmConfig.SetMemory(*c.Memory * 1024 * 1024); err != nil {
			return err
		}
	}

	if c.Display != nil {
		if c.Display.Width > 0 {
			vmConfig.Display.Width = c.Display.Width
		}
		if c.Display.Height > 0 {
			vmConfig.Display.Height = c.Display.Height
		}
		vmConfig.Display.Unit = c.Display.Unit
	}

	vmConfig.DisplayRefit = c.DisplayRefit

	if c.RandomMAC {
		vmConfig.MACAddress = virtualization.VZMACAddressRandomLocallyAdministeredAddress()
	}

	if c.RandomSerial && runtime.GOARCH == "arm64" {
		if oldPlatform, ok := vmConfig.Platform.(*vmconfig.DarwinPlatform); ok {
			ecid := virtualization.VZMacMachineIdentifierFromID(objcutil.AllocClass("VZMacMachineIdentifier")).Init()
			vmConfig.Platform = vmconfig.NewDarwinPlatform(ecid, oldPlatform.HardwareModel)
		}
	}

	if err := vmConfig.Save(vmDir.ConfigURL()); err != nil {
		return err
	}

	if c.Disk != "" {
		config, err := weaveconfig.NewConfig()
		if err != nil {
			return err
		}
		temporaryDiskURL := config.WeaveTmpDir.URLByAppendingPathComponent(
			objcutil.NSStr("set-disk-" + objcutil.GoStr(foundation.NSUUIDUUID().UUIDString())))

		if _, err := foundation.NSFileManagerDefaultManager().
			CopyItemAtURLToURLError(objcutil.NSURLFromPath(c.Disk), temporaryDiskURL); err != nil {
			return err
		}
		if err := vmstorage.FileManagerReplaceItem(vmDir.DiskURL(), temporaryDiskURL); err != nil {
			return err
		}
	}

	if c.DiskSize != nil {
		return vmDir.ResizeDisk(*c.DiskSize, diskimage.DiskImageFormatRaw)
	}

	return nil
}

// ParseVMDisplayConfig ports the VMDisplayConfig ExpressibleByArgument
// conformance: WIDTHxHEIGHT with an optional pt/px suffix.
func ParseVMDisplayConfig(argument string) vmconfig.VMDisplayConfig {
	var unit *vmconfig.VMDisplayConfigUnit

	if strings.HasSuffix(argument, string(vmconfig.VMDisplayConfigUnitPixel)) {
		argument = strings.TrimSuffix(argument, string(vmconfig.VMDisplayConfigUnitPixel))
		pixel := vmconfig.VMDisplayConfigUnitPixel
		unit = &pixel
	} else if strings.HasSuffix(argument, string(vmconfig.VMDisplayConfigUnitPoint)) {
		argument = strings.TrimSuffix(argument, string(vmconfig.VMDisplayConfigUnitPoint))
		point := vmconfig.VMDisplayConfigUnitPoint
		unit = &point
	}

	parts := strings.Split(argument, "x")
	config := vmconfig.VMDisplayConfig{Unit: unit}
	if len(parts) > 0 {
		config.Width, _ = strconv.Atoi(parts[0])
	}
	if len(parts) > 1 {
		config.Height, _ = strconv.Atoi(parts[1])
	}
	return config
}
