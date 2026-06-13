// Port of tart's Commands/Pull.swift, extended with registry-profile
// resolution: fully-qualified references work as always, bare names resolve
// against the default profile, and --registry selects a named one.
//go:build darwin

package command

import (
	"context"
	"fmt"

	weaveconfig "github.com/deploymenttheory/weave/internal/config"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/logging"
	"github.com/deploymenttheory/weave/internal/registry"
	"github.com/deploymenttheory/weave/internal/vmstorage"
)

// PullCommand ports the Pull command.
type PullCommand struct {
	RemoteName  string
	Registry    string // registry profile name; empty: resolution rules apply
	Insecure    bool
	Concurrency uint
	Deduplicate bool
}

func (c *PullCommand) Validate() error {
	if c.Concurrency < 1 {
		return weaveerrors.ErrGeneric("network concurrency cannot be less than 1")
	}
	return nil
}

func (c *PullCommand) Run(ctx context.Context) error {
	// Be more liberal when accepting a local image as the argument
	// (cirruslabs/tart#36) — but an explicit --registry always means remote.
	if c.Registry == "" {
		localStorage, err := vmstorage.NewVMStorageLocal()
		if err != nil {
			return err
		}
		if localStorage.Exists(c.RemoteName) {
			fmt.Printf("%q is a local image, nothing to pull here!\n", c.RemoteName)
			return nil
		}
	}

	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}
	client, remoteName, err := registry.Resolve(c.RemoteName, c.Registry, c.Insecure, settings)
	if err != nil {
		return err
	}

	logging.DefaultLogger().AppendNewLine(fmt.Sprintf("pulling %s...", remoteName))

	ociStorage, err := vmstorage.NewVMStorageOCI()
	if err != nil {
		return err
	}
	return ociStorage.Pull(ctx, remoteName, client, c.Concurrency, c.Deduplicate)
}
