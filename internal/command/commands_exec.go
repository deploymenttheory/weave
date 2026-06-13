// Port of tart's Commands/Exec.swift: executes a command in a running VM
// through the Tart Guest Agent's Exec streaming gRPC.
//go:build darwin

package command

import (
	"context"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/objcutil"
	weaveplatform "github.com/deploymenttheory/weave/internal/platform"
	"github.com/deploymenttheory/weave/internal/terminal"
	"github.com/deploymenttheory/weave/internal/vmstorage"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	"github.com/deploymenttheory/weave/internal/agentrpc"
)

// ExecCommand ports the Exec command.
type ExecCommand struct {
	Interactive bool
	TTY         bool
	Name        string
	Command     []string
}

func (c *ExecCommand) Run(ctx context.Context) error {
	if !weaveplatform.MacOSAtLeast(14) {
		return weaveerrors.ErrGeneric("\"weave exec\" is only available on macOS 14 (Sonoma) or newer")
	}

	// Open the VM's directory and ensure that the VM is running.
	storage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return err
	}
	vmDir, err := storage.Open(c.Name)
	if err != nil {
		return err
	}
	running, err := vmDir.Running()
	if err != nil {
		return err
	}
	if !running {
		return weaveerrors.ErrVMNotRunning(c.Name)
	}

	// Change the current working directory to the VM's base directory to
	// work around the 104-byte Unix domain socket path limitation.
	controlSocketURL := vmDir.ControlSocketURL()
	if baseURL := controlSocketURL.BaseURL(); baseURL != nil {
		foundation.NSFileManagerDefaultManager().ChangeCurrentDirectoryPath(baseURL.Path())
	}
	controlSocketPath := objcutil.GoStr(controlSocketURL.RelativePath())

	conn, err := grpc.NewClient("unix://"+controlSocketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return weaveerrors.ErrGeneric("Failed to connect to the VM using its control socket: %v, is the Tart Guest Agent running?", err)
	}
	defer conn.Close()

	// Switch the controlling terminal into raw mode when a remote
	// pseudo-terminal is requested.
	var state *terminal.TermState
	if c.TTY && terminal.TermIsTerminal() {
		state, err = terminal.TermMakeRaw()
		if err != nil {
			return err
		}
	}
	defer func() {
		// Restore the terminal to its initial state.
		if state != nil {
			_ = terminal.TermRestore(state)
		}
	}()

	if err := c.execute(ctx, conn); err != nil {
		return err
	}
	return nil
}

func (c *ExecCommand) execute(ctx context.Context, conn *grpc.ClientConn) error {
	execCall, err := agentrpc.NewAgentClient(conn).Exec(ctx)
	if err != nil {
		return weaveerrors.ErrGeneric("Failed to connect to the VM using its control socket: %v, is the Tart Guest Agent running?", err)
	}

	command := &agentrpc.ExecRequest_Command{
		Name:        c.Command[0],
		Args:        c.Command[1:],
		Interactive: c.Interactive,
		Tty:         c.TTY,
	}
	if c.TTY {
		if width, height, err := terminal.TermGetSize(); err == nil {
			command.TerminalSize = &agentrpc.TerminalSize{Cols: uint32(width), Rows: uint32(height)}
		}
	}
	var sendMu sync.Mutex
	send := func(request *agentrpc.ExecRequest) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return execCall.Send(request)
	}

	if err := send(&agentrpc.ExecRequest{
		Type: &agentrpc.ExecRequest_Command_{Command: command},
	}); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 3)

	// Stream the host's standard input if interactive mode is enabled.
	if c.Interactive {
		go func() {
			buffer := make([]byte, 64*1024)
			for {
				n, readErr := os.Stdin.Read(buffer)
				if n > 0 {
					if err := send(&agentrpc.ExecRequest{
						Type: &agentrpc.ExecRequest_StandardInput{
							StandardInput: &agentrpc.IOChunk{Data: append([]byte(nil), buffer[:n]...)},
						},
					}); err != nil {
						errCh <- err
						return
					}
				}
				if readErr != nil {
					// Signal EOF as we're done reading standard input.
					_ = send(&agentrpc.ExecRequest{
						Type: &agentrpc.ExecRequest_StandardInput{
							StandardInput: &agentrpc.IOChunk{Data: nil},
						},
					})
					if readErr != io.EOF {
						errCh <- readErr
					}
					return
				}
			}
		}()
	}

	// Stream the host's terminal dimensions if a pseudo-terminal was
	// requested.
	if c.TTY {
		sigwinch := make(chan os.Signal, 1)
		signal.Notify(sigwinch, syscall.SIGWINCH)
		defer signal.Stop(sigwinch)

		go func() {
			for {
				select {
				case <-sigwinch:
					if width, height, err := terminal.TermGetSize(); err == nil {
						if err := send(&agentrpc.ExecRequest{
							Type: &agentrpc.ExecRequest_TerminalResize{
								TerminalResize: &agentrpc.TerminalSize{Cols: uint32(width), Rows: uint32(height)},
							},
						}); err != nil {
							errCh <- err
							return
						}
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// Process command events.
	go func() {
		for {
			response, err := execCall.Recv()
			if err != nil {
				errCh <- err
				return
			}

			switch event := response.GetType().(type) {
			case *agentrpc.ExecResponse_StandardOutput:
				_, _ = os.Stdout.Write(event.StandardOutput.GetData())
			case *agentrpc.ExecResponse_StandardError:
				_, _ = os.Stderr.Write(event.StandardError.GetData())
			case *agentrpc.ExecResponse_Exit_:
				errCh <- &weaveerrors.ExecCustomExitCodeError{Code: event.Exit.GetCode()}
				return
			default:
				// Unknown event, do nothing.
			}
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
