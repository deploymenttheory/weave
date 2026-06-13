// Port of tart's Commands/FQN.swift.
//go:build darwin

package command

import (
	"context"
	"fmt"

	"github.com/deploymenttheory/weave/internal/oci"
	"github.com/deploymenttheory/weave/internal/vmstorage"
)

// FQNCommand ports the FQN command.
type FQNCommand struct {
	Name string
}

func (c *FQNCommand) Run(ctx context.Context) error {
	if remoteName, err := oci.NewRemoteName(c.Name); err == nil {
		storage, err := vmstorage.NewVMStorageOCI()
		if err != nil {
			return err
		}
		digest, err := storage.Digest(remoteName)
		if err != nil {
			return err
		}

		remoteName.Reference = oci.NewDigestReference(digest)
		fmt.Println(remoteName)
	} else {
		fmt.Println(c.Name)
	}
	return nil
}
