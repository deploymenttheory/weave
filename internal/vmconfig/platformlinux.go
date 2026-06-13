// Port of tart's Platform/Linux.swift: the Linux guest platform.
//go:build darwin

package vmconfig

import (
	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	virtualization "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/virtualization"
	idiomatic "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/virtualization"
	"github.com/deploymenttheory/weave/internal/objcutil"
	weaveplatform "github.com/deploymenttheory/weave/internal/platform"
)

// LinuxPlatform ports tart's Linux struct (named to avoid clashing with the
// weaveplatform.OS constant).
type LinuxPlatform struct{}

var _ Platform = (*LinuxPlatform)(nil)

func (p *LinuxPlatform) platformEncodeJSON(object map[string]any) error {
	// Linux contributes no platform-specific keys.
	return nil
}

func (p *LinuxPlatform) OS() weaveplatform.OS { return weaveplatform.OSLinux }

func (p *LinuxPlatform) BootLoader(nvramURL *foundation.NSURL) (*virtualization.VZBootLoader, error) {
	result := idiomatic.NewEFIBootLoader().Unwrap()

	result.SetVariableStore(
		virtualization.VZEFIVariableStoreFromID(objcutil.AllocClass("VZEFIVariableStore")).InitWithURL(nvramURL))

	return &result.VZBootLoader, nil
}

func (p *LinuxPlatform) Platform(nvramURL *foundation.NSURL, needsNestedVirtualization bool) (*virtualization.VZPlatformConfiguration, error) {
	config := idiomatic.NewGenericPlatformConfiguration().Unwrap()
	if weaveplatform.MacOSAtLeast(15) {
		config.SetNestedVirtualizationEnabled(needsNestedVirtualization)
	}
	return &config.VZPlatformConfiguration, nil
}

func (p *LinuxPlatform) GraphicsDevice(vmConfig *VMConfig) *virtualization.VZGraphicsDeviceConfiguration {
	result := idiomatic.NewVirtioGraphicsDeviceConfiguration().Unwrap()

	scanout := virtualization.VZVirtioGraphicsScanoutConfigurationFromID(objcutil.AllocClass("VZVirtioGraphicsScanoutConfiguration")).
		InitWithWidthInPixelsHeightInPixels(vmConfig.Display.Width, vmConfig.Display.Height)
	result.SetScanouts(objcutil.NSArrayFromIDs[*virtualization.VZVirtioGraphicsScanoutConfiguration](scanout.Ptr()))

	return &result.VZGraphicsDeviceConfiguration
}

func (p *LinuxPlatform) Keyboards() []*virtualization.VZKeyboardConfiguration {
	return []*virtualization.VZKeyboardConfiguration{
		&idiomatic.NewUSBKeyboardConfiguration().Unwrap().VZKeyboardConfiguration,
	}
}

func (p *LinuxPlatform) PointingDevices() []*virtualization.VZPointingDeviceConfiguration {
	return []*virtualization.VZPointingDeviceConfiguration{
		&idiomatic.NewUSBScreenCoordinatePointingDeviceConfiguration().Unwrap().VZPointingDeviceConfiguration,
	}
}

func (p *LinuxPlatform) PointingDevicesSimplified() []*virtualization.VZPointingDeviceConfiguration {
	// Linux doesn't support the trackpad, so just return the regular
	// pointing devices.
	return p.PointingDevices()
}
