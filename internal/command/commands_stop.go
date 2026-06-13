// Port of tart's Commands/Stop.swift.
//go:build darwin

package command

import (
	"context"
	"syscall"
	"time"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/vmdirectory"
	"github.com/deploymenttheory/weave/internal/vmstorage"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
)

// StopCommand ports the Stop command.
type StopCommand struct {
	Name    string
	Timeout uint64 // seconds to wait for graceful termination
}

func (c *StopCommand) Run(ctx context.Context) error {
	storage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return err
	}
	vmDir, err := storage.Open(c.Name)
	if err != nil {
		return err
	}

	state, err := vmDir.State()
	if err != nil {
		return err
	}
	switch state {
	case vmdirectory.VMDirectoryStateSuspended:
		return c.stopSuspended(vmDir)
	case vmdirectory.VMDirectoryStateRunning:
		return c.stopRunning(ctx, vmDir)
	default:
		return weaveerrors.ErrVMNotRunning(c.Name)
	}
}

func (c *StopCommand) stopSuspended(vmDir *vmdirectory.VMDirectory) error {
	_, _ = foundation.NSFileManagerDefaultManager().RemoveItemAtURLError(vmDir.StateURL())
	return nil
}

func (c *StopCommand) stopRunning(ctx context.Context, vmDir *vmdirectory.VMDirectory) error {
	lock, err := vmDir.Lock()
	if err != nil {
		return err
	}
	defer lock.Close()

	// Find the VM's PID.
	pid, err := lock.PID()
	if err != nil {
		return err
	}
	if pid == 0 {
		return weaveerrors.ErrVMNotRunning(c.Name)
	}

	// Try to gracefully terminate the VM. The return code is deliberately
	// not checked: the VM may already be shutting down and we'd hit a race.
	_ = syscall.Kill(int(pid), syscall.SIGINT)

	// Ensure that the VM has terminated.
	gracefulWait := time.Duration(c.Timeout) * time.Second
	const gracefulTick = 100 * time.Millisecond

	for gracefulWait > 0 {
		pid, err = lock.PID()
		if err != nil {
			return err
		}
		if pid == 0 {
			return nil
		}

		select {
		case <-time.After(gracefulTick):
		case <-ctx.Done():
			return ctx.Err()
		}
		gracefulWait -= gracefulTick
	}

	// Seems that the VM is still running; proceed with forceful termination.
	if err := syscall.Kill(int(pid), syscall.SIGKILL); err != nil {
		return weaveerrors.ErrVMTerminationFailed("failed to forcefully terminate the VM \"%s\": %v", c.Name, err)
	}
	return nil
}
