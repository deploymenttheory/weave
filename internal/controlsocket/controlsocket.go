// Port of tart's ControlSocket.swift: a Unix domain socket proxy bridging
// external clients (e.g. "weave exec") to a vsock port inside the VM. Swift
// NIO's async server/client channels become net.Listener plus per-connection
// goroutines with a bidirectional io.Copy proxy.
//go:build darwin

package controlsocket

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/objcutil"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	virtualization "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/virtualization"
	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/library/oslog"
)

// VirtioSocketConnector is the slice of VM.connect(toPort:) that
// ControlSocket needs; *vm.VM satisfies it.
type VirtioSocketConnector interface {
	Connect(ctx context.Context, toPort uint32) (*virtualization.VZVirtioSocketConnection, error)
}

// The connector is published by the run command once the VM exists
// (mirroring tart's Run.swift global vm); nil until then.
var (
	connectorMu sync.RWMutex
	connector   VirtioSocketConnector
)

// SetConnector publishes the running VM for control socket clients. Call it
// only with a live VM — a typed-nil connector would defeat the nil check in
// handleClient.
func SetConnector(vmConnector VirtioSocketConnector) {
	connectorMu.Lock()
	defer connectorMu.Unlock()
	connector = vmConnector
}

func currentConnector() VirtioSocketConnector {
	connectorMu.RLock()
	defer connectorMu.RUnlock()
	return connector
}

// ControlSocket ports tart's ControlSocket class.
type ControlSocket struct {
	controlSocketURL *foundation.NSURL
	vmPort           uint32
	logger           *oslog.Logger
}

// NewControlSocket ports ControlSocket.init(_:vmPort:) with the default
// vmPort of 8080.
func NewControlSocket(controlSocketURL *foundation.NSURL) *ControlSocket {
	return NewControlSocketWithPort(controlSocketURL, 8080)
}

// NewControlSocketWithPort ports ControlSocket.init(_:vmPort:).
func NewControlSocketWithPort(controlSocketURL *foundation.NSURL, vmPort uint32) *ControlSocket {
	return &ControlSocket{
		controlSocketURL: controlSocketURL,
		vmPort:           vmPort,
		logger:           oslog.NewLogger("com.deploymenttheory.weave.control-socket", "network"),
	}
}

// Run ports ControlSocket.run(): binds the Unix domain socket and serves
// client connections until ctx is cancelled.
func (s *ControlSocket) Run(ctx context.Context) error {
	fileManager := foundation.NSFileManagerDefaultManager()

	// Remove the control socket file from previous "run" invocations, if
	// any, otherwise we may get the "address already in use" error. Failures
	// are deliberately ignored (Swift: try?).
	_, _ = fileManager.RemoveItemAtPathError(s.controlSocketURL.Path())

	// Change the current working directory to the VM's base directory to
	// work around the 104-byte Unix domain socket path limitation, then bind
	// to the path relative to it.
	if baseURL := s.controlSocketURL.BaseURL(); baseURL != nil {
		fileManager.ChangeCurrentDirectoryPath(baseURL.Path())
	}

	listener, err := net.Listen("unix", objcutil.GoStr(s.controlSocketURL.RelativePath()))
	if err != nil {
		return err
	}

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	var clients sync.WaitGroup
	defer clients.Wait()

	for {
		clientConn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}

		clients.Go(func() {
			s.handleClient(ctx, clientConn)
		})
	}
}

// handleClient ports ControlSocket.handleClient: connects to the VM's vsock
// port and proxies bytes in both directions until either side disconnects.
// Like the Swift original, failures are logged rather than propagated.
func (s *ControlSocket) handleClient(ctx context.Context, clientConn net.Conn) {
	defer func() { _ = clientConn.Close() }()

	s.logger.Info("received new control socket connection from a client")
	s.logger.Info(fmt.Sprintf("dialing to VM on port %d...", s.vmPort))

	vm := currentConnector()
	if vm == nil {
		s.logger.Error(fmt.Sprintf("control socket connection failed: %v", weaveerrors.ErrVMSocketFailed(s.vmPort, "VM is not running")))
		return
	}

	vmConnection, err := vm.Connect(ctx, s.vmPort)
	if err != nil {
		s.logger.Error(fmt.Sprintf("control socket connection failed: %v", err))
		return
	}
	defer vmConnection.Close()

	s.logger.Info("running control socket proxy")

	// NIO's ClientBootstrap.withConnectedSocket equivalent: adopt the virtio
	// socket's file descriptor. Dup it so the os.File owns its descriptor
	// and VZVirtioSocketConnection.Close keeps sole ownership of the original.
	dupFD, err := syscall.Dup(vmConnection.FileDescriptor())
	if err != nil {
		s.logger.Error(fmt.Sprintf("control socket connection failed: %v", err))
		return
	}
	vmConn := os.NewFile(uintptr(dupFD), "virtio-socket")
	defer func() { _ = vmConn.Close() }()

	// When either copy direction finishes, close both ends so the opposite
	// copy unblocks (NIO closes the channels when the task group completes).
	var once sync.Once
	closeBoth := func() {
		_ = clientConn.Close()
		_ = vmConn.Close()
	}

	var proxies sync.WaitGroup
	proxies.Go(func() {
		// Proxy data from a client (e.g. "weave exec") to a VM.
		_, _ = io.Copy(vmConn, clientConn)
		once.Do(closeBoth)
	})
	proxies.Go(func() {
		// Proxy data from a VM to a client (e.g. "weave exec").
		_, _ = io.Copy(clientConn, vmConn)
		once.Do(closeBoth)
	})
	proxies.Wait()

	s.logger.Info("control socket client disconnected")
}
