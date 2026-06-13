// Port of tart's Network/Softnet.swift: VM networking through the softnet
// helper process over a datagram socketpair. NSTask drives the process;
// socketpair/setsockopt/tcsetpgrp are raw syscalls.
//go:build darwin

package network

import (
	"fmt"
	"os"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/deploymenttheory/weave/internal/terminal"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/vmconfig"

	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	virtualization "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/virtualization"
)

// buildSoftnetNIC constructs a softnet NIC: it spawns the softnet helper over a
// datagram socketpair and wraps the VM-side file handle in a
// VZFileHandleNetworkDeviceAttachment. The helper's lifecycle is driven by the
// returned NIC's engine.
func buildSoftnetNIC(nicConfig vmconfig.NICConfig, mac *virtualization.VZMACAddress) (NIC, error) {
	var args []string
	if nicConfig.SoftnetHostMode {
		args = append(args, "--vm-net-type", "host")
	}
	if nicConfig.SoftnetAllow != "" {
		args = append(args, "--allow", nicConfig.SoftnetAllow)
	}
	if nicConfig.SoftnetBlock != "" {
		args = append(args, "--block", nicConfig.SoftnetBlock)
	}
	if nicConfig.SoftnetExpose != "" {
		args = append(args, "--expose", nicConfig.SoftnetExpose)
	}

	softnet, err := NewSoftnet(objcutil.GoStr(mac.String()), args...)
	if err != nil {
		return NIC{}, err
	}
	return NIC{Attachment: softnet.attachment(), MAC: mac, engine: softnetEngine{softnet}}, nil
}

// softnetEngine adapts *Softnet to the nicEngine lifecycle interface.
type softnetEngine struct{ softnet *Softnet }

func (e softnetEngine) run(sema *AsyncSemaphore) error { return e.softnet.Run(sema) }
func (e softnetEngine) stop() error                    { return e.softnet.Stop() }

// SoftnetError ports the SoftnetError enum.
type SoftnetError struct {
	Kind string // "InitializationFailed" | "RuntimeFailed"
	Why  string
}

func (e *SoftnetError) Error() string { return e.Kind + ": " + e.Why }

func softnetInitializationFailed(format string, params ...any) *SoftnetError {
	return &SoftnetError{Kind: "InitializationFailed", Why: fmt.Sprintf(format, params...)}
}

func softnetRuntimeFailed(format string, params ...any) *SoftnetError {
	return &SoftnetError{Kind: "RuntimeFailed", Why: fmt.Sprintf(format, params...)}
}

// Softnet ports tart's Softnet class.
type Softnet struct {
	task        *foundation.NSTask
	monitorDone chan struct{}
	finished    atomic.Bool

	VMFD int
}

// NewSoftnet ports Softnet.init(vmMACAddress:extraArguments:).
func NewSoftnet(vmMACAddress string, extraArguments ...string) (*Softnet, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return nil, softnetInitializationFailed("socketpair() failed: %v", err)
	}

	vmFD := fds[0]
	softnetFD := fds[1]

	if err := setSocketBuffers(vmFD, 1*1024*1024); err != nil {
		return nil, err
	}
	if err := setSocketBuffers(softnetFD, 1*1024*1024); err != nil {
		return nil, err
	}

	executableURL, err := softnetExecutableURL()
	if err != nil {
		return nil, err
	}

	task := foundation.NSTaskFromID(purego.Send[purego.ID](purego.ID(purego.GetClass("NSTask")), purego.RegisterName("new")))
	task.SetExecutableURL(executableURL)
	arguments := append([]string{"--vm-fd", "0", "--vm-mac-address", vmMACAddress}, extraArguments...)
	task.SetArguments(objcutil.NSStringArray(arguments))
	stdinHandle := foundation.NSFileHandleFromID(objcutil.AllocClass("NSFileHandle")).
		InitWithFileDescriptorCloseOnDealloc(softnetFD, false)
	task.SetStandardInput(stdinHandle.Ptr())

	return &Softnet{
		task:        task,
		monitorDone: make(chan struct{}),
		VMFD:        vmFD,
	}, nil
}

// softnetExecutableURL ports Softnet.softnetExecutableURL().
func softnetExecutableURL() (*foundation.NSURL, error) {
	executableURL := objcutil.ResolveBinaryPath("softnet")
	if executableURL == nil {
		return nil, softnetInitializationFailed("softnet not found in PATH")
	}
	return executableURL, nil
}

// Run ports Softnet.run(_:): launches the process and monitors it.
func (s *Softnet) Run(sema *AsyncSemaphore) error {
	if _, err := s.task.LaunchAndReturnError(); err != nil {
		return err
	}

	go func() {
		// Wait for the Softnet to finish.
		s.task.WaitUntilExit()

		// Signal to the caller that the Softnet has finished.
		sema.Signal()

		// Signal to ourselves that the Softnet has finished.
		s.finished.Store(true)
		close(s.monitorDone)
	}()

	return nil
}

// Stop ports Softnet.stop().
func (s *Softnet) Stop() error {
	if s.finished.Load() {
		<-s.monitorDone
		return softnetRuntimeFailed("Softnet process terminated prematurely")
	}

	s.task.Interrupt()
	<-s.monitorDone
	return nil
}

func setSocketBuffers(fd int, sizeBytes int) error {
	// The system expects the value of SO_RCVBUF to be at least double the
	// value of SO_SNDBUF, and for optimal performance, the recommended value
	// of SO_RCVBUF is four times the value of SO_SNDBUF (see Apple's
	// VZFileHandleNetworkDeviceAttachment.maximumTransmissionUnit docs).
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4*sizeBytes); err != nil {
		return softnetInitializationFailed("setsockopt(SO_RCVBUF) failed: %v", err)
	}
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_SNDBUF, sizeBytes); err != nil {
		return softnetInitializationFailed("setsockopt(SO_SNDBUF) failed: %v", err)
	}
	return nil
}

// attachment builds the VM-side VZFileHandleNetworkDeviceAttachment over the
// softnet socketpair.
func (s *Softnet) attachment() *virtualization.VZNetworkDeviceAttachment {
	fileHandle := foundation.NSFileHandleFromID(objcutil.AllocClass("NSFileHandle")).
		InitWithFileDescriptorCloseOnDealloc(s.VMFD, false)
	attachment := virtualization.VZFileHandleNetworkDeviceAttachmentFromID(
		objcutil.AllocClass("VZFileHandleNetworkDeviceAttachment")).InitWithFileHandle(fileHandle)
	return &attachment.VZNetworkDeviceAttachment
}

// SoftnetConfigureSUIDBitIfNeeded ports Softnet.configureSUIDBitIfNeeded().
func SoftnetConfigureSUIDBitIfNeeded() error {
	// Obtain the Softnet executable path. Resolving symlinks matters here:
	// otherwise we get "/opt/homebrew/bin/softnet" instead of the Cellar path.
	executableURL, err := softnetExecutableURL()
	if err != nil {
		return err
	}
	softnetExecutablePath := objcutil.GoStr(executableURL.URLByResolvingSymlinksInPath().Path())

	// Check if the SUID bit is already configured.
	info, err := os.Stat(softnetExecutablePath)
	if err != nil {
		return err
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		if stat.Uid == 0 && info.Mode()&os.ModeSetuid != 0 {
			return nil
		}
	}

	// Check if passwordless Sudo is already configured for Softnet.
	sudoExecutableURL := objcutil.ResolveBinaryPath("sudo")
	if sudoExecutableURL == nil {
		return softnetInitializationFailed("sudo not found in PATH")
	}

	probe := foundation.NSTaskFromID(purego.Send[purego.ID](purego.ID(purego.GetClass("NSTask")), purego.RegisterName("new")))
	probe.SetExecutableURL(sudoExecutableURL)
	probe.SetArguments(objcutil.NSStringArray([]string{"--non-interactive", "softnet", "--help"}))
	if _, err := probe.LaunchAndReturnError(); err != nil {
		return err
	}
	probe.WaitUntilExit()
	if probe.TerminationStatus() == 0 {
		return nil
	}

	// Configure the SUID bit by spawning Sudo in interactive mode and asking
	// the user for the password required to run chown & chmod.
	fmt.Fprintln(os.Stderr, "Softnet requires a Sudo password to set the SUID bit on the Softnet executable, please enter it below.")

	elevate := foundation.NSTaskFromID(purego.Send[purego.ID](purego.ID(purego.GetClass("NSTask")), purego.RegisterName("new")))
	elevate.SetExecutableURL(sudoExecutableURL)
	elevate.SetArguments(objcutil.NSStringArray([]string{
		"sh", "-c",
		fmt.Sprintf("chown root %s && chmod u+s %s", softnetExecutablePath, softnetExecutablePath),
	}))
	if _, err := elevate.LaunchAndReturnError(); err != nil {
		return err
	}

	// Set the TTY's foreground process group to that of the Sudo process,
	// otherwise it will get stopped by a SIGTTIN once user input arrives.
	pgid := int32(elevate.ProcessIdentifier())
	if err := terminal.TermIoctl(os.Stdin.Fd(), syscall.TIOCSPGRP, unsafe.Pointer(&pgid)); err != nil {
		return weaveerrors.ErrSoftnetFailed(fmt.Sprintf("tcsetpgrp(2) failed: %v", err))
	}

	elevate.WaitUntilExit()

	if elevate.TerminationStatus() != 0 {
		return weaveerrors.ErrSoftnetFailed("failed to configure SUID bit on Softnet executable with Sudo")
	}

	return nil
}
