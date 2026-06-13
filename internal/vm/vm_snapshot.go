// Snapshot (suspend/resume) support: bridges the macOS 14 save/restore
// machine-state APIs through manual blocks. Extracted from the run command
// when the monolith was split — methods on VM must live with the type.
//go:build darwin

package vm

import (
	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	dispatch "github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/cgo"
	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego"
)

// restoreMachineStateFrom / saveMachineStateTo bridge the macOS 14 snapshot
// APIs through manual blocks.
func (vm *VM) RestoreMachineStateFrom(url *foundation.NSURL) error {
	return vm.sendURLErrorCompletion("restoreMachineStateFromURL:completionHandler:", url)
}

func (vm *VM) SaveMachineStateTo(url *foundation.NSURL) error {
	return vm.sendURLErrorCompletion("saveMachineStateToURL:completionHandler:", url)
}

func (vm *VM) sendURLErrorCompletion(selector string, url *foundation.NSURL) error {
	errCh := make(chan error, 1)
	block := purego.NewBlock(func(_ purego.Block, errID purego.ID) {
		if errID != 0 {
			errCh <- purego.NSErrorToError(errID)
		} else {
			errCh <- nil
		}
	})
	dispatch.RunOnMainThread(func() {
		vm.VirtualMachine.Ptr().Send(purego.RegisterName(selector), url.Ptr(), block)
	})
	return <-errCh
}
