// Port of lume's Commands/Serve.swift: start the HTTP API server, or the
// MCP stdio server with --mcp.
//go:build darwin

package mcp

import (
	"context"
)

// ServeCommand ports the serve command.
type ServeCommand struct {
	Port uint16
	MCP  bool
}

func (c *ServeCommand) Run(ctx context.Context) error {
	if c.MCP {
		return RunMCPServer(ctx)
	}
	return NewAPIServer(c.Port).Run(ctx)
}
