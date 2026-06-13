// Translation of a pulled lume image's metadata (oci.VMDescription) into a
// weave-schema config.json. Lives here rather than in the oci package so the
// canonical vmconfig.VMConfig type writes the file — the schema can never
// drift — without creating an oci→vmconfig import cycle.
//go:build darwin

package vmdirectory

import (
	"encoding/base64"
	"strconv"
	"strings"

	"github.com/deploymenttheory/weave/internal/diskimage"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/logging"
	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/oci"
	"github.com/deploymenttheory/weave/internal/vmconfig"

	virtualization "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/virtualization"
)

// writeLumeConfig builds a weave VMConfig from a lume image's description
// (per the translation table in docs/registries-and-image-formats.md)
// and saves it as this directory's config.json.
func (d *VMDirectory) writeLumeConfig(description *oci.VMDescription) error {
	platform, err := lumePlatform(description)
	if err != nil {
		return err
	}

	// Keep the image's MAC when present (tart pull semantics: clone
	// regenerates per-VM); fall back to a random one.
	var macAddress *virtualization.VZMACAddress
	if description.MACAddress != "" {
		macAddress = virtualization.VZMACAddressFromID(objcutil.AllocClass("VZMACAddress")).
			InitWithString(objcutil.NSStr(description.MACAddress))
		if macAddress == nil {
			logging.DefaultLogger().AppendNewLine("warning: lume image carries a malformed MAC address; generating a random one")
		}
	}

	config := vmconfig.NewVMConfig(platform, int(description.CPUCount),
		description.MemorySizeBytes, macAddress, diskimage.DiskImageFormatRaw)

	if width, height, ok := parseLumeDisplay(description.Display); ok {
		config.Display = vmconfig.VMDisplayConfig{Width: width, Height: height}
	}

	return config.Save(d.ConfigURL())
}

// lumePlatform reconstructs the guest platform from the image's base64 VZ
// dataRepresentation payloads, validating them the same way the create path
// does.
func lumePlatform(description *oci.VMDescription) (vmconfig.Platform, error) {
	switch description.OS {
	case "linux":
		return &vmconfig.LinuxPlatform{}, nil

	case "darwin":
		ecidData, err := base64.StdEncoding.DecodeString(description.ECIDBase64)
		if err != nil || len(ecidData) == 0 {
			return nil, weaveerrors.ErrPullFailed("lume image has a missing or malformed machine identifier")
		}
		ecid := virtualization.VZMacMachineIdentifierFromID(objcutil.AllocClass("VZMacMachineIdentifier")).
			InitWithDataRepresentation(objcutil.BytesToNSData(ecidData))
		if ecid == nil {
			return nil, weaveerrors.ErrPullFailed("lume image machine identifier is not a valid VZMacMachineIdentifier")
		}

		hardwareModelData, err := base64.StdEncoding.DecodeString(description.HardwareModelBase64)
		if err != nil || len(hardwareModelData) == 0 {
			return nil, weaveerrors.ErrPullFailed("lume image has a missing or malformed hardware model")
		}
		hardwareModel := virtualization.VZMacHardwareModelFromID(objcutil.AllocClass("VZMacHardwareModel")).
			InitWithDataRepresentation(objcutil.BytesToNSData(hardwareModelData))
		if hardwareModel == nil {
			return nil, vmconfig.UnsupportedHostOSError{}
		}
		if !hardwareModel.IsSupported() {
			return nil, vmconfig.UnsupportedHostOSError{}
		}
		return vmconfig.NewDarwinPlatform(ecid, hardwareModel), nil

	default:
		return nil, weaveerrors.ErrPullFailed("lume image declares unsupported guest OS %q", description.OS)
	}
}

// parseLumeDisplay parses lume's "WxH" display strings.
func parseLumeDisplay(display string) (width int, height int, ok bool) {
	widthValue, heightValue, found := strings.Cut(strings.ToLower(display), "x")
	if !found {
		return 0, 0, false
	}
	width, widthErr := strconv.Atoi(strings.TrimSpace(widthValue))
	height, heightErr := strconv.Atoi(strings.TrimSpace(heightValue))
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}
