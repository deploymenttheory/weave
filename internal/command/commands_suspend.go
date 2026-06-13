// Port of tart's Commands/Suspend.swift: signals the "weave run" process to
// suspend the VM via SIGUSR1.
//go:build darwin

package command

import (
	"context"
	"syscall"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/vmstorage"
)

// SuspendCommand ports the Suspend command (macOS 14+).
type SuspendCommand struct {
	Name string
}

func (c *SuspendCommand) Run(ctx context.Context) error {
	storage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return err
	}
	vmDir, err := storage.Open(c.Name)
	if err != nil {
		return err
	}
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

	// Tell the "weave run" process to suspend the VM.
	if err := syscall.Kill(int(pid), syscall.SIGUSR1); err != nil {
		return weaveerrors.ErrSuspendFailed("failed to send SIGUSR1 signal to the \"weave run\" process running VM \"" + c.Name + "\"")
	}
	return nil
}
