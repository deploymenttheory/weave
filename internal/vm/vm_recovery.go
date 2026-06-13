// Port of tart's VM+Recovery.swift: starting a macOS VM directly into
// recovery using the private _VZVirtualMachineStartOptions API (kudos to
// @saagarjha's VirtualApple). Swift's Dynamic package becomes plain ObjC
// runtime sends.
//go:build darwin

package vm

import (
	dispatch "github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/cgo"
	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego"
)

// startMachineWithRecoveryPrivateAPI ports the VZVirtualMachine.start(_
// recovery:) extension: the macOS 12 era private-API fallback for booting
// into recovery (the public VZMacOSVirtualMachineStartOptions path in
// startMachine supersedes it on macOS 13+).
func (vm *VM) startMachineWithRecoveryPrivateAPI(recovery bool) error {
	if !recovery {
		// Just use the regular API.
		return vm.SendErrorCompletion("startWithCompletionHandler:")
	}

	errCh := make(chan error, 1)
	// The private completion handler receives `Any? result`, which is an
	// NSError on failure and nil on success.
	block := purego.NewBlock(func(_ purego.Block, resultID purego.ID) {
		if resultID != 0 && purego.Send[bool](resultID, purego.RegisterName("isKindOfClass:"), purego.GetClass("NSError")) {
			errCh <- purego.NSErrorToError(resultID)
		} else {
			errCh <- nil
		}
	})

	dispatch.RunOnMainThread(func() {
		options := purego.ID(purego.GetClass("_VZVirtualMachineStartOptions")).Send(purego.RegisterName("new"))
		options.Send(purego.RegisterName("setBootMacOSRecovery:"), recovery)
		vm.VirtualMachine.Ptr().Send(
			purego.RegisterName("_startWithOptions:completionHandler:"), options, block)
	})

	return <-errCh
}
