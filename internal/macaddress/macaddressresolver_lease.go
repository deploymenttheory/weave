// Port of tart's MACAddressResolver/Lease.swift.
//go:build darwin

package macaddress

import (
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// arphrdEther is ARPHRD_ETHER from <net/if_arp.h>.
const arphrdEther = 1

// Lease mirrors tart's Lease struct.
type Lease struct {
	MAC       MACAddress
	IP        netip.Addr
	ExpiresAt time.Time
}

// NewLease ports Lease.init?(fromRawLease:).
func NewLease(fromRawLease map[string]string) (Lease, bool) {
	// Retrieve the required fields.
	hwAddress, ok := fromRawLease["hw_address"]
	if !ok {
		return Lease{}, false
	}
	ipAddress, ok := fromRawLease["ip_address"]
	if !ok {
		return Lease{}, false
	}
	leaseValue, ok := fromRawLease["lease"]
	if !ok {
		return Lease{}, false
	}

	// Parse the MAC address.
	hwAddressProto, hwAddressMAC, found := strings.Cut(hwAddress, ",")
	if !found {
		return Lease{}, false
	}
	if proto, err := strconv.Atoi(hwAddressProto); err == nil && proto != arphrdEther {
		return Lease{}, false
	}
	mac, ok := NewMACAddress(hwAddressMAC)
	if !ok {
		return Lease{}, false
	}

	// Parse the IP address.
	ip, err := netip.ParseAddr(ipAddress)
	if err != nil || !ip.Is4() {
		return Lease{}, false
	}

	// Parse the expiration timestamp (a 0x-prefixed hex value).
	leaseTimestamp, err := strconv.ParseInt(strings.TrimPrefix(leaseValue, "0x"), 16, 64)
	if err != nil {
		return Lease{}, false
	}

	return Lease{
		MAC:       mac,
		IP:        ip,
		ExpiresAt: time.Unix(leaseTimestamp, 0),
	}, true
}
