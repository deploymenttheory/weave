// Bridged networking (VZBridgedNetworkDeviceAttachment): the NIC is bridged
// onto a host physical interface. Requires the com.apple.vm.networking
// entitlement or root; the entitlement failure surfaces when the VM starts.
// Attachment-only: no out-of-band lifecycle, so bridged NICs carry no engine.
//go:build darwin

package network

import (
	"strings"

	virtualization "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/virtualization"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/vmconfig"

	"github.com/ebitengine/purego/objc"

	"github.com/deploymenttheory/go-bindings-macosplatform/internal/pureobjc"
)

// buildBridged constructs a bridged NIC, resolving the host interface by
// identifier or localized display name.
func buildBridged(nicConfig vmconfig.NICConfig, mac *virtualization.VZMACAddress) (NIC, error) {
	iface, found := FindBridgedInterface(nicConfig.BridgedInterface)
	if !found {
		return NIC{}, weaveerrors.ErrGeneric(
			"no bridge interfaces matched %q, available interfaces: %s",
			nicConfig.BridgedInterface, strings.Join(BridgeInterfaces(), ", "))
	}
	attachment := virtualization.VZBridgedNetworkDeviceAttachmentFromID(
		objcutil.AllocClass("VZBridgedNetworkDeviceAttachment")).InitWithInterface(iface)
	return NIC{Attachment: &attachment.VZNetworkDeviceAttachment, MAC: mac}, nil
}

// FindBridgedInterface resolves a host bridged interface by identifier or
// localized display name. An empty name selects the first available interface.
func FindBridgedInterface(name string) (*virtualization.VZBridgedNetworkInterface, bool) {
	interfaces := virtualization.VZBridgedNetworkInterfaceNetworkInterfaces()
	if interfaces == nil {
		return nil, false
	}
	count := objc.Send[uint](interfaces.Ptr(), objcutil.SelCount)
	for i := range count {
		id := objc.Send[objc.ID](interfaces.Ptr(), objcutil.SelObjectAtIndex, i)
		iface := virtualization.VZBridgedNetworkInterfaceFromID(pureobjc.Retain(id))
		if name == "" ||
			objcutil.GoStr(iface.Identifier()) == name ||
			objcutil.GoStr(iface.LocalizedDisplayName()) == name {
			return iface, true
		}
	}
	return nil, false
}

// BridgeInterfaces lists the available host bridged interfaces, each as
// "identifier (or \"Display Name\")", for error messages and `--net-bridged=list`.
func BridgeInterfaces() []string {
	var descriptions []string
	interfaces := virtualization.VZBridgedNetworkInterfaceNetworkInterfaces()
	if interfaces == nil {
		return nil
	}
	count := objc.Send[uint](interfaces.Ptr(), objcutil.SelCount)
	for i := range count {
		id := objc.Send[objc.ID](interfaces.Ptr(), objcutil.SelObjectAtIndex, i)
		iface := virtualization.VZBridgedNetworkInterfaceFromID(pureobjc.Retain(id))

		description := objcutil.GoStr(iface.Identifier())
		if displayName := objcutil.GoStr(iface.LocalizedDisplayName()); displayName != "" {
			description += " (or \"" + displayName + "\")"
		}
		descriptions = append(descriptions, description)
	}
	return descriptions
}
