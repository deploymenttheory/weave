// NAT networking (VZNATNetworkDeviceAttachment), entitlement-free. Ports
// tart's NetworkShared. Attachment-only: there is no out-of-band lifecycle, so
// NAT NICs carry no engine.
//go:build darwin

package network

import (
	virtualization "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/virtualization"
	idiomatic "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/virtualization"
)

// buildNAT constructs a NAT NIC.
func buildNAT(mac *virtualization.VZMACAddress) (NIC, error) {
	attachment := &idiomatic.NewNATNetworkDeviceAttachment().Unwrap().VZNetworkDeviceAttachment
	return NIC{Attachment: attachment, MAC: mac}, nil
}
