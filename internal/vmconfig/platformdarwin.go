// Port of tart's Platform/Darwin.swift: the macOS guest platform. idiomatic
// wrappers provide the constructors; raw setters configure the objects.
//go:build darwin

package vmconfig

import (
	"encoding/base64"

	weaveplatform "github.com/deploymenttheory/weave/internal/platform"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/objcutil"

	appkit "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/appkit"
	corefoundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/corefoundation"
	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	virtualization "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/virtualization"
	idiomatic "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/virtualization"
)

// UnsupportedHostOSError ports Darwin.swift's UnsupportedHostOSError.
type UnsupportedHostOSError struct{}

func (UnsupportedHostOSError) Error() string {
	return "error: host macOS version is outdated to run this virtual machine"
}

// DarwinPlatform ports tart's Darwin struct (named to avoid clashing with
// the weaveplatform.OS constant).
type DarwinPlatform struct {
	ECID          *virtualization.VZMacMachineIdentifier
	HardwareModel *virtualization.VZMacHardwareModel
}

var (
	_ Platform            = (*DarwinPlatform)(nil)
	_ PlatformSuspendable = (*DarwinPlatform)(nil)
)

// NewDarwinPlatform ports Darwin.init(ecid:hardwareModel:).
func NewDarwinPlatform(ecid *virtualization.VZMacMachineIdentifier, hardwareModel *virtualization.VZMacHardwareModel) *DarwinPlatform {
	return &DarwinPlatform{ECID: ecid, HardwareModel: hardwareModel}
}

// newDarwinPlatformFromJSON ports Darwin.init(from:): decodes the
// base64-encoded ecid and hardwareModel keys.
func newDarwinPlatformFromJSON(config vmConfigJSON) (*DarwinPlatform, error) {
	ecidData, err := base64.StdEncoding.DecodeString(config.ECID)
	if err != nil {
		return nil, weaveerrors.ErrGeneric("failed to initialize Data using the provided value")
	}
	ecid := virtualization.VZMacMachineIdentifierFromID(objcutil.AllocClass("VZMacMachineIdentifier")).
		InitWithDataRepresentation(objcutil.BytesToNSData(ecidData))
	if ecid == nil {
		return nil, weaveerrors.ErrGeneric("failed to initialize VZMacMachineIdentifier using the provided value")
	}

	hardwareModelData, err := base64.StdEncoding.DecodeString(config.HardwareModel)
	if err != nil {
		return nil, weaveerrors.ErrGeneric("failed to initialize Data using the provided value")
	}
	hardwareModel := virtualization.VZMacHardwareModelFromID(objcutil.AllocClass("VZMacHardwareModel")).
		InitWithDataRepresentation(objcutil.BytesToNSData(hardwareModelData))
	if hardwareModel == nil {
		return nil, UnsupportedHostOSError{}
	}

	return &DarwinPlatform{ECID: ecid, HardwareModel: hardwareModel}, nil
}

func (p *DarwinPlatform) platformEncodeJSON(object map[string]any) error {
	object["ecid"] = base64.StdEncoding.EncodeToString(objcutil.NSDataToBytes(p.ECID.DataRepresentation()))
	object["hardwareModel"] = base64.StdEncoding.EncodeToString(objcutil.NSDataToBytes(p.HardwareModel.DataRepresentation()))
	return nil
}

func (p *DarwinPlatform) OS() weaveplatform.OS { return weaveplatform.OSDarwin }

func (p *DarwinPlatform) BootLoader(nvramURL *foundation.NSURL) (*virtualization.VZBootLoader, error) {
	return &idiomatic.NewMacOSBootLoader().Unwrap().VZBootLoader, nil
}

func (p *DarwinPlatform) Platform(nvramURL *foundation.NSURL, needsNestedVirtualization bool) (*virtualization.VZPlatformConfiguration, error) {
	if needsNestedVirtualization {
		return nil, weaveerrors.ErrVMConfigurationError("macOS virtual machines do not support nested virtualization")
	}

	result := idiomatic.NewMacPlatformConfiguration().Unwrap()

	result.SetMachineIdentifier(p.ECID)
	result.SetAuxiliaryStorage(
		virtualization.VZMacAuxiliaryStorageFromID(objcutil.AllocClass("VZMacAuxiliaryStorage")).InitWithURL(nvramURL))

	if !p.HardwareModel.IsSupported() {
		// At the moment support of the M1 chip is not yet dropped in any
		// macOS version, which means the host software does not support
		// this hardware model and should be updated.
		return nil, UnsupportedHostOSError{}
	}

	result.SetHardwareModel(p.HardwareModel)

	return &result.VZPlatformConfiguration, nil
}

func (p *DarwinPlatform) GraphicsDevice(vmConfig *VMConfig) *virtualization.VZGraphicsDeviceConfiguration {
	result := idiomatic.NewMacGraphicsDeviceConfiguration().Unwrap()

	unit := VMDisplayConfigUnitPoint
	if vmConfig.Display.Unit != nil {
		unit = *vmConfig.Display.Unit
	}
	if hostMainScreen := appkit.NSScreenMainScreen(); unit == VMDisplayConfigUnitPoint && hostMainScreen != nil {
		vmScreenSize := corefoundation.CGSize{
			Width:  float64(vmConfig.Display.Width),
			Height: float64(vmConfig.Display.Height),
		}
		display := virtualization.VZMacGraphicsDisplayConfigurationFromID(objcutil.AllocClass("VZMacGraphicsDisplayConfiguration")).
			InitForScreenSizeInPoints(hostMainScreen, vmScreenSize)
		result.SetDisplays(objcutil.NSArrayFromIDs[*virtualization.VZMacGraphicsDisplayConfiguration](display.Ptr()))
		return &result.VZGraphicsDeviceConfiguration
	}

	display := virtualization.VZMacGraphicsDisplayConfigurationFromID(objcutil.AllocClass("VZMacGraphicsDisplayConfiguration")).
		// 72 PPI is a reasonable guess according to Apple's
		// CGDisplayScreenSize documentation.
		InitWithWidthInPixelsHeightInPixelsPixelsPerInch(vmConfig.Display.Width, vmConfig.Display.Height, 72)
	result.SetDisplays(objcutil.NSArrayFromIDs[*virtualization.VZMacGraphicsDisplayConfiguration](display.Ptr()))

	return &result.VZGraphicsDeviceConfiguration
}

func (p *DarwinPlatform) Keyboards() []*virtualization.VZKeyboardConfiguration {
	// The Mac keyboard is only supported by guests starting with macOS
	// Ventura; tart gates it on the host running macOS 14.
	if weaveplatform.MacOSAtLeast(14) {
		return []*virtualization.VZKeyboardConfiguration{
			&idiomatic.NewUSBKeyboardConfiguration().Unwrap().VZKeyboardConfiguration,
			&idiomatic.NewMacKeyboardConfiguration().Unwrap().VZKeyboardConfiguration,
		}
	}
	return []*virtualization.VZKeyboardConfiguration{
		&idiomatic.NewUSBKeyboardConfiguration().Unwrap().VZKeyboardConfiguration,
	}
}

func (p *DarwinPlatform) KeyboardsSuspendable() []*virtualization.VZKeyboardConfiguration {
	if weaveplatform.MacOSAtLeast(14) {
		return []*virtualization.VZKeyboardConfiguration{
			&idiomatic.NewMacKeyboardConfiguration().Unwrap().VZKeyboardConfiguration,
		}
	}
	return p.Keyboards()
}

func (p *DarwinPlatform) PointingDevices() []*virtualization.VZPointingDeviceConfiguration {
	// The trackpad is only supported by guests starting with macOS Ventura.
	return []*virtualization.VZPointingDeviceConfiguration{
		&idiomatic.NewUSBScreenCoordinatePointingDeviceConfiguration().Unwrap().VZPointingDeviceConfiguration,
		&idiomatic.NewMacTrackpadConfiguration().Unwrap().VZPointingDeviceConfiguration,
	}
}

func (p *DarwinPlatform) PointingDevicesSimplified() []*virtualization.VZPointingDeviceConfiguration {
	// Only include the USB pointing device, not the trackpad.
	return []*virtualization.VZPointingDeviceConfiguration{
		&idiomatic.NewUSBScreenCoordinatePointingDeviceConfiguration().Unwrap().VZPointingDeviceConfiguration,
	}
}

func (p *DarwinPlatform) PointingDevicesSuspendable() []*virtualization.VZPointingDeviceConfiguration {
	if weaveplatform.MacOSAtLeast(14) {
		return []*virtualization.VZPointingDeviceConfiguration{
			&idiomatic.NewMacTrackpadConfiguration().Unwrap().VZPointingDeviceConfiguration,
		}
	}
	return p.PointingDevices()
}
