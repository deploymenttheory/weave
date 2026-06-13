// Port of tart's Commands/Logout.swift.
//go:build darwin

package command

import (
	"context"

	"github.com/deploymenttheory/weave/internal/credentials"
)

// LogoutCommand ports the Logout command.
type LogoutCommand struct {
	Host string
}

func (c *LogoutCommand) Run(ctx context.Context) error {
	return (&credentials.KeychainCredentialsProvider{}).Remove(c.Host)
}
