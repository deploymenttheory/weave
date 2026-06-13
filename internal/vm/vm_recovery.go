// Port of tart's VM+Recovery.swift: starting a macOS VM directly into
// recovery using the private _VZVirtualMachineStartOptions API (kudos to
// @saagarjha's VirtualApple). Swift's Dynamic package becomes plain ObjC
// runtime sends.
//go:build darwin

package vm

import (
	"github.com/ebitengine/purego/objc"

	dispatch "github.com/deploymenttheory/go-bindings-macosplatform/internal/objc"
	"github.com/deploymenttheory/go-bindings-macosplatform/internal/pureobjc"
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
	block := objc.NewBlock(func(_ objc.Block, resultID objc.ID) {
		if resultID != 0 && objc.Send[bool](resultID, objc.RegisterName("isKindOfClass:"), objc.GetClass("NSError")) {
			errCh <- pureobjc.NSErrorToError(resultID)
		} else {
			errCh <- nil
		}
	})

	dispatch.RunOnMainThread(func() {
		options := objc.ID(objc.GetClass("_VZVirtualMachineStartOptions")).Send(objc.RegisterName("new"))
		options.Send(objc.RegisterName("setBootMacOSRecovery:"), recovery)
		vm.VirtualMachine.Ptr().Send(
			objc.RegisterName("_startWithOptions:completionHandler:"), options, block)
	})

	return <-errCh
}
