// Port of tart's MACAddressResolver/MACAddress.swift.
//go:build darwin

package macaddress

import (
	"fmt"
	"strconv"
	"strings"
)

// MACAddress mirrors tart's MACAddress struct.
type MACAddress struct {
	MAC [6]uint8
}

// NewMACAddress ports MACAddress.init?(fromString:).
func NewMACAddress(fromString string) (MACAddress, bool) {
	components := strings.Split(fromString, ":")
	if len(components) != 6 {
		return MACAddress{}, false
	}

	var address MACAddress
	for index, component := range components {
		value, err := strconv.ParseUint(component, 16, 8)
		if err != nil {
			return MACAddress{}, false
		}
		address.MAC[index] = uint8(value)
	}
	return address, true
}

func (a MACAddress) String() string {
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		a.MAC[0], a.MAC[1], a.MAC[2], a.MAC[3], a.MAC[4], a.MAC[5])
}
