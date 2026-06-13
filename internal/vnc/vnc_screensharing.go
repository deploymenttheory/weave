// Port of tart's VNC/ScreenSharingVNC.swift: points a vnc:// URL at the
// VM's resolved IP, to be opened with macOS Screen Sharing.
//go:build darwin

package vnc

import (
	"context"

	"github.com/deploymenttheory/weave/internal/vmconfig"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/macaddress"
	"github.com/deploymenttheory/weave/internal/objcutil"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
)

// ScreenSharingVNC ports tart's ScreenSharingVNC class.
type ScreenSharingVNC struct {
	VMConfig *vmconfig.VMConfig
}

var _ VNC = (*ScreenSharingVNC)(nil)

func NewScreenSharingVNC(vmConfig *vmconfig.VMConfig) *ScreenSharingVNC {
	return &ScreenSharingVNC{VMConfig: vmConfig}
}

func (v *ScreenSharingVNC) WaitForURL(ctx context.Context, netBridged bool) (*foundation.NSURL, error) {
	vmMACAddress, ok := macaddress.NewMACAddress(objcutil.GoStr(v.VMConfig.MACAddress.String()))
	if !ok {
		return nil, weaveerrors.ErrGeneric("failed to parse VM's MAC address")
	}

	strategy := macaddress.IPResolutionStrategyDHCP
	if netBridged {
		strategy = macaddress.IPResolutionStrategyARP
	}
	ip, found, err := macaddress.ResolveIP(ctx, vmMACAddress, strategy, 60, nil)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, IPNotFoundError{}
	}

	return foundation.NSURLURLWithString(objcutil.NSStr("vnc://" + ip.String())), nil
}

func (v *ScreenSharingVNC) Stop() error {
	// Nothing to do.
	return nil
}
