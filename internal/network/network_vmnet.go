// vmnet-direct networking: builds a custom vmnet network (host/shared/bridged)
// and attaches it to the VM via VZVmnetNetworkDeviceAttachment. This is the
// advanced, opt-in isolation engine. It requires the com.apple.vm.networking
// entitlement or running as root; without it vmnet_network_configuration_create
// fails and we return a structured error suggesting the softnet alternative.
//
// Capability note: the vmnet_network_configuration_* API consumed by
// VZVmnetNetworkDeviceAttachment exposes mode + subnet + DHCP/NAT toggles, but
// NOT the vmnet_enable_isolation_key (that belongs to the older
// vmnet_start_interface dictionary path). Network-level isolation here is
// therefore achieved by giving each segment a distinct subnet; fine-grained
// host/peer blocking remains softnet's job. vmnet networks are also
// process-scoped — they cannot be shared across separate weave processes.
//go:build darwin

package network

import (
	"runtime"
	"unsafe"

	virtualization "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/virtualization"
	vmnet "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/vmnet"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/vmconfig"
)

// buildVmnetNIC constructs a vmnet-direct NIC.
func buildVmnetNIC(nicConfig vmconfig.NICConfig, mac *virtualization.VZMACAddress) (NIC, error) {
	mode, err := vmnetOperatingMode(nicConfig.VmnetMode)
	if err != nil {
		return NIC{}, err
	}

	var status vmnet.Vmnet_return_t
	config := vmnet.VmnetNetworkConfigurationCreate(mode, &status)
	if config == nil || status != vmnet.VMNET_SUCCESS {
		return NIC{}, weaveerrors.ErrGeneric(
			"failed to create vmnet network configuration (status %s); the vmnet "+
				"engine requires the com.apple.vm.networking entitlement or root — "+
				"consider a softnet-based profile instead", status.String())
	}

	if nicConfig.VmnetMode == "bridged" {
		if nicConfig.BridgedInterface == "" {
			return NIC{}, weaveerrors.ErrGeneric("vmnet bridged mode requires a host interface name")
		}
		if rc := vmnet.VmnetNetworkConfigurationSetExternalInterface(config, nicConfig.BridgedInterface); rc != vmnet.VMNET_SUCCESS {
			return NIC{}, weaveerrors.ErrGeneric("failed to set vmnet external interface %q (status %s)",
				nicConfig.BridgedInterface, rc.String())
		}
	}

	if nicConfig.VmnetSubnet != "" && nicConfig.VmnetMask != "" {
		subnet := cString(nicConfig.VmnetSubnet)
		mask := cString(nicConfig.VmnetMask)
		rc := vmnet.VmnetNetworkConfigurationSetIpv4Subnet(config,
			unsafe.Pointer(&subnet[0]), unsafe.Pointer(&mask[0]))
		runtime.KeepAlive(subnet)
		runtime.KeepAlive(mask)
		if rc != vmnet.VMNET_SUCCESS {
			return NIC{}, weaveerrors.ErrGeneric("failed to set vmnet subnet %s/%s (status %s)",
				nicConfig.VmnetSubnet, nicConfig.VmnetMask, rc.String())
		}
	}

	if nicConfig.VmnetNoDHCP {
		vmnet.VmnetNetworkConfigurationDisableDhcp(config)
	}
	if nicConfig.VmnetNoNAT {
		vmnet.VmnetNetworkConfigurationDisableNat44(config)
	}

	netStatus := vmnet.VMNET_SUCCESS
	netPtr := vmnet.VmnetNetworkCreate(config, &netStatus)
	if netPtr == nil || netStatus != vmnet.VMNET_SUCCESS {
		return NIC{}, weaveerrors.ErrGeneric(
			"failed to create vmnet network (status %s); the vmnet engine requires "+
				"the com.apple.vm.networking entitlement or root", netStatus.String())
	}

	attachment := virtualization.VZVmnetNetworkDeviceAttachmentFromID(
		objcutil.AllocClass("VZVmnetNetworkDeviceAttachment")).InitWithNetwork(netPtr)

	return NIC{
		Attachment: &attachment.VZNetworkDeviceAttachment,
		MAC:        mac,
		engine:     &vmnetEngine{network: netPtr},
	}, nil
}

// vmnetOperatingMode maps the config string to a vmnet operating mode.
func vmnetOperatingMode(mode string) (vmnet.Operating_modes_t, error) {
	switch mode {
	case "host", "":
		return vmnet.VMNET_HOST_MODE, nil
	case "shared":
		return vmnet.VMNET_SHARED_MODE, nil
	case "bridged":
		return vmnet.VMNET_BRIDGED_MODE, nil
	default:
		return 0, weaveerrors.ErrGeneric("unsupported vmnet mode %q (want host|shared|bridged)", mode)
	}
}

// cString returns a null-terminated copy of s for passing to C as a char*.
func cString(s string) []byte {
	return append([]byte(s), 0)
}

// vmnetEngine holds the vmnet network reference for a vmnet-direct NIC. The
// network must outlive the VM, so the reference is held for the VM's lifetime
// and dropped on stop; the process exit reclaims the underlying network.
type vmnetEngine struct {
	network unsafe.Pointer
}

func (e *vmnetEngine) run(sema *AsyncSemaphore) error { return nil }

func (e *vmnetEngine) stop() error {
	// Drop our reference; the vmnet network is released when the process exits.
	// (The configuration API exposes no explicit network-release entry point.)
	e.network = nil
	return nil
}
