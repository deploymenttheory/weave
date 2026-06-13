// Port of lume's Commands/SSH.swift: connect to a VM via SSH or execute a
// command remotely. Password authentication is handled in-process (no
// sshpass needed), with a fallback to the system ssh binary when the
// in-process client cannot establish a TCP connection. VM lookup and IP
// resolution reuse weave's local storage and ResolveIP machinery.
//go:build darwin

package command

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deploymenttheory/weave/internal/logging"
	"github.com/deploymenttheory/weave/internal/vmconfig"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/macaddress"
	"github.com/deploymenttheory/weave/internal/objcutil"
	weavessh "github.com/deploymenttheory/weave/internal/ssh"
	"github.com/deploymenttheory/weave/internal/vmstorage"
)

// convention (admin/admin) rather than lume's lume/lume.
type SSHCommand struct {
	Name     string
	Command  []string
	User     string
	Password string
	Timeout  uint64 // seconds; 0 means no timeout
	Wait     uint16 // seconds to wait for IP resolution
	Resolver macaddress.IPResolutionStrategy
}

func (c *SSHCommand) Run(ctx context.Context) error {
	storage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return err
	}
	vmDir, err := storage.Open(c.Name)
	if err != nil {
		return err
	}

	if running, err := vmDir.Running(); err != nil {
		return err
	} else if !running {
		return weaveerrors.ErrVMNotRunning(c.Name)
	}

	vmConfig, err := vmconfig.NewVMConfigFromURL(vmDir.ConfigURL())
	if err != nil {
		return err
	}
	vmMACAddress, ok := macaddress.NewMACAddress(objcutil.GoStr(vmConfig.MACAddress.String()))
	if !ok {
		return weaveerrors.ErrGeneric("failed to parse VM's MAC address")
	}

	ip, found, err := macaddress.ResolveIP(ctx, vmMACAddress, c.Resolver, c.Wait, vmDir.ControlSocketURL())
	if err != nil {
		return err
	}
	if !found {
		return weavessh.ErrSSHNoIPAddress(c.Name)
	}

	// Try the in-process client first, falling back to system ssh only on
	// connection failures (not auth failures or timeouts), as SSH.swift does.
	err = c.executeWith(ctx, weavessh.NewSSHClient(ip.String(), 22, c.User, c.Password))
	var sshErr *weavessh.SSHError
	if errors.As(err, &sshErr) && sshErr.Kind == weavessh.SSHErrorConnectionFailed {
		logging.DefaultLogger().AppendNewLine("in-process SSH connection failed, falling back to system ssh")
		return c.executeWith(ctx, weavessh.NewSystemSSHClient(ip.String(), 22, c.User, c.Password))
	}
	return err
}

// sshExecutor is the shared surface of SSHClient and SystemSSHClient.
type sshExecutor interface {
	Execute(ctx context.Context, command string, timeout time.Duration) (weavessh.SSHResult, error)
	Interactive(ctx context.Context) error
}

func (c *SSHCommand) executeWith(ctx context.Context, client sshExecutor) error {
	if len(c.Command) == 0 {
		return client.Interactive(ctx)
	}

	result, err := client.Execute(ctx, strings.Join(c.Command, " "), time.Duration(c.Timeout)*time.Second)
	if err != nil {
		return err
	}
	if result.Output != "" {
		fmt.Print(result.Output)
		if !strings.HasSuffix(result.Output, "\n") {
			fmt.Println()
		}
	}
	if result.ExitCode != 0 {
		return &weaveerrors.ExecCustomExitCodeError{Code: result.ExitCode}
	}
	return nil
}
