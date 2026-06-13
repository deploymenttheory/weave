// Package client is the host side of the weave guest agent. It deploys the
// embedded weave-guestd binary into a guest, launches it over a long-lived SSH
// stdio channel, and exposes the framed transport so feature clients (the
// clipboard engine today) can drive their module. It is darwin-only: weave runs
// on macOS hosts.
//go:build darwin

package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/deploymenttheory/weave/internal/guestagent/agent"
	"github.com/deploymenttheory/weave/internal/guestagent/agentbin"
	"github.com/deploymenttheory/weave/internal/guestagent/proto"
	weavessh "github.com/deploymenttheory/weave/internal/ssh"
)

// DefaultRemotePath is where the agent binary is deployed in the guest.
const DefaultRemotePath = "/tmp/weave-guestd"

// Options configures a Dial.
type Options struct {
	// GOOS/GOARCH select the embedded agent binary for the guest (e.g.
	// "darwin"/"arm64", "linux"/"amd64").
	GOOS, GOARCH string
	// RemotePath overrides DefaultRemotePath.
	RemotePath string
}

// Client is a live connection to a guest agent. It serialises exchanges with a
// mutex so a single module's request/response pairs do not interleave on the
// shared stdio channel.
type Client struct {
	ssh     *weavessh.SSHClient
	session *weavessh.AgentSession
	in      *bufio.Reader
	out     *bufio.Writer

	mu sync.Mutex

	// Hello is the agent's handshake identity.
	Hello agent.Hello
}

// Dial deploys (if needed) and launches the guest agent, returning a connected
// client. It first tries to launch an already-resident binary and verify its
// version via the handshake; on absence or version mismatch it uploads the
// embedded binary and relaunches. The connection is torn down when ctx is
// cancelled.
func Dial(ctx context.Context, ssh *weavessh.SSHClient, opts Options) (*Client, error) {
	remotePath := opts.RemotePath
	if remotePath == "" {
		remotePath = DefaultRemotePath
	}

	// Fast path: an up-to-date agent is already deployed.
	if c, err := launch(ctx, ssh, remotePath); err == nil {
		if hello, herr := c.handshake(); herr == nil && hello.Version == agent.Version {
			c.Hello = hello
			return c, nil
		}
		_ = c.Close()
	}

	// Deploy the embedded binary, then launch.
	binary, ok := agentbin.Binary(opts.GOOS, opts.GOARCH)
	if !ok {
		return nil, fmt.Errorf("guestagent: no embedded agent for %s/%s", opts.GOOS, opts.GOARCH)
	}
	if err := ssh.Upload(ctx, bytes.NewReader(binary), remotePath, 0o755); err != nil {
		return nil, fmt.Errorf("guestagent: deploy: %w", err)
	}

	c, err := launch(ctx, ssh, remotePath)
	if err != nil {
		return nil, err
	}
	hello, err := c.handshake()
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("guestagent: handshake: %w", err)
	}
	c.Hello = hello
	return c, nil
}

func launch(ctx context.Context, ssh *weavessh.SSHClient, remotePath string) (*Client, error) {
	session, err := ssh.StartAgent(ctx, remotePath)
	if err != nil {
		return nil, err
	}
	return &Client{
		ssh:     ssh,
		session: session,
		in:      proto.NewBufferedReader(session.Stdout),
		out:     proto.NewBufferedWriter(session.Stdin),
	}, nil
}

// Close terminates the agent and its connection.
func (c *Client) Close() error {
	if c.session != nil {
		return c.session.Close()
	}
	return nil
}

// Lock/Unlock guard a multi-frame exchange on the shared channel. Writer and
// Reader give the caller the framed streams to drive its module's protocol.
func (c *Client) Lock()                 { c.mu.Lock() }
func (c *Client) Unlock()               { c.mu.Unlock() }
func (c *Client) Writer() *bufio.Writer { return c.out }
func (c *Client) Reader() *bufio.Reader { return c.in }

func (c *Client) handshake() (agent.Hello, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := proto.WriteRequest(c.out, proto.Request{Module: agent.ModuleName, Op: agent.OpHello}); err != nil {
		return agent.Hello{}, err
	}
	resp, err := proto.ReadResponse(c.in)
	if err != nil {
		return agent.Hello{}, err
	}
	if resp.Err != "" {
		return agent.Hello{}, fmt.Errorf("%s", resp.Err)
	}
	var hello agent.Hello
	if err := json.Unmarshal(resp.Meta, &hello); err != nil {
		return agent.Hello{}, err
	}
	return hello, nil
}
