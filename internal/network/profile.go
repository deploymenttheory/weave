// Named scenario profiles. Each profile expands into a per-NIC topology
// ([]vmconfig.NICConfig) implementing one of weave's supported isolation
// scenarios. Profiles are the friendly surface over the per-NIC primitives;
// power users compose topologies directly with --net-device.
//
// Every profile preserves VM management: framebuffer VNC (_VZVNCServer) and the
// vsock agent are network-independent and work under any guest isolation. SSH
// over the guest IP is available only when the topology has a host-reachable
// NIC (noted per profile below).
//go:build darwin

package network

import (
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/vmconfig"
)

// Profile names the supported scenario presets.
type Profile string

const (
	// ProfileNAT — Apple shared/NAT. Internet + host + VM-to-VM. SSH works.
	ProfileNAT Profile = "nat"
	// ProfileInternetOnly — softnet default filtering: the guest reaches the
	// internet but not the host or other VMs (softnet blocks private ranges by
	// default). Management: VNC + vsock; SSH only if a port is exposed.
	ProfileInternetOnly Profile = "internet-only"
	// ProfileIsolated — air-gapped: softnet blocks all egress (0.0.0.0/0). No
	// internet, host, or peers. Management: VNC + vsock only.
	ProfileIsolated Profile = "isolated"
	// ProfileVMLab — vmnet host-mode segment: VMs on the segment interconnect
	// and reach the host, but not the internet. For pentest/exploit labs.
	// Requires the com.apple.vm.networking entitlement or root.
	// Management: VNC + vsock (SSH via the host-mode subnet).
	ProfileVMLab Profile = "vm-lab"
	// ProfileBridged — bridged onto a host physical interface; the guest is a
	// peer on the LAN. Requires the entitlement or root. SSH works.
	ProfileBridged Profile = "bridged"
)

// ProfileOptions carries the parameters a profile may need.
type ProfileOptions struct {
	// BridgedInterface is the host interface name for the bridged profile
	// (empty selects the first available interface).
	BridgedInterface string
	// SoftnetExpose forwards host ports to the guest for the internet-only and
	// isolated profiles, e.g. "2222:22" to keep SSH reachable.
	SoftnetExpose string
	// VmnetSubnet/VmnetMask give the vm-lab segment a specific subnet so that
	// distinct labs are isolated from each other (empty uses the vmnet default).
	VmnetSubnet string
	VmnetMask   string
}

// ExpandProfile resolves a profile name into a NIC topology. The first NIC is
// marked primary; MAC addresses are left empty for the caller to bind from the
// VM config (primary) or derive (secondary).
func ExpandProfile(name string, opts ProfileOptions) ([]vmconfig.NICConfig, error) {
	switch Profile(name) {
	case ProfileNAT:
		return []vmconfig.NICConfig{{Mode: vmconfig.NICModeNAT, IsPrimary: true}}, nil

	case ProfileInternetOnly:
		// softnet with no allow/block is internet-only by default (it blocks
		// private ranges and permits globally-routable addresses).
		return []vmconfig.NICConfig{{
			Mode:          vmconfig.NICModeSoftnet,
			IsPrimary:     true,
			SoftnetExpose: opts.SoftnetExpose,
		}}, nil

	case ProfileIsolated:
		return []vmconfig.NICConfig{{
			Mode:          vmconfig.NICModeSoftnet,
			IsPrimary:     true,
			SoftnetBlock:  "0.0.0.0/0",
			SoftnetExpose: opts.SoftnetExpose,
		}}, nil

	case ProfileVMLab:
		return []vmconfig.NICConfig{{
			Mode:        vmconfig.NICModeVmnet,
			IsPrimary:   true,
			VmnetMode:   "host",
			VmnetSubnet: opts.VmnetSubnet,
			VmnetMask:   opts.VmnetMask,
		}}, nil

	case ProfileBridged:
		return []vmconfig.NICConfig{{
			Mode:             vmconfig.NICModeBridged,
			IsPrimary:        true,
			BridgedInterface: opts.BridgedInterface,
		}}, nil

	default:
		return nil, weaveerrors.ErrGeneric(
			"unknown network profile %q (want nat|internet-only|isolated|vm-lab|bridged)", name)
	}
}
