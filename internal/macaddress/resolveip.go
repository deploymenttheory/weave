// IP resolution strategies for a VM's MAC address (ported from tart's
// Commands/IP.swift resolveIP): DHCP leases, the ARP cache, or the guest
// agent over the VM's control socket.
//go:build darwin

package macaddress

import (
	"context"
	"net/netip"
	"time"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/objcutil"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
)

// IPResolutionStrategy mirrors the enum of the same name.
type IPResolutionStrategy string

const (
	IPResolutionStrategyDHCP  IPResolutionStrategy = "dhcp"
	IPResolutionStrategyARP   IPResolutionStrategy = "arp"
	IPResolutionStrategyAgent IPResolutionStrategy = "agent"
)

// ParseIPResolutionStrategy ports the ExpressibleByArgument conformance.
func ParseIPResolutionStrategy(argument string) (IPResolutionStrategy, bool) {
	switch IPResolutionStrategy(argument) {
	case IPResolutionStrategyDHCP, IPResolutionStrategyARP, IPResolutionStrategyAgent:
		return IPResolutionStrategy(argument), true
	default:
		return "", false
	}
}

// ResolveIP ports IP.resolveIP(_:resolutionStrategy:secondsToWait:
// controlSocketURL:).
func ResolveIP(ctx context.Context, vmMACAddress MACAddress, resolutionStrategy IPResolutionStrategy,
	secondsToWait uint16, controlSocketURL *foundation.NSURL) (netip.Addr, bool, error) {
	waitUntil := time.Now().Add(time.Duration(secondsToWait) * time.Second)

	for {
		switch resolutionStrategy {
		case IPResolutionStrategyARP:
			cache, err := NewARPCache()
			if err != nil {
				return netip.Addr{}, false, err
			}
			ip, found, err := cache.ResolveMACAddress(vmMACAddress)
			if err != nil {
				return netip.Addr{}, false, err
			}
			if found {
				return ip, true, nil
			}
		case IPResolutionStrategyDHCP:
			leases, err := NewLeases()
			if err != nil {
				return netip.Addr{}, false, err
			}
			if leases != nil {
				if ip, found := leases.ResolveMACAddress(vmMACAddress); found {
					return ip, true, nil
				}
			}
		case IPResolutionStrategyAgent:
			if controlSocketURL == nil {
				return netip.Addr{}, false, weaveerrors.ErrGeneric("Cannot perform IP resolution via Tart Guest Agent when control socket URL is not set")
			}

			// Change the current working directory to the VM's base
			// directory to work around the 104-byte Unix domain socket path
			// limitation.
			if baseURL := controlSocketURL.BaseURL(); baseURL != nil {
				foundation.NSFileManagerDefaultManager().ChangeCurrentDirectoryPath(baseURL.Path())
			}

			ip, found, err := AgentResolverResolveIP(objcutil.GoStr(controlSocketURL.RelativePath()))
			if err != nil {
				return netip.Addr{}, false, err
			}
			if found {
				return ip, true, nil
			}
		}

		if !time.Now().Before(waitUntil) {
			return netip.Addr{}, false, nil
		}

		// Wait a second.
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			return netip.Addr{}, false, ctx.Err()
		}
	}
}
