// Port of lume's Commands/Images.swift, upgraded to a remote listing: lume
// lists its local image cache, whereas this command queries the registry's
// tags-list endpoint (GET /v2/<name>/tags/list) for the given repository.
// Note that org-wide listing is not possible against ghcr.io via the OCI
// distribution API (there is no per-organization _catalog endpoint).
//go:build darwin

package command

import (
	"context"
	"fmt"

	weaveconfig "github.com/deploymenttheory/weave/internal/config"
	weaveregistry "github.com/deploymenttheory/weave/internal/registry"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
)

// ImagesCommand lists the tags available for a remote repository.
type ImagesCommand struct {
	Repository string // <host>/<namespace>, or <repo> with --registry/default profile
	Registry   string // registry profile name
	Insecure   bool
	Quiet      bool
}

func (c *ImagesCommand) Run(ctx context.Context) error {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}
	client, remoteName, err := weaveregistry.Resolve(c.Repository+":latest", c.Registry, c.Insecure, settings)
	if err != nil {
		return weaveerrors.ErrFailedToParseRemoteName(fmt.Sprintf("%v", err))
	}

	tags, err := client.TagsList(ctx)
	if err != nil {
		return err
	}

	for _, tag := range tags {
		if c.Quiet {
			fmt.Println(tag)
		} else {
			fmt.Printf("%s/%s:%s\n", remoteName.Host, remoteName.Namespace, tag)
		}
	}
	return nil
}
