// Weave's networking model. A VM's networking is a Topology: an ordered list
// of NICs, each carrying its own VZ attachment, MAC address, and optional
// lifecycle engine (softnet helper process, vmnet network). This supersedes
// tart's single Network-per-VM with a shared MAC and enables multi-NIC
// topologies with mixed isolation.
//
// The AsyncSemaphore is the Go counterpart of tart's swift-async-algorithms
// AsyncSemaphore(value: 0), used to coordinate VM shutdown when a networking
// engine (e.g. softnet) exits.
//go:build darwin

package network

import (
	"context"

	virtualization "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/virtualization"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/vmconfig"
)

// NIC is a single resolved virtual NIC ready to be wired into a VM
// configuration: a VZ network attachment plus the MAC address to assign to it.
type NIC struct {
	Attachment *virtualization.VZNetworkDeviceAttachment
	MAC        *virtualization.VZMACAddress

	// engine drives any out-of-band lifecycle for this NIC (softnet helper,
	// vmnet network). nil for attachment-only modes (nat, bridged).
	engine nicEngine
}

// nicEngine is the per-NIC lifecycle hook. run is called when the VM starts and
// may signal sema when the engine exits (which triggers VM shutdown); stop is
// called when the VM stops.
type nicEngine interface {
	run(sema *AsyncSemaphore) error
	stop() error
}

// Topology is a VM's full set of NICs plus their aggregate lifecycle.
type Topology struct {
	nics []NIC
}

// NICs returns the resolved NICs in order. The first/primary NIC's MAC is the
// one used for guest IP resolution.
func (t *Topology) NICs() []NIC { return t.nics }

// Run starts every NIC engine. On the first failure it stops the engines that
// already started and returns the error.
func (t *Topology) Run(sema *AsyncSemaphore) error {
	for i := range t.nics {
		engine := t.nics[i].engine
		if engine == nil {
			continue
		}
		if err := engine.run(sema); err != nil {
			for j := 0; j < i; j++ {
				if e := t.nics[j].engine; e != nil {
					_ = e.stop()
				}
			}
			return err
		}
	}
	return nil
}

// Stop stops every NIC engine, returning the first error encountered while
// still attempting to stop the rest.
func (t *Topology) Stop() error {
	var first error
	for i := range t.nics {
		if engine := t.nics[i].engine; engine != nil {
			if err := engine.stop(); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

// BuildTopology resolves a list of NICConfigs into a runnable Topology,
// constructing the VZ attachment, MAC, and lifecycle engine for each NIC.
// Secondary NICs with an empty MAC are assigned a deterministic
// locally-administered address derived from the primary MAC.
func BuildTopology(nics []vmconfig.NICConfig) (*Topology, error) {
	if len(nics) == 0 {
		return nil, weaveerrors.ErrGeneric("network topology has no NICs")
	}

	primaryMAC := nics[0].MACAddress
	for i := range nics {
		if nics[i].IsPrimary && nics[i].MACAddress != "" {
			primaryMAC = nics[i].MACAddress
			break
		}
	}

	topology := &Topology{nics: make([]NIC, 0, len(nics))}
	for i := range nics {
		nicConfig := nics[i]
		if nicConfig.MACAddress == "" {
			nicConfig.MACAddress = vmconfig.DeriveMACAddress(primaryMAC, i)
		}

		mac, err := macFromString(nicConfig.MACAddress)
		if err != nil {
			return nil, err
		}

		nic, err := buildNIC(nicConfig, mac)
		if err != nil {
			return nil, err
		}
		topology.nics = append(topology.nics, nic)
	}
	return topology, nil
}

// buildNIC dispatches to the per-mode builder.
func buildNIC(nicConfig vmconfig.NICConfig, mac *virtualization.VZMACAddress) (NIC, error) {
	switch nicConfig.Mode {
	case vmconfig.NICModeNAT:
		return buildNAT(mac)
	case vmconfig.NICModeBridged:
		return buildBridged(nicConfig, mac)
	case vmconfig.NICModeSoftnet:
		return buildSoftnetNIC(nicConfig, mac)
	case vmconfig.NICModeVmnet:
		return buildVmnetNIC(nicConfig, mac)
	default:
		return NIC{}, weaveerrors.ErrGeneric("unsupported NIC mode %q", string(nicConfig.Mode))
	}
}

// macFromString parses a MAC string into a VZMACAddress.
func macFromString(s string) (*virtualization.VZMACAddress, error) {
	mac := virtualization.VZMACAddressFromID(objcutil.AllocClass("VZMACAddress")).
		InitWithString(objcutil.NSStr(s))
	if mac == nil {
		return nil, weaveerrors.ErrGeneric("invalid MAC address %q", s)
	}
	return mac, nil
}

// AsyncSemaphore is the Go counterpart of the Semaphore package's
// AsyncSemaphore(value: 0) used to coordinate VM shutdown.
type AsyncSemaphore struct {
	signals chan struct{}
}

func NewAsyncSemaphore() *AsyncSemaphore {
	return &AsyncSemaphore{signals: make(chan struct{}, 64)}
}

// Signal increments the semaphore; it never blocks.
func (s *AsyncSemaphore) Signal() {
	select {
	case s.signals <- struct{}{}:
	default:
	}
}

// WaitUnlessCancelled blocks until the semaphore is signalled or ctx is
// cancelled (Swift: waitUnlessCancelled throwing CancellationError).
func (s *AsyncSemaphore) WaitUnlessCancelled(ctx context.Context) error {
	select {
	case <-s.signals:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
