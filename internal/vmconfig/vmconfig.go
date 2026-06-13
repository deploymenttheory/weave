// Port of tart's VMConfig.swift. Swift's Codable conformance becomes
// MarshalJSON/UnmarshalJSON; encoding goes through a map[string]any so the
// standard library emits sorted keys, matching Config.jsonEncoder()'s
// .sortedKeys output. Platform-specific keys are flattened into the same
// object via Platform.platformEncodeJSON, mirroring platform.encode(to:).
//go:build darwin

package vmconfig

import (
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/deploymenttheory/weave/internal/clipboardpolicy"
	"github.com/deploymenttheory/weave/internal/diskimage"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/objcutil"
	weaveplatform "github.com/deploymenttheory/weave/internal/platform"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	virtualization "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/virtualization"
)

// LessThanMinimalResourcesError ports VMConfig.swift's class of the same name.
type LessThanMinimalResourcesError struct {
	UserExplanation string
}

func (e *LessThanMinimalResourcesError) Error() string {
	return "LessThanMinimalResourcesError: " + e.UserExplanation
}

// VMDisplayConfigUnit mirrors VMDisplayConfig.Unit.
type VMDisplayConfigUnit string

const (
	VMDisplayConfigUnitPoint VMDisplayConfigUnit = "pt"
	VMDisplayConfigUnitPixel VMDisplayConfigUnit = "px"
)

// VMDisplayConfig mirrors tart's VMDisplayConfig struct.
type VMDisplayConfig struct {
	Width  int                  `json:"width"`
	Height int                  `json:"height"`
	Unit   *VMDisplayConfigUnit `json:"unit,omitempty"`
}

func defaultVMDisplayConfig() VMDisplayConfig {
	return VMDisplayConfig{Width: 1024, Height: 768}
}

// String ports VMDisplayConfig's CustomStringConvertible conformance.
func (d VMDisplayConfig) String() string {
	if d.Unit != nil {
		return fmt.Sprintf("%dx%d%s", d.Width, d.Height, *d.Unit)
	}
	return fmt.Sprintf("%dx%d", d.Width, d.Height)
}

// VMConfig mirrors tart's VMConfig struct. CPUCount and MemorySize are
// private(set) in Swift; mutate them only through SetCPU/SetMemory.
type VMConfig struct {
	Version       int
	OS            weaveplatform.OS
	Arch          weaveplatform.Architecture
	Platform      Platform
	CPUCountMin   int
	CPUCount      int
	MemorySizeMin uint64
	MemorySize    uint64
	MACAddress    *virtualization.VZMACAddress
	Display       VMDisplayConfig
	DisplayRefit  *bool
	DiskFormat    diskimage.DiskImageFormat
	// NICs is the per-NIC network topology. Empty means "legacy single NAT
	// NIC", synthesised on demand via EnsureNICs from MACAddress. The primary
	// NIC's MAC mirrors MACAddress for backward compatibility.
	NICs []NICConfig
	// ClipboardPolicy optionally overrides the enterprise clipboard policy for
	// this VM. nil means "use the settings default / CLI flags".
	ClipboardPolicy *clipboardpolicy.Policy
}

// NewVMConfig ports VMConfig.init(platform:cpuCountMin:memorySizeMin:
// macAddress:diskFormat:). A nil macAddress selects a random
// locally-administered address, like the Swift default argument.
func NewVMConfig(platform Platform, cpuCountMin int, memorySizeMin uint64, macAddress *virtualization.VZMACAddress, diskFormat diskimage.DiskImageFormat) *VMConfig {
	if macAddress == nil {
		macAddress = virtualization.VZMACAddressRandomLocallyAdministeredAddress()
	}
	return &VMConfig{
		Version:       1,
		OS:            platform.OS(),
		Arch:          weaveplatform.CurrentArchitecture(),
		Platform:      platform,
		CPUCountMin:   cpuCountMin,
		CPUCount:      cpuCountMin,
		MemorySizeMin: memorySizeMin,
		MemorySize:    memorySizeMin,
		MACAddress:    macAddress,
		Display:       defaultVMDisplayConfig(),
		DiskFormat:    diskFormat,
	}
}

// NewVMConfigFromJSON ports VMConfig.init(fromJSON:).
func NewVMConfigFromJSON(data []byte) (*VMConfig, error) {
	config := &VMConfig{}
	if err := json.Unmarshal(data, config); err != nil {
		return nil, err
	}
	return config, nil
}

// NewVMConfigFromURL ports VMConfig.init(fromURL:).
func NewVMConfigFromURL(url *foundation.NSURL) (*VMConfig, error) {
	data, err := foundation.NSDataDataWithContentsOfURLOptionsError(url, 0)
	if err != nil {
		return nil, err
	}
	return NewVMConfigFromJSON(objcutil.NSDataToBytes(data))
}

// ToJSON ports VMConfig.toJSON(): compact JSON with sorted keys.
func (c *VMConfig) ToJSON() ([]byte, error) {
	return json.Marshal(c)
}

// Save ports VMConfig.save(toURL:): pretty-printed JSON written atomically.
func (c *VMConfig) Save(toURL *foundation.NSURL) error {
	object, err := c.jsonObject()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(object, "", "  ")
	if err != nil {
		return err
	}
	if !objcutil.BytesToNSData(data).WriteToURLAtomically(toURL, true) {
		return weaveerrors.ErrGeneric("failed to write VM configuration to %s", objcutil.GoStr(toURL.Path()))
	}
	return nil
}

func (c *VMConfig) jsonObject() (map[string]any, error) {
	object := map[string]any{
		"version":       c.Version,
		"os":            c.OS,
		"arch":          c.Arch,
		"cpuCountMin":   c.CPUCountMin,
		"cpuCount":      c.CPUCount,
		"memorySizeMin": c.MemorySizeMin,
		"memorySize":    c.MemorySize,
		"macAddress":    objcutil.GoStr(c.MACAddress.String()),
		"display":       c.Display,
		"diskFormat":    string(c.DiskFormat),
	}
	if c.DisplayRefit != nil {
		object["displayRefit"] = *c.DisplayRefit
	}
	if len(c.NICs) > 0 {
		object["nics"] = c.NICs
	}
	if c.ClipboardPolicy != nil {
		object["clipboardPolicy"] = c.ClipboardPolicy
	}
	if err := c.Platform.platformEncodeJSON(object); err != nil {
		return nil, err
	}
	return object, nil
}

// MarshalJSON ports VMConfig.encode(to:).
func (c *VMConfig) MarshalJSON() ([]byte, error) {
	object, err := c.jsonObject()
	if err != nil {
		return nil, err
	}
	return json.Marshal(object)
}

// vmConfigJSON is the decoding container for VMConfig.init(from:), including
// the flattened platform-specific keys.
type vmConfigJSON struct {
	Version         int                         `json:"version"`
	OS              *weaveplatform.OS           `json:"os"`
	Arch            *weaveplatform.Architecture `json:"arch"`
	CPUCountMin     int                         `json:"cpuCountMin"`
	CPUCount        int                         `json:"cpuCount"`
	MemorySizeMin   uint64                      `json:"memorySizeMin"`
	MemorySize      uint64                      `json:"memorySize"`
	MACAddress      string                      `json:"macAddress"`
	Display         *VMDisplayConfig            `json:"display"`
	DisplayRefit    *bool                       `json:"displayRefit"`
	DiskFormat      string                      `json:"diskFormat"`
	NICs            []NICConfig                 `json:"nics"`
	ClipboardPolicy *clipboardpolicy.Policy     `json:"clipboardPolicy"`

	// macOS-specific keys
	ECID          string `json:"ecid"`
	HardwareModel string `json:"hardwareModel"`
}

// UnmarshalJSON ports VMConfig.init(from:).
func (c *VMConfig) UnmarshalJSON(data []byte) error {
	var decoded vmConfigJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	c.Version = decoded.Version

	c.OS = weaveplatform.OSDarwin
	if decoded.OS != nil {
		c.OS = *decoded.OS
	}
	c.Arch = weaveplatform.ArchitectureARM64
	if decoded.Arch != nil {
		c.Arch = *decoded.Arch
	}

	switch c.OS {
	case weaveplatform.OSLinux:
		c.Platform = &LinuxPlatform{}
	default:
		if runtime.GOARCH != "arm64" {
			return weaveerrors.ErrGeneric("Darwin VMs are only supported on Apple Silicon hosts")
		}
		platform, err := newDarwinPlatformFromJSON(decoded)
		if err != nil {
			return err
		}
		c.Platform = platform
	}

	c.CPUCountMin = decoded.CPUCountMin
	c.CPUCount = decoded.CPUCount
	c.MemorySizeMin = decoded.MemorySizeMin
	c.MemorySize = decoded.MemorySize

	macAddress := virtualization.VZMACAddressFromID(objcutil.AllocClass("VZMACAddress")).
		InitWithString(objcutil.NSStr(decoded.MACAddress))
	if macAddress == nil {
		return weaveerrors.ErrGeneric("failed to initialize VZMacAddress using the provided value")
	}
	c.MACAddress = macAddress

	// Per-NIC topology. When present it is authoritative; otherwise it stays
	// empty and EnsureNICs synthesises a single primary NAT NIC from
	// MACAddress on demand. Keep MACAddress in sync with the primary NIC so
	// legacy consumers (VMDirectory.MACAddress, IP resolution) keep working.
	c.NICs = decoded.NICs
	if primary := c.PrimaryNIC(); primary != nil && primary.MACAddress != "" {
		primaryMAC := virtualization.VZMACAddressFromID(objcutil.AllocClass("VZMACAddress")).
			InitWithString(objcutil.NSStr(primary.MACAddress))
		if primaryMAC != nil {
			c.MACAddress = primaryMAC
		}
	}

	c.Display = defaultVMDisplayConfig()
	if decoded.Display != nil {
		c.Display = *decoded.Display
	}
	c.DisplayRefit = decoded.DisplayRefit

	c.DiskFormat = diskimage.DiskImageFormatRaw
	if format, ok := diskimage.ParseDiskImageFormat(decoded.DiskFormat); ok {
		c.DiskFormat = format
	}

	c.ClipboardPolicy = decoded.ClipboardPolicy

	return nil
}

// SetCPU ports VMConfig.setCPU(cpuCount:).
func (c *VMConfig) SetCPU(cpuCount int) error {
	if c.OS == weaveplatform.OSDarwin && cpuCount < c.CPUCountMin {
		return &LessThanMinimalResourcesError{UserExplanation: fmt.Sprintf(
			"VM should have %d CPU cores at minimum (requested %d)", c.CPUCountMin, cpuCount)}
	}

	if minimumAllowed := int(virtualization.VZVirtualMachineConfigurationMinimumAllowedCPUCount()); cpuCount < minimumAllowed {
		return &LessThanMinimalResourcesError{UserExplanation: fmt.Sprintf(
			"VM should have %d CPU cores at minimum (requested %d)", minimumAllowed, cpuCount)}
	}

	c.CPUCount = cpuCount
	return nil
}

// SetMemory ports VMConfig.setMemory(memorySize:).
func (c *VMConfig) SetMemory(memorySize uint64) error {
	if c.OS == weaveplatform.OSDarwin && memorySize < c.MemorySizeMin {
		return &LessThanMinimalResourcesError{UserExplanation: fmt.Sprintf(
			"VM should have %d bytes of memory at minimum (requested %d)", c.MemorySizeMin, memorySize)}
	}

	if minimumAllowed := virtualization.VZVirtualMachineConfigurationMinimumAllowedMemorySize(); memorySize < minimumAllowed {
		return &LessThanMinimalResourcesError{UserExplanation: fmt.Sprintf(
			"VM should have %d bytes of memory at minimum (requested %d)", minimumAllowed, memorySize)}
	}

	c.MemorySize = memorySize
	return nil
}
