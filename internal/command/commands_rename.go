// Port of tart's Commands/Rename.swift.
//go:build darwin

package command

import (
	"context"
	"strings"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/vmstorage"
)

// RenameCommand ports the Rename command.
type RenameCommand struct {
	Name    string
	NewName string
}

func (c *RenameCommand) Validate() error {
	if strings.Contains(c.NewName, "/") {
		return weaveerrors.ErrGeneric("<new-name> should be a local name")
	}
	return nil
}

func (c *RenameCommand) Run(ctx context.Context) error {
	localStorage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return err
	}

	if !localStorage.Exists(c.Name) {
		return weaveerrors.ErrGeneric("failed to rename a non-existent local VM: %s", c.Name)
	}

	if localStorage.Exists(c.NewName) {
		return weaveerrors.ErrGeneric("failed to rename VM %s, target VM %s already exists, delete it first!", c.Name, c.NewName)
	}

	return localStorage.Rename(c.Name, c.NewName)
}
