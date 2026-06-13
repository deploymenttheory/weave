// Port of tart's VNC/VNC.swift.
//go:build darwin

package vnc

import (
	"context"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
)

// IPNotFoundError ports Run.swift's IPNotFound error.
type IPNotFoundError struct{}

func (IPNotFoundError) Error() string { return "IP not found" }

// VNC ports tart's VNC protocol.
type VNC interface {
	WaitForURL(ctx context.Context, netBridged bool) (*foundation.NSURL, error)
	Stop() error
}
