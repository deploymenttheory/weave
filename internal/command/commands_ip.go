// Port of tart's Commands/IP.swift.
//go:build darwin

package command

import (
	"context"
	"fmt"

	"github.com/deploymenttheory/weave/internal/vmconfig"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/macaddress"
	"github.com/deploymenttheory/weave/internal/objcutil"
	weaveplatform "github.com/deploymenttheory/weave/internal/platform"
	"github.com/deploymenttheory/weave/internal/vmstorage"
)

// IPCommand ports the IP command.
type IPCommand struct {
	Name     string
	Wait     uint16
	Resolver macaddress.IPResolutionStrategy
}

func (c *IPCommand) Run(ctx context.Context) error {
	storage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return err
	}
	vmDir, err := storage.Open(c.Name)
	if err != nil {
		return err
	}
	vmConfig, err := vmconfig.NewVMConfigFromURL(vmDir.ConfigURL())
	if err != nil {
		return err
	}
	vmMACAddress, ok := macaddress.NewMACAddress(objcutil.GoStr(vmConfig.MACAddress.String()))
	if !ok {
		return weaveerrors.ErrGeneric("failed to parse VM's MAC address")
	}

	ip, found, err := macaddress.ResolveIP(ctx, vmMACAddress, c.Resolver, c.Wait, vmDir.ControlSocketURL())
	if err != nil {
		return err
	}
	if !found {
		message := "no IP address found"

		if running, err := vmDir.Running(); err == nil && !running {
			message += ", is your VM running?"
		}

		if c.Resolver == macaddress.IPResolutionStrategyAgent {
			message += " (also make sure that Guest agent for Tart is running inside of a VM)"
		} else if vmConfig.OS == weaveplatform.OSLinux && c.Resolver == macaddress.IPResolutionStrategyARP {
			message += " (not all Linux distributions are compatible with the ARP resolver)"
		}

		return weaveerrors.ErrNoIPAddressFound("%s", message)
	}

	fmt.Println(ip)
	return nil
}
