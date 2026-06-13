// Port of tart's Platform/Platform.swift. The Codable conformance becomes
// the platformEncodeJSON hook, which flattens platform-specific keys into
// the VMConfig JSON object exactly like the Swift encode(to:) overloads.
//go:build darwin

package vmconfig

import (
	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	virtualization "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/virtualization"
	weaveplatform "github.com/deploymenttheory/weave/internal/platform"
)

// Platform ports tart's Platform protocol.
type Platform interface {
	OS() weaveplatform.OS
	BootLoader(nvramURL *foundation.NSURL) (*virtualization.VZBootLoader, error)
	Platform(nvramURL *foundation.NSURL, needsNestedVirtualization bool) (*virtualization.VZPlatformConfiguration, error)
	GraphicsDevice(vmConfig *VMConfig) *virtualization.VZGraphicsDeviceConfiguration
	Keyboards() []*virtualization.VZKeyboardConfiguration
	PointingDevices() []*virtualization.VZPointingDeviceConfiguration
	PointingDevicesSimplified() []*virtualization.VZPointingDeviceConfiguration

	// platformEncodeJSON adds the platform-specific keys (e.g. Darwin's
	// ecid/hardwareModel) to the VMConfig JSON object.
	platformEncodeJSON(object map[string]any) error
}

// PlatformSuspendable ports tart's PlatformSuspendable protocol.
type PlatformSuspendable interface {
	Platform
	PointingDevicesSuspendable() []*virtualization.VZPointingDeviceConfiguration
	KeyboardsSuspendable() []*virtualization.VZKeyboardConfiguration
}
