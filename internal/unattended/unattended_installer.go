// Port of lume's Unattended/UnattendedInstaller.swift: orchestrates an
// unattended Setup Assistant run. The VM is started as a child
// "weave run <name> --vnc-experimental --vnc-password <generated>
// --no-graphics" process (run must own a main thread and AppKit run loop),
// the printed VNC URL is parsed from its stdout, and after boot_wait the
// boot commands are executed over VNC + OCR. An optional health check and
// post-setup SSH commands finish the job; the VM is left running.
//go:build darwin

package unattended

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/deploymenttheory/weave/internal/vmconfig"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/macaddress"
	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/passphrase"
	"github.com/deploymenttheory/weave/internal/screenviewer"
	weavessh "github.com/deploymenttheory/weave/internal/ssh"
	"github.com/deploymenttheory/weave/internal/vmdirectory"
	"github.com/deploymenttheory/weave/internal/vmstorage"
	weavevnc "github.com/deploymenttheory/weave/internal/vnc"
)

// SetupOptions configures RunUnattendedSetup.
type SetupOptions struct {
	Name     string
	Debug    bool
	DebugDir string
	// ShowScreen opens a view-only viewer (a browser MJPEG stream of the
	// frames the automation captures) so an operator can watch each step
	// while tuning a preset to a new macOS build — without sending any input
	// into the guest.
	ShowScreen bool
}

var VNCURLPattern = regexp.MustCompile(`vnc://:([^@]+)@([\d.]+):(\d+)`)

// RunUnattendedSetup performs the full preset-mode unattended setup flow.
func RunUnattendedSetup(ctx context.Context, opts SetupOptions, config *UnattendedConfig) error {
	commands, err := ParseBootCommands(config.BootCommands)
	if err != nil {
		return err
	}

	storage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return err
	}
	vmDir, err := storage.Open(opts.Name)
	if err != nil {
		return err
	}
	if running, err := vmDir.Running(); err != nil {
		return err
	} else if running {
		return weaveerrors.ErrVMIsRunning(opts.Name)
	}

	// Start the VM with a known VNC password so we can dial back in.
	password := strings.Join(passphrase.GeneratePassphrase(4), "-")
	host, port, err := StartVMWithVNC(ctx, opts.Name, password)
	if err != nil {
		return err
	}
	fmt.Printf("VM started; VNC at %s:%d\n", host, port)

	// Optional view-only screen viewer.
	var viewer *screenviewer.ScreenServer
	if opts.ShowScreen {
		if viewer, err = screenviewer.NewScreenServer(); err != nil {
			return err
		}
		defer viewer.Close()
		fmt.Printf("View-only screen: open %s in a browser to watch (no input reaches the VM).\n", viewer.URL())
		screenviewer.OpenInBrowser(viewer.URL())
	}

	fmt.Printf("Waiting %ds for the VM to boot...\n", config.BootWait)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Duration(config.BootWait) * time.Second):
	}

	vnc, err := weavevnc.DialVNC(ctx, host, port, password)
	if err != nil {
		return err
	}
	defer vnc.Close()
	fmt.Printf("Connected to VNC (%dx%d framebuffer)\n", vnc.Width, vnc.Height)

	automation := NewAutomation(vnc, opts.Debug, opts.DebugDir)
	automation.viewer = viewer
	if err := automation.ExecuteAll(ctx, commands); err != nil {
		return err
	}
	fmt.Println("Boot commands completed")

	if config.HealthCheck == nil && len(config.PostSSHCommands) == 0 {
		fmt.Println("Unattended setup finished; VM left running.")
		return nil
	}

	// Health check and post-setup commands need the VM's IP.
	ip, err := waitForVMIP(ctx, vmDir, 120)
	if err != nil {
		return err
	}

	user, password2 := "weave", "weave"
	if config.HealthCheck != nil {
		if config.HealthCheck.User != "" {
			user = config.HealthCheck.User
		}
		if config.HealthCheck.Password != "" {
			password2 = config.HealthCheck.Password
		}
		if err := RunHealthCheck(ctx, config.HealthCheck, ip); err != nil {
			return err
		}
	}

	if len(config.PostSSHCommands) > 0 {
		if err := RunPostSSHCommands(ctx, config.PostSSHCommands, ip, user, password2); err != nil {
			return err
		}
	}

	fmt.Println("Unattended setup finished; VM left running.")
	return nil
}

// StartVMWithVNC spawns the detached run subprocess and parses the VNC URL
// it prints ("VNC server is running at vnc://:<password>@<host>:<port>").
func StartVMWithVNC(ctx context.Context, name string, password string) (host string, port int, err error) {
	executable, err := os.Executable()
	if err != nil {
		return "", 0, err
	}

	// The VM runs headless: the automation drives it over VNC, and the
	// optional view-only screen viewer (ScreenServer) shows the captured
	// frames. A native VZVirtualMachineView window is deliberately avoided
	// because it would forward the operator's mouse/keyboard into the guest
	// and fight the automation.
	command := exec.Command(executable, "run", name,
		"--vnc-experimental", "--vnc-password", password, "--no-graphics")
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.Stderr = os.Stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", 0, err
	}
	if err := command.Start(); err != nil {
		return "", 0, err
	}
	go func() { _ = command.Wait() }()

	// Scan the child's output for the VNC URL (keep draining afterwards so
	// the child never blocks on a full pipe).
	urlCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Println(line)
			if match := VNCURLPattern.FindStringSubmatch(line); match != nil {
				select {
				case urlCh <- line:
				default:
				}
			}
		}
	}()

	select {
	case <-ctx.Done():
		return "", 0, ctx.Err()
	case <-time.After(60 * time.Second):
		return "", 0, weaveerrors.ErrVNCAutomationFailed("timed out waiting for the VNC server URL from the run process")
	case line := <-urlCh:
		match := VNCURLPattern.FindStringSubmatch(line)
		port, err := strconv.Atoi(match[3])
		if err != nil {
			return "", 0, weaveerrors.ErrVNCAutomationFailed("malformed VNC URL: " + line)
		}
		return match[2], port, nil
	}
}

// waitForVMIP resolves the VM's IP, waiting up to secondsToWait.
func waitForVMIP(ctx context.Context, vmDir *vmdirectory.VMDirectory, secondsToWait uint16) (string, error) {
	vmConfig, err := vmconfig.NewVMConfigFromURL(vmDir.ConfigURL())
	if err != nil {
		return "", err
	}
	mac, ok := macaddress.NewMACAddress(objcutil.GoStr(vmConfig.MACAddress.String()))
	if !ok {
		return "", weaveerrors.ErrGeneric("failed to parse VM's MAC address")
	}
	ip, found, err := macaddress.ResolveIP(ctx, mac, macaddress.IPResolutionStrategyDHCP, secondsToWait, vmDir.ControlSocketURL())
	if err != nil {
		return "", err
	}
	if !found {
		return "", weavessh.ErrSSHNoIPAddress(vmDir.Name())
	}
	return ip.String(), nil
}
