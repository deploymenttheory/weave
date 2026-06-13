// Port of tart's Commands/Delete.swift.
//go:build darwin

package command

import (
	"context"

	"github.com/deploymenttheory/weave/internal/vmstorage"
)

// DeleteCommand ports the Delete command.
type DeleteCommand struct {
	Names []string
}

func (c *DeleteCommand) Run(ctx context.Context) error {
	for _, name := range c.Names {
		if err := vmstorage.VMStorageHelperDelete(name); err != nil {
			return err
		}
	}
	return nil
}
