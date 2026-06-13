// Parser for the --net-device primitive spec, the power-user surface beneath
// the named profiles. One spec describes one NIC:
//
//	nat
//	bridged:en0
//	softnet,block=10.0.0.0/8,expose=2222:22
//	softnet,host
//	vmnet,mode=host,subnet=192.168.66.1,mask=255.255.255.0,nonat
//
// Any spec may carry ,mac=<addr> and ,primary. The leading token is the mode,
// optionally with a :value shorthand (the bridged/vmnet host interface).
//go:build darwin

package network

import (
	"strings"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/vmconfig"
)

// ParseNICDevice parses a single --net-device spec into a NICConfig.
func ParseNICDevice(spec string) (vmconfig.NICConfig, error) {
	parts := strings.Split(spec, ",")
	head := strings.TrimSpace(parts[0])
	if head == "" {
		return vmconfig.NICConfig{}, weaveerrors.ErrGeneric("empty --net-device spec")
	}

	modeToken, headValue, _ := strings.Cut(head, ":")
	nic := vmconfig.NICConfig{}

	switch modeToken {
	case "nat":
		nic.Mode = vmconfig.NICModeNAT
	case "bridged":
		nic.Mode = vmconfig.NICModeBridged
		nic.BridgedInterface = headValue
	case "softnet":
		nic.Mode = vmconfig.NICModeSoftnet
	case "vmnet":
		nic.Mode = vmconfig.NICModeVmnet
		nic.BridgedInterface = headValue
	default:
		return vmconfig.NICConfig{}, weaveerrors.ErrGeneric(
			"unknown --net-device mode %q (want nat|bridged|softnet|vmnet)", modeToken)
	}

	for _, raw := range parts[1:] {
		option := strings.TrimSpace(raw)
		if option == "" {
			continue
		}
		key, value, hasValue := strings.Cut(option, "=")

		// Mode-agnostic options.
		switch key {
		case "mac":
			nic.MACAddress = value
			continue
		case "primary":
			nic.IsPrimary = true
			continue
		}

		switch nic.Mode {
		case vmconfig.NICModeSoftnet:
			switch key {
			case "allow":
				nic.SoftnetAllow = value
			case "block":
				nic.SoftnetBlock = value
			case "expose":
				nic.SoftnetExpose = value
			case "host":
				nic.SoftnetHostMode = true
			default:
				return vmconfig.NICConfig{}, weaveerrors.ErrGeneric("unknown softnet option %q", key)
			}
		case vmconfig.NICModeVmnet:
			switch key {
			case "mode":
				nic.VmnetMode = value
			case "subnet":
				nic.VmnetSubnet = value
			case "mask":
				nic.VmnetMask = value
			case "nodhcp":
				nic.VmnetNoDHCP = true
			case "nonat":
				nic.VmnetNoNAT = true
			default:
				return vmconfig.NICConfig{}, weaveerrors.ErrGeneric("unknown vmnet option %q", key)
			}
		default:
			return vmconfig.NICConfig{}, weaveerrors.ErrGeneric(
				"option %q is not valid for mode %q", option, string(nic.Mode))
		}
		_ = hasValue
	}

	return nic, nil
}

// ParseNICDevices parses multiple --net-device specs, marking the first NIC
// primary when none is explicitly tagged.
func ParseNICDevices(specs []string) ([]vmconfig.NICConfig, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	nics := make([]vmconfig.NICConfig, 0, len(specs))
	primarySeen := false
	for _, spec := range specs {
		nic, err := ParseNICDevice(spec)
		if err != nil {
			return nil, err
		}
		if nic.IsPrimary {
			primarySeen = true
		}
		nics = append(nics, nic)
	}
	if !primarySeen {
		nics[0].IsPrimary = true
	}
	return nics, nil
}
