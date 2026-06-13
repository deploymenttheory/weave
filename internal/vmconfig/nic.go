// Per-NIC network configuration. Weave models each VM's networking as an
// ordered list of NICs, every one carrying its own mode, MAC address, and
// mode-specific properties. This supersedes the single shared MAC + single
// Network of the original tart port and enables multi-NIC topologies with
// mixed isolation (e.g. an isolated lab NIC plus a NAT management NIC).
//go:build darwin

package vmconfig

import (
	"crypto/sha256"
	"fmt"

	"github.com/deploymenttheory/weave/internal/objcutil"
)

// NICMode is the networking mode of a single NIC.
type NICMode string

const (
	// NICModeNAT is Apple's built-in shared/NAT attachment
	// (VZNATNetworkDeviceAttachment). Entitlement-free. VM reaches the
	// internet and the host; VMs on the same shared subnet can reach each
	// other.
	NICModeNAT NICMode = "nat"
	// NICModeBridged bridges the NIC onto a host physical interface
	// (VZBridgedNetworkDeviceAttachment). Requires the com.apple.vm.networking
	// entitlement or root.
	NICModeBridged NICMode = "bridged"
	// NICModeSoftnet routes through the softnet userspace packet filter
	// (VZFileHandleNetworkDeviceAttachment + helper process). Entitlement-free;
	// supports allow/block/expose and host-only filtering. This is weave's
	// default isolation engine.
	NICModeSoftnet NICMode = "softnet"
	// NICModeVmnet builds a custom vmnet network (host/shared/bridged) and
	// attaches it via VZVmnetNetworkDeviceAttachment. Advanced opt-in;
	// requires the com.apple.vm.networking entitlement or root.
	NICModeVmnet NICMode = "vmnet"
)

// NICConfig describes a single virtual NIC. Mode-specific fields are only
// meaningful for their corresponding Mode and are omitted from JSON when empty.
type NICConfig struct {
	Mode       NICMode `json:"mode"`
	MACAddress string  `json:"macAddress"`
	IsPrimary  bool    `json:"isPrimary,omitempty"`

	// Bridged.
	BridgedInterface string `json:"bridgedInterface,omitempty"`

	// Softnet.
	SoftnetAllow    string `json:"softnetAllow,omitempty"`
	SoftnetBlock    string `json:"softnetBlock,omitempty"`
	SoftnetExpose   string `json:"softnetExpose,omitempty"`
	SoftnetHostMode bool   `json:"softnetHostMode,omitempty"`

	// Vmnet (advanced). VmnetMode is "host" | "shared" | "bridged".
	VmnetMode   string `json:"vmnetMode,omitempty"`
	VmnetSubnet string `json:"vmnetSubnet,omitempty"`
	VmnetMask   string `json:"vmnetMask,omitempty"`
	VmnetNoDHCP bool   `json:"vmnetNoDhcp,omitempty"`
	VmnetNoNAT  bool   `json:"vmnetNoNat,omitempty"`
}

// PrimaryNIC returns the NIC marked primary, or the first NIC if none is
// marked, or nil for an empty list. The primary NIC's MAC is the address used
// for guest IP resolution (DHCP/ARP) and running-VM MAC-conflict detection.
func (c *VMConfig) PrimaryNIC() *NICConfig {
	if len(c.NICs) == 0 {
		return nil
	}
	for i := range c.NICs {
		if c.NICs[i].IsPrimary {
			return &c.NICs[i]
		}
	}
	return &c.NICs[0]
}

// EnsureNICs returns the configured NICs, synthesising a single primary NAT
// NIC from the legacy MACAddress field when none are configured (backward
// compatibility with configs written before per-NIC networking). It does not
// mutate the config.
func (c *VMConfig) EnsureNICs() []NICConfig {
	if len(c.NICs) > 0 {
		return c.NICs
	}
	mac := ""
	if c.MACAddress != nil {
		mac = objcutil.GoStr(c.MACAddress.String())
	}
	return []NICConfig{{Mode: NICModeNAT, MACAddress: mac, IsPrimary: true}}
}

// DeriveMACAddress produces a deterministic, locally-administered unicast MAC
// string for a secondary NIC, derived from the primary MAC and the NIC index.
// Determinism keeps a multi-NIC guest's MACs stable across runs (so DHCP leases
// persist) without having to write them back into config.json on every run.
func DeriveMACAddress(primaryMAC string, index int) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s#nic%d", primaryMAC, index))
	octets := sum[:6]
	// Force locally-administered (bit 1 set) and unicast (bit 0 clear) on the
	// first octet, matching VZMACAddress.randomLocallyAdministeredAddress().
	octets[0] = (octets[0] | 0x02) &^ 0x01
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		octets[0], octets[1], octets[2], octets[3], octets[4], octets[5])
}
