// Port of lume's SSH/SSHClient.swift on top of golang.org/x/crypto/ssh
// instead of SwiftNIO SSH. Semantics preserved: password auth with a single
// attempt, all host keys accepted (VM-internal use only), 30-second dial
// timeout, combined stdout+stderr output, and a missing exit status treated
// as success (some servers close the channel before sending it).
//go:build darwin

package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/telemetry"
	"github.com/deploymenttheory/weave/internal/terminal"

	"golang.org/x/crypto/ssh"
)

const sshDialTimeout = 30 * time.Second

// SSHResult ports lume's SSHResult.
type SSHResult struct {
	ExitCode int32
	Output   string
}

// SSHClient ports lume's SSHClient actor.
type SSHClient struct {
	Host     string
	Port     uint16
	User     string
	Password string
}

func NewSSHClient(host string, port uint16, user string, password string) *SSHClient {
	return &SSHClient{Host: host, Port: port, User: user, Password: password}
}

func (c *SSHClient) clientConfig() *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User: c.User,
		Auth: []ssh.AuthMethod{ssh.Password(c.Password)},
		// Accept all host keys for internal VM connections, matching lume's
		// AcceptAllHostKeysDelegate. Not for use beyond local VMs.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         sshDialTimeout,
	}
}

// connect dials the SSH server, classifying failures into SSHError kinds so
// callers can decide whether the system-ssh fallback applies.
func (c *SSHClient) connect(ctx context.Context) (*ssh.Client, error) {
	ctx, span := otel.Tracer("weave").Start(ctx, "ssh.connect",
		trace.WithAttributes(
			attribute.String("ssh.host", c.Host),
			attribute.Int("ssh.port", int(c.Port)),
			attribute.String("ssh.user", c.User),
		))
	defer span.End()
	telemetry.OTelShared().Instruments.SSHConnections.Add(ctx, 1,
		metric.WithAttributes(attribute.String("ssh.host", c.Host)))

	address := net.JoinHostPort(c.Host, fmt.Sprintf("%d", c.Port))

	dialer := net.Dialer{Timeout: sshDialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, ErrSSHConnectionFailed(err.Error())
	}

	sshConn, channels, requests, err := ssh.NewClientConn(conn, address, c.clientConfig())
	if err != nil {
		_ = conn.Close()
		if strings.Contains(err.Error(), "unable to authenticate") {
			return nil, ErrSSHAuthenticationFailed()
		}
		return nil, ErrSSHConnectionFailed(err.Error())
	}

	return ssh.NewClient(sshConn, channels, requests), nil
}

// Execute runs a command on the remote host and returns its combined
// stdout+stderr output and exit code. A zero timeout means no timeout.
func (c *SSHClient) Execute(ctx context.Context, command string, timeout time.Duration) (SSHResult, error) {
	ctx, span := otel.Tracer("weave").Start(ctx, "ssh.exec",
		trace.WithAttributes(
			attribute.String("ssh.host", c.Host),
			attribute.String("ssh.command", command),
		))
	defer span.End()

	client, err := c.connect(ctx)
	if err != nil {
		return SSHResult{}, err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return SSHResult{}, ErrSSHConnectionFailed(err.Error())
	}
	defer session.Close()

	var output bytes.Buffer
	session.Stdout = &output
	session.Stderr = &output

	// x/crypto sessions have no context support: enforce the timeout (and
	// context cancellation) by closing the client, which unblocks Wait.
	var timedOut atomic.Bool
	watchCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		watchCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-watchCtx.Done():
			timedOut.Store(true)
			_ = client.Close()
		case <-watchDone:
		}
	}()

	err = session.Run(command)
	if timedOut.Load() {
		return SSHResult{}, ErrSSHTimeout()
	}
	if err != nil {
		var exitErr *ssh.ExitError
		var missingErr *ssh.ExitMissingError
		switch {
		case errors.As(err, &exitErr):
			return SSHResult{ExitCode: int32(exitErr.ExitStatus()), Output: output.String()}, nil
		case errors.As(err, &missingErr):
			// Channel closed without an exit status: assume success, as
			// lume's CommandExecHandler does.
			return SSHResult{ExitCode: 0, Output: output.String()}, nil
		default:
			return SSHResult{}, ErrSSHCommandFailed(-1, err.Error())
		}
	}

	return SSHResult{ExitCode: 0, Output: output.String()}, nil
}

// shellSingleQuote wraps s in single quotes for safe interpolation into a
// remote /bin/sh command line.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Upload streams r into the file remotePath on the remote host and sets its
// permission bits. Used to deploy the clipboard agent binary into the guest; a
// single SSH session pipes the bytes through `cat >` and chmods the result.
func (c *SSHClient) Upload(ctx context.Context, r io.Reader, remotePath string, mode os.FileMode) error {
	client, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return ErrSSHConnectionFailed(err.Error())
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		return ErrSSHConnectionFailed(err.Error())
	}

	quoted := shellSingleQuote(remotePath)
	command := fmt.Sprintf("cat > %s && chmod %o %s", quoted, mode.Perm(), quoted)
	if err := session.Start(command); err != nil {
		return ErrSSHCommandFailed(-1, err.Error())
	}

	// Close the session if the context is cancelled mid-copy.
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Close()
		case <-watchDone:
		}
	}()

	if _, err := io.Copy(stdin, r); err != nil {
		_ = stdin.Close()
		return ErrSSHCommandFailed(-1, err.Error())
	}
	if err := stdin.Close(); err != nil {
		return ErrSSHCommandFailed(-1, err.Error())
	}
	if err := session.Wait(); err != nil {
		var missingErr *ssh.ExitMissingError
		if !errors.As(err, &missingErr) {
			return ErrSSHCommandFailed(-1, err.Error())
		}
	}
	return nil
}

// AgentSession is a long-lived remote command with its stdin/stdout exposed for
// a request/response protocol (the clipboard agent). Close terminates the
// remote process and its connection; it is also closed automatically when the
// context passed to StartAgent is cancelled.
type AgentSession struct {
	Stdin  io.WriteCloser
	Stdout io.Reader

	client    *ssh.Client
	session   *ssh.Session
	closeOnce sync.Once
}

// Close tears down the agent session and its underlying connection.
func (s *AgentSession) Close() error {
	s.closeOnce.Do(func() {
		_ = s.session.Close()
		_ = s.client.Close()
	})
	return nil
}

// StartAgent launches command on the remote host as a resident process and
// returns its piped stdin/stdout. Unlike Execute it does not wait for the
// process to exit; the caller drives it via the returned pipes and calls Close
// when done. Remote stderr is discarded (the agent reports errors in-band).
func (c *SSHClient) StartAgent(ctx context.Context, command string) (*AgentSession, error) {
	client, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}

	session, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, ErrSSHConnectionFailed(err.Error())
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, ErrSSHConnectionFailed(err.Error())
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, ErrSSHConnectionFailed(err.Error())
	}
	session.Stderr = io.Discard

	if err := session.Start(command); err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, ErrSSHCommandFailed(-1, err.Error())
	}

	agent := &AgentSession{Stdin: stdin, Stdout: stdout, client: client, session: session}
	go func() {
		<-ctx.Done()
		_ = agent.Close()
	}()
	return agent, nil
}

// Interactive starts an interactive shell session with a PTY, passing the
// local terminal through in raw mode and propagating window-size changes.
func (c *SSHClient) Interactive(ctx context.Context) error {
	client, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return ErrSSHConnectionFailed(err.Error())
	}
	defer session.Close()

	session.Stdin = os.Stdin
	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	width, height := uint16(80), uint16(24)
	if terminal.TermIsTerminal() {
		if w, h, err := terminal.TermGetSize(); err == nil {
			width, height = w, h
		}

		state, err := terminal.TermMakeRaw()
		if err != nil {
			return err
		}
		defer func() { _ = terminal.TermRestore(state) }()
	}

	modes := ssh.TerminalModes{}
	if err := session.RequestPty("xterm-256color", int(height), int(width), modes); err != nil {
		return ErrSSHConnectionFailed(err.Error())
	}

	// Propagate local window resizes to the remote PTY.
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			if w, h, err := terminal.TermGetSize(); err == nil {
				_ = session.WindowChange(int(h), int(w))
			}
		}
	}()

	if err := session.Shell(); err != nil {
		return ErrSSHConnectionFailed(err.Error())
	}

	// Close the session when the context is cancelled (e.g. SIGINT handling
	// in main.go), otherwise wait for the remote shell to exit.
	waitDone := make(chan error, 1)
	go func() { waitDone <- session.Wait() }()
	select {
	case <-ctx.Done():
		_ = session.Close()
		return ctx.Err()
	case err := <-waitDone:
		var exitErr *ssh.ExitError
		var missingErr *ssh.ExitMissingError
		if err == nil || errors.As(err, &missingErr) {
			return nil
		}
		if errors.As(err, &exitErr) {
			if code := int32(exitErr.ExitStatus()); code != 0 {
				return &weaveerrors.ExecCustomExitCodeError{Code: code}
			}
			return nil
		}
		return ErrSSHConnectionFailed(err.Error())
	}
}
