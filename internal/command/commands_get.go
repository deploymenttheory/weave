// Port of tart's Commands/Get.swift.
//go:build darwin

package command

import (
	"context"
	"fmt"

	"github.com/deploymenttheory/weave/internal/vmconfig"

	weaveplatform "github.com/deploymenttheory/weave/internal/platform"
	"github.com/deploymenttheory/weave/internal/vmstorage"
)

type getVMInfo struct {
	OS         weaveplatform.OS
	CPU        int
	Memory     uint64
	Disk       int
	DiskFormat string
	Size       string
	Display    string
	Running    bool
	State      string
}

// GetCommand ports the Get command.
type GetCommand struct {
	Name   string
	Format Format
}

func (c *GetCommand) Run(ctx context.Context) error {
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

	diskGB, err := vmDir.SizeGB()
	if err != nil {
		return err
	}
	allocatedBytes, err := vmDir.AllocatedSizeBytes()
	if err != nil {
		return err
	}
	running, err := vmDir.Running()
	if err != nil {
		return err
	}
	state, err := vmDir.State()
	if err != nil {
		return err
	}

	info := getVMInfo{
		OS:         vmConfig.OS,
		CPU:        vmConfig.CPUCount,
		Memory:     vmConfig.MemorySize / 1024 / 1024,
		Disk:       diskGB,
		DiskFormat: string(vmConfig.DiskFormat),
		Size:       fmt.Sprintf("%.3f", float32(allocatedBytes)/1000/1000/1000),
		Display:    vmConfig.Display.String(),
		Running:    running,
		State:      string(state),
	}
	fmt.Println(c.Format.RenderSingle(info))
	return nil
}
