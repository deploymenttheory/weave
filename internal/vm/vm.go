// Port of tart's VM.swift: wraps VZVirtualMachine, crafts its configuration,
// drives start/stop/install, and receives delegate callbacks.
//
// Swift-to-Go mechanics:
//   - VZVirtualMachineDelegate is implemented by registering an ObjC class
//     at runtime through purego (vmDelegateClass).
//   - Generated *CompletionHandler bindings panic under purego, so every
//     async call builds its block manually (see fetcher.go for the pattern).
//   - All VZVirtualMachine access is dispatched to the main queue via
//     internal/objc.RunOnMainThread, matching the @MainActor annotations.
//go:build darwin

package vm

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/deploymenttheory/weave/internal/telemetry"
	"github.com/deploymenttheory/weave/internal/vmstorage"

	"github.com/deploymenttheory/weave/internal/controlsocket"
	"github.com/deploymenttheory/weave/internal/vmconfig"

	"github.com/deploymenttheory/weave/internal/ci"
	weaveconfig "github.com/deploymenttheory/weave/internal/config"
	"github.com/deploymenttheory/weave/internal/diskimage"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/fetcher"
	"github.com/deploymenttheory/weave/internal/ipsw"
	weavelock "github.com/deploymenttheory/weave/internal/lock"
	"github.com/deploymenttheory/weave/internal/logging"
	weavenetwork "github.com/deploymenttheory/weave/internal/network"
	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/oci"
	weaveplatform "github.com/deploymenttheory/weave/internal/platform"
	"github.com/deploymenttheory/weave/internal/prune"
	"github.com/deploymenttheory/weave/internal/vmdirectory"

	"github.com/ebitengine/purego/objc"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	virtualization "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/virtualization"
	dispatch "github.com/deploymenttheory/go-bindings-macosplatform/internal/objc"
	"github.com/deploymenttheory/go-bindings-macosplatform/internal/pureobjc"
	idiomatic "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/virtualization"
)

// Error types ported from VM.swift.

type UnsupportedRestoreImageError struct{}

func (UnsupportedRestoreImageError) Error() string { return "unsupported restore image" }

type NoMainScreenFoundError struct{}

func (NoMainScreenFoundError) Error() string { return "no main screen found" }

type DownloadFailedError struct{}

func (DownloadFailedError) Error() string { return "download failed" }

// UnsupportedOSError ports VM.swift's UnsupportedOSError.
type UnsupportedOSError struct {
	What     string
	Plural   string
	Requires string
}

func NewUnsupportedOSError(what string, plural string) *UnsupportedOSError {
	return &UnsupportedOSError{What: what, Plural: plural, Requires: "running macOS 13.0 (Ventura) or newer"}
}

func (e *UnsupportedOSError) Error() string {
	return fmt.Sprintf("error: %s %s only supported on hosts %s", e.What, e.Plural, e.Requires)
}

type UnsupportedArchitectureError struct{}

func (UnsupportedArchitectureError) Error() string { return "unsupported architecture" }

// VMOptions carries VM.init's defaulted parameters. Zero values match the
// Swift defaults (audio and clipboard are inverted to keep that true).
type VMOptions struct {
	// NICs optionally overrides the VM config's persisted network topology for
	// this run (nil uses the config's NICs, synthesised from the legacy MAC
	// when absent). The primary NIC's MAC is bound from the VM config; missing
	// MACs on secondary NICs are derived deterministically.
	NICs                     []vmconfig.NICConfig
	AdditionalStorageDevices []*virtualization.VZStorageDeviceConfiguration
	DirectorySharingDevices  []*virtualization.VZDirectorySharingDeviceConfiguration
	SerialPorts              []*virtualization.VZSerialPortConfiguration
	Suspendable              bool
	Nested                   bool
	NoAudio                  bool
	NoClipboard              bool
	// ClipboardPolicyEnabled means the host-side enterprise clipboard engine is
	// authoritative; the SPICE agent clipboard is disabled so policy controls
	// (direction, formats, files, bandwidth) are not bypassed by the OS path.
	ClipboardPolicyEnabled bool
	Sync                   virtualization.VZDiskImageSynchronizationMode
	Caching                *virtualization.VZDiskImageCachingMode
	NoTrackpad             bool
	NoPointer              bool
	NoKeyboard             bool
}

func (o *VMOptions) normalize() {
	if o.Sync == 0 {
		o.Sync = virtualization.VZDiskImageSynchronizationModeFull
	}
}

// resolveTopology builds the network topology for a run: it uses the override
// NICs when present, otherwise the config's NICs (synthesised from the legacy
// MAC when absent), then binds the primary NIC's MAC to the config's address so
// guest IP resolution and MAC-conflict detection stay correct.
func resolveTopology(vmConfig *vmconfig.VMConfig, options VMOptions) (*weavenetwork.Topology, error) {
	nics := options.NICs
	if nics == nil {
		nics = vmConfig.EnsureNICs()
	} else {
		nics = append([]vmconfig.NICConfig(nil), nics...)
	}

	primaryMAC := objcutil.GoStr(vmConfig.MACAddress.String())
	boundPrimary := false
	for i := range nics {
		if nics[i].IsPrimary && !boundPrimary {
			nics[i].MACAddress = primaryMAC
			boundPrimary = true
		}
	}
	if !boundPrimary && len(nics) > 0 {
		nics[0].IsPrimary = true
		nics[0].MACAddress = primaryMAC
	}

	return weavenetwork.BuildTopology(nics)
}

// VM ports tart's VM class.
type VM struct {
	// VirtualMachine is Virtualization.Framework's virtual machine.
	VirtualMachine *virtualization.VZVirtualMachine

	// Configuration is the machine's VZVirtualMachineConfiguration.
	Configuration *virtualization.VZVirtualMachineConfiguration

	// sema communicates with the VZVirtualMachineDelegate.
	sema *weavenetwork.AsyncSemaphore

	Name   string
	Config *vmconfig.VMConfig

	network    *weavenetwork.Topology
	delegateID objc.ID
}

var _ controlsocket.VirtioSocketConnector = (*VM)(nil)

// vmDelegateRegistry maps delegate instances to their VM.
var vmDelegateRegistry sync.Map // objc.ID → *VM

// vmDelegateClass registers the ObjC class implementing
// VZVirtualMachineDelegate, signalling the VM's semaphore like VM.swift.
var vmDelegateClass = sync.OnceValue(func() objc.Class {
	lookup := func(self objc.ID) *VM {
		if vm, ok := vmDelegateRegistry.Load(self); ok {
			return vm.(*VM)
		}
		return nil
	}

	class, err := objc.RegisterClass("OrinVMDelegate", objc.GetClass("NSObject"),
		[]*objc.Protocol{objc.GetProtocol("VZVirtualMachineDelegate")},
		nil,
		[]objc.MethodDef{
			{
				Cmd: objc.RegisterName("guestDidStop:"),
				Fn: func(self objc.ID, _ objc.SEL, _ objc.ID) {
					fmt.Println("guest has stopped the virtual machine")
					if vm := lookup(self); vm != nil {
						vm.sema.Signal()
					}
				},
			},
			{
				Cmd: objc.RegisterName("virtualMachine:didStopWithError:"),
				Fn: func(self objc.ID, _ objc.SEL, _ objc.ID, errID objc.ID) {
					fmt.Printf("guest has stopped the virtual machine due to error: %v\n", pureobjc.NSErrorToError(errID))
					if vm := lookup(self); vm != nil {
						vm.sema.Signal()
					}
				},
			},
			{
				Cmd: objc.RegisterName("virtualMachine:networkDevice:attachmentWasDisconnectedWithError:"),
				Fn: func(self objc.ID, _ objc.SEL, _ objc.ID, deviceID objc.ID, errID objc.ID) {
					fmt.Printf("virtual machine's network attachment %v has been disconnected with error: %v\n",
						deviceID, pureobjc.NSErrorToError(errID))
					if vm := lookup(self); vm != nil {
						vm.sema.Signal()
					}
				},
			},
		})
	if err != nil {
		panic(fmt.Sprintf("failed to register OrinVMDelegate: %v", err))
	}
	return class
})

// NewVM ports VM.init(vmDir:…) for an existing VM directory.
func NewVM(vmDir *vmdirectory.VMDirectory, options VMOptions) (*VM, error) {
	options.normalize()

	config, err := vmconfig.NewVMConfigFromURL(vmDir.ConfigURL())
	if err != nil {
		return nil, err
	}

	if config.Arch != weaveplatform.CurrentArchitecture() {
		return nil, UnsupportedArchitectureError{}
	}

	topology, err := resolveTopology(config, options)
	if err != nil {
		return nil, err
	}

	configuration, err := craftConfiguration(vmDir.DiskURL(), vmDir.NvramURL(), config, options, topology)
	if err != nil {
		return nil, err
	}

	vm := &VM{
		Configuration: configuration,
		sema:          weavenetwork.NewAsyncSemaphore(),
		Name:          vmDir.Name(),
		Config:        config,
		network:       topology,
	}
	vm.attachVirtualMachine()

	return vm, nil
}

// attachVirtualMachine creates the VZVirtualMachine on the main queue and
// installs the delegate (Swift: VZVirtualMachine(configuration:) + delegate).
func (vm *VM) attachVirtualMachine() {
	dispatch.RunOnMainThread(func() {
		vm.VirtualMachine = virtualization.VZVirtualMachineFromID(objcutil.AllocClass("VZVirtualMachine")).
			InitWithConfiguration(vm.Configuration)

		delegateID := objc.ID(vmDelegateClass()).Send(objc.RegisterName("new"))
		vmDelegateRegistry.Store(delegateID, vm)
		vm.delegateID = delegateID
		vm.VirtualMachine.Ptr().Send(objc.RegisterName("setDelegate:"), delegateID)
	})
}

// VMRetrieveIPSW ports VM.retrieveIPSW(remoteURL:): returns a cached *.ipsw
// location, downloading and caching it when missing.
func VMRetrieveIPSW(ctx context.Context, remoteURL *foundation.NSURL) (*foundation.NSURL, error) {
	// Check if we already have this IPSW in cache.
	headRequest := foundation.NSMutableURLRequestFromID(
		objc.Send[objc.ID](objcutil.AllocClass("NSMutableURLRequest"), objc.RegisterName("initWithURL:"), remoteURL.Ptr()))
	headRequest.SetHTTPMethod(objcutil.NSStr("HEAD"))
	_, headResponse, err := fetcher.FetcherFetch(ctx, &headRequest.NSURLRequest, false)
	if err != nil {
		return nil, err
	}

	if hash := headResponse.ValueForHTTPHeaderField(objcutil.NSStr("x-amz-meta-digest-sha256")); hash != nil {
		cache, err := ipsw.NewIPSWCache()
		if err != nil {
			return nil, err
		}
		ipswLocation := cache.LocationFor("sha256:" + objcutil.GoStr(hash) + ".ipsw")

		if foundation.NSFileManagerDefaultManager().FileExistsAtPath(ipswLocation.Path()) {
			logging.DefaultLogger().AppendNewLine("Using cached *.ipsw file...")
			if err := prune.URLUpdateAccessDate(ipswLocation, time.Now()); err != nil {
				return nil, err
			}
			return ipswLocation, nil
		}
	}

	// Download the IPSW.
	logging.DefaultLogger().AppendNewLine(fmt.Sprintf("Fetching %s...", objcutil.GoStr(remoteURL.LastPathComponent())))

	chunks, response, err := fetcher.FetcherFetch(ctx, foundation.NSURLRequestRequestWithURL(remoteURL), true)
	if err != nil {
		return nil, err
	}

	config, err := weaveconfig.NewConfig()
	if err != nil {
		return nil, err
	}
	temporaryLocation := config.WeaveTmpDir.URLByAppendingPathComponent(
		foundation.NSUUIDUUID().UUIDString().StringByAppendingString(objcutil.NSStr(".ipsw")))

	// Refuse the download up front if the host volume cannot hold it
	// (framework-queried capacity; prunable cache entries reclaimed first).
	if expectedLength := response.ExpectedContentLength(); expectedLength > 0 {
		if err := vmstorage.EnsureDiskSpace(uint64(expectedLength), nil); err != nil {
			return nil, err
		}
	}

	progress := logging.NewDownloadProgress(response.ExpectedContentLength())
	logging.NewProgressObserver(progress).Log(logging.DefaultLogger())

	temporaryPath := objcutil.GoStr(temporaryLocation.Path())
	temporaryFile, err := os.Create(temporaryPath)
	if err != nil {
		return nil, err
	}
	defer temporaryFile.Close()

	lock, err := weavelock.NewFileLock(temporaryLocation)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := lock.Lock(); err != nil {
		return nil, err
	}

	digest := oci.NewDigest()
	for chunk := range chunks {
		if chunk.Err != nil {
			return nil, chunk.Err
		}
		if _, err := temporaryFile.Write(chunk.Data); err != nil {
			return nil, err
		}
		digest.Update(chunk.Data)
		progress.Add(int64(len(chunk.Data)))
	}
	if err := temporaryFile.Close(); err != nil {
		return nil, err
	}

	cache, err := ipsw.NewIPSWCache()
	if err != nil {
		return nil, err
	}
	finalLocation := cache.LocationFor(digest.Finalize() + ".ipsw")

	// Swift uses FileManager.replaceItemAt; an atomic rename is equivalent.
	if err := os.Rename(temporaryPath, objcutil.GoStr(finalLocation.Path())); err != nil {
		return nil, err
	}
	return finalLocation, nil
}

// InFinalState ports VM.inFinalState.
func (vm *VM) InFinalState() bool {
	state := vm.machineState()
	return state == virtualization.VZVirtualMachineStateStopped ||
		state == virtualization.VZVirtualMachineStatePaused ||
		state == virtualization.VZVirtualMachineStateError
}

func (vm *VM) machineState() virtualization.VZVirtualMachineState {
	var state virtualization.VZVirtualMachineState
	dispatch.RunOnMainThread(func() {
		state = vm.VirtualMachine.State()
	})
	return state
}

// NewVMInstallingFromIPSW ports the arm64-only VM.init(vmDir:ipswURL:…):
// creates NVRAM, disk and config from a restore image, then runs the
// automated macOS installation.
func NewVMInstallingFromIPSW(ctx context.Context, vmDir *vmdirectory.VMDirectory, ipswURL *foundation.NSURL,
	diskSizeGB uint16, diskFormat diskimage.DiskImageFormat, options VMOptions) (*VM, error) {
	ctx, span := otel.Tracer("weave").Start(ctx, "vm.create_from_ipsw",
		trace.WithAttributes(attribute.String("vm.name", vmDir.Name())))
	defer span.End()
	telemetry.OTelShared().Instruments.VMOperations.Add(ctx, 1,
		metric.WithAttributes(attribute.String("operation", "create"), attribute.String("vm.name", vmDir.Name())))

	options.normalize()

	if !ipswURL.IsFileURL() {
		remoteIPSW, err := VMRetrieveIPSW(ctx, ipswURL)
		if err != nil {
			return nil, err
		}
		ipswURL = remoteIPSW
	}

	// The Virtualization.Framework cannot deal with paths that contain
	// symlinks, so expand them first.
	ipswURL = ipswURL.URLByResolvingSymlinksInPath()

	// Load the restore image and get the requirements that match both the
	// image and our platform.
	image, err := loadMacOSRestoreImage(ctx, ipswURL)
	if err != nil {
		return nil, err
	}

	requirements := image.MostFeaturefulSupportedConfiguration()
	if requirements == nil {
		return nil, UnsupportedRestoreImageError{}
	}

	// Create NVRAM.
	if _, err := virtualization.VZMacAuxiliaryStorageFromID(objcutil.AllocClass("VZMacAuxiliaryStorage")).
		InitCreatingStorageAtURLHardwareModelOptionsError(vmDir.NvramURL(), requirements.HardwareModel(), 0); err != nil {
		return nil, err
	}

	// Create disk.
	if err := vmDir.ResizeDisk(diskSizeGB, diskFormat); err != nil {
		return nil, err
	}

	// Create config.
	ecid := virtualization.VZMacMachineIdentifierFromID(objcutil.AllocClass("VZMacMachineIdentifier")).Init()
	config := vmconfig.NewVMConfig(
		vmconfig.NewDarwinPlatform(ecid, requirements.HardwareModel()),
		int(requirements.MinimumSupportedCPUCount()),
		requirements.MinimumSupportedMemorySize(),
		nil,
		diskFormat,
	)
	// Allocate at least 4 CPUs because otherwise VMs are frequently freezing.
	if err := config.SetCPU(max(4, int(requirements.MinimumSupportedCPUCount()))); err != nil {
		return nil, err
	}
	if err := config.Save(vmDir.ConfigURL()); err != nil {
		return nil, err
	}

	topology, err := resolveTopology(config, options)
	if err != nil {
		return nil, err
	}

	configuration, err := craftConfiguration(vmDir.DiskURL(), vmDir.NvramURL(), config, options, topology)
	if err != nil {
		return nil, err
	}

	vm := &VM{
		Configuration: configuration,
		sema:          weavenetwork.NewAsyncSemaphore(),
		Name:          vmDir.Name(),
		Config:        config,
		network:       topology,
	}
	vm.attachVirtualMachine()

	// Run automated installation.
	if err := vm.install(ctx, ipswURL); err != nil {
		return nil, err
	}

	return vm, nil
}

// loadMacOSRestoreImage bridges VZMacOSRestoreImage.load(from:) through a
// manually built block.
func loadMacOSRestoreImage(ctx context.Context, ipswURL *foundation.NSURL) (*virtualization.VZMacOSRestoreImage, error) {
	type result struct {
		image *virtualization.VZMacOSRestoreImage
		err   error
	}
	resultCh := make(chan result, 1)

	block := objc.NewBlock(func(_ objc.Block, imageID objc.ID, errID objc.ID) {
		if errID != 0 {
			resultCh <- result{err: pureobjc.NSErrorToError(errID)}
			return
		}
		resultCh <- result{image: virtualization.VZMacOSRestoreImageFromID(pureobjc.Retain(imageID))}
	})
	objc.ID(objc.GetClass("VZMacOSRestoreImage")).Send(
		objc.RegisterName("loadFileURL:completionHandler:"), ipswURL.Ptr(), block)

	select {
	case r := <-resultCh:
		return r.image, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// install ports VM.install(_:): runs VZMacOSInstaller with progress logging.
func (vm *VM) install(ctx context.Context, ipswURL *foundation.NSURL) error {
	var installer *virtualization.VZMacOSInstaller
	dispatch.RunOnMainThread(func() {
		installer = virtualization.VZMacOSInstallerFromID(objcutil.AllocClass("VZMacOSInstaller")).
			InitWithVirtualMachineRestoreImageURL(vm.VirtualMachine, ipswURL)
	})

	logging.DefaultLogger().AppendNewLine("Installing OS...")
	observer := logging.NewProgressObserver(&logging.NSProgressWrapper{Inner: installer.Progress()})
	observer.Log(logging.DefaultLogger())

	errCh := make(chan error, 1)
	block := objc.NewBlock(func(_ objc.Block, errID objc.ID) {
		if errID != 0 {
			errCh <- pureobjc.NSErrorToError(errID)
		} else {
			errCh <- nil
		}
	})
	dispatch.RunOnMainThread(func() {
		installer.Ptr().Send(objc.RegisterName("installWithCompletionHandler:"), block)
	})

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		installer.Progress().Cancel()
		return <-errCh
	}
}

// VMLinux ports VM.linux(vmDir:diskSizeGB:diskFormat:).
func VMLinux(vmDir *vmdirectory.VMDirectory, diskSizeGB uint16, diskFormat diskimage.DiskImageFormat) (*VM, error) {
	// Create NVRAM.
	if _, err := virtualization.VZEFIVariableStoreFromID(objcutil.AllocClass("VZEFIVariableStore")).
		InitCreatingVariableStoreAtURLOptionsError(vmDir.NvramURL(), 0); err != nil {
		return nil, err
	}

	// Create disk.
	if err := vmDir.ResizeDisk(diskSizeGB, diskFormat); err != nil {
		return nil, err
	}

	// Create config.
	config := vmconfig.NewVMConfig(&vmconfig.LinuxPlatform{}, 4, 4096*1024*1024, nil, diskFormat)
	if err := config.Save(vmDir.ConfigURL()); err != nil {
		return nil, err
	}

	return NewVM(vmDir, VMOptions{})
}

// Start ports VM.start(recovery:resume:).
func (vm *VM) Start(recovery bool, shouldResume bool) error {
	ctx, span := otel.Tracer("weave").Start(context.Background(), "vm.start",
		trace.WithAttributes(
			attribute.String("vm.name", vm.Name),
			attribute.Bool("vm.recovery", recovery),
		))
	defer span.End()
	telemetry.OTelShared().Instruments.VMOperations.Add(ctx, 1,
		metric.WithAttributes(attribute.String("operation", "start"), attribute.String("vm.name", vm.Name)))

	if err := vm.network.Run(vm.sema); err != nil {
		span.RecordError(err)
		return err
	}

	if shouldResume {
		return vm.resumeMachine()
	}
	return vm.startMachine(recovery)
}

// Connect ports VM.connect(toPort:); it satisfies the controlsocket.VirtioSocketConnector
// interface used by ControlSocket.
func (vm *VM) Connect(ctx context.Context, toPort uint32) (*virtualization.VZVirtioSocketConnection, error) {
	var socketDeviceID objc.ID
	dispatch.RunOnMainThread(func() {
		devices := vm.VirtualMachine.SocketDevices()
		if devices != nil && objc.Send[uint](devices.Ptr(), objcutil.SelCount) > 0 {
			socketDeviceID = objc.Send[objc.ID](devices.Ptr(), objcutil.SelObjectAtIndex, uint(0))
		}
	})

	if socketDeviceID == 0 {
		return nil, weaveerrors.ErrVMSocketFailed(toPort, ", VM has no socket devices configured")
	}

	isVirtio := objc.Send[bool](socketDeviceID, objc.RegisterName("isKindOfClass:"),
		objc.GetClass("VZVirtioSocketDevice"))
	if !isVirtio {
		return nil, weaveerrors.ErrVMSocketFailed(toPort, ", expected VM's first socket device to have a type of VZVirtioSocketDevice")
	}

	type result struct {
		connection *virtualization.VZVirtioSocketConnection
		err        error
	}
	resultCh := make(chan result, 1)
	block := objc.NewBlock(func(_ objc.Block, connectionID objc.ID, errID objc.ID) {
		if errID != 0 {
			resultCh <- result{err: pureobjc.NSErrorToError(errID)}
			return
		}
		resultCh <- result{connection: virtualization.VZVirtioSocketConnectionFromID(pureobjc.Retain(connectionID))}
	})
	dispatch.RunOnMainThread(func() {
		socketDeviceID.Send(objc.RegisterName("connectToPort:completionHandler:"), toPort, block)
	})

	select {
	case r := <-resultCh:
		return r.connection, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Run ports VM.run(): waits for the delegate (or cancellation), stops the VM
// gracefully when cancelled, then stops the network.
func (vm *VM) Run(ctx context.Context) error {
	// A cancellation here is triggered by "weave stop", Ctrl+C, or closing
	// the VM window, so shut down the VM gracefully below.
	_ = vm.sema.WaitUnlessCancelled(ctx)

	if ctx.Err() != nil {
		if vm.machineState() == virtualization.VZVirtualMachineStateRunning {
			fmt.Println("Stopping VM...")
			if err := vm.stopMachine(); err != nil {
				return err
			}
		}
	}

	return vm.network.Stop()
}

// startMachine ports the @MainActor VM.start(_ recovery:).
func (vm *VM) startMachine(recovery bool) error {
	errCh := make(chan error, 1)
	block := objc.NewBlock(func(_ objc.Block, errID objc.ID) {
		if errID != 0 {
			errCh <- pureobjc.NSErrorToError(errID)
		} else {
			errCh <- nil
		}
	})

	dispatch.RunOnMainThread(func() {
		startOptions := idiomatic.NewMacOSVirtualMachineStartOptions().Unwrap()
		startOptions.SetStartUpFromMacOSRecovery(recovery)
		vm.VirtualMachine.Ptr().Send(
			objc.RegisterName("startWithOptions:completionHandler:"), startOptions.Ptr(), block)
	})

	return <-errCh
}

// resumeMachine ports the @MainActor VM.resume().
func (vm *VM) resumeMachine() error {
	return vm.SendErrorCompletion("resumeWithCompletionHandler:")
}

// stopMachine ports the @MainActor VM.stop().
func (vm *VM) stopMachine() error {
	return vm.SendErrorCompletion("stopWithCompletionHandler:")
}

func (vm *VM) SendErrorCompletion(selector string) error {
	errCh := make(chan error, 1)
	block := objc.NewBlock(func(_ objc.Block, errID objc.ID) {
		if errID != 0 {
			errCh <- pureobjc.NSErrorToError(errID)
		} else {
			errCh <- nil
		}
	})
	dispatch.RunOnMainThread(func() {
		vm.VirtualMachine.Ptr().Send(objc.RegisterName(selector), block)
	})
	return <-errCh
}

// craftConfiguration ports VM.craftConfiguration(…).
func craftConfiguration(diskURL *foundation.NSURL, nvramURL *foundation.NSURL,
	vmConfig *vmconfig.VMConfig, options VMOptions, topology *weavenetwork.Topology) (*virtualization.VZVirtualMachineConfiguration, error) {
	configuration := idiomatic.NewVirtualMachineConfiguration().Unwrap()

	// Boot loader.
	bootLoader, err := vmConfig.Platform.BootLoader(nvramURL)
	if err != nil {
		return nil, err
	}
	configuration.SetBootLoader(bootLoader)

	// CPU and memory.
	configuration.SetCPUCount(uint(vmConfig.CPUCount))
	configuration.SetMemorySize(vmConfig.MemorySize)

	// vmconfig.Platform.
	platform, err := vmConfig.Platform.Platform(nvramURL, options.Nested)
	if err != nil {
		return nil, err
	}
	configuration.SetPlatform(platform)

	// Display.
	graphicsDevice := vmConfig.Platform.GraphicsDevice(vmConfig)
	configuration.SetGraphicsDevices(objcutil.NSArrayFromIDs[*virtualization.VZGraphicsDeviceConfiguration](graphicsDevice.Ptr()))

	// Audio.
	soundDeviceConfiguration := idiomatic.NewVirtioSoundDeviceConfiguration().Unwrap()
	if !options.NoAudio && !options.Suspendable {
		inputStream := idiomatic.NewVirtioSoundDeviceInputStreamConfiguration().Unwrap()
		outputStream := idiomatic.NewVirtioSoundDeviceOutputStreamConfiguration().Unwrap()

		inputStream.SetSource(&idiomatic.NewHostAudioInputStreamSource().Unwrap().VZAudioInputStreamSource)
		outputStream.SetSink(&idiomatic.NewHostAudioOutputStreamSink().Unwrap().VZAudioOutputStreamSink)

		soundDeviceConfiguration.SetStreams(
			objcutil.NSArrayFromIDs[*virtualization.VZVirtioSoundDeviceStreamConfiguration](inputStream.Ptr(), outputStream.Ptr()))
	} else {
		// Just a null speaker.
		outputStream := idiomatic.NewVirtioSoundDeviceOutputStreamConfiguration().Unwrap()
		soundDeviceConfiguration.SetStreams(
			objcutil.NSArrayFromIDs[*virtualization.VZVirtioSoundDeviceStreamConfiguration](outputStream.Ptr()))
	}
	configuration.SetAudioDevices(objcutil.NSArrayFromIDs[*virtualization.VZAudioDeviceConfiguration](soundDeviceConfiguration.Ptr()))

	// Keyboard and mouse.
	suspendablePlatform, isSuspendable := vmConfig.Platform.(vmconfig.PlatformSuspendable)
	if options.Suspendable && isSuspendable {
		configuration.SetKeyboards(keyboardArray(suspendablePlatform.KeyboardsSuspendable()))
		configuration.SetPointingDevices(pointingDeviceArray(suspendablePlatform.PointingDevicesSuspendable()))
	} else {
		if options.NoKeyboard {
			configuration.SetKeyboards(objcutil.EmptyNSArray[*virtualization.VZKeyboardConfiguration]())
		} else {
			configuration.SetKeyboards(keyboardArray(vmConfig.Platform.Keyboards()))
		}

		switch {
		case options.NoPointer:
			configuration.SetPointingDevices(objcutil.EmptyNSArray[*virtualization.VZPointingDeviceConfiguration]())
		case options.NoTrackpad:
			configuration.SetPointingDevices(pointingDeviceArray(vmConfig.Platform.PointingDevicesSimplified()))
		default:
			configuration.SetPointingDevices(pointingDeviceArray(vmConfig.Platform.PointingDevices()))
		}
	}

	// Networking: one VZVirtioNetworkDeviceConfiguration per NIC, each with its
	// own attachment and MAC address (multi-NIC with per-NIC properties).
	networkDeviceIDs := make([]objc.ID, 0, len(topology.NICs()))
	for _, nic := range topology.NICs() {
		vio := idiomatic.NewVirtioNetworkDeviceConfiguration().Unwrap()
		vio.SetAttachment(nic.Attachment)
		vio.SetMACAddress(nic.MAC)
		networkDeviceIDs = append(networkDeviceIDs, vio.Ptr())
	}
	configuration.SetNetworkDevices(objcutil.NSArrayFromIDs[*virtualization.VZNetworkDeviceConfiguration](networkDeviceIDs...))

	consoleDeviceIDs := make([]objc.ID, 0, 2)

	// Clipboard sharing via Spice agent. Skipped when the enterprise clipboard
	// engine owns the clipboard, so its policy is the single source of truth.
	if !options.NoClipboard && !options.ClipboardPolicyEnabled {
		spiceAgentConsoleDevice := idiomatic.NewVirtioConsoleDeviceConfiguration().Unwrap()
		spiceAgentPort := idiomatic.NewVirtioConsolePortConfiguration().Unwrap()
		spiceAgentPort.SetName(virtualization.VZSpiceAgentPortAttachmentSpiceAgentPortName())
		spiceAgentPortAttachment := idiomatic.NewSpiceAgentPortAttachment().Unwrap()
		spiceAgentPortAttachment.SetSharesClipboard(true)
		spiceAgentPort.SetAttachment(&spiceAgentPortAttachment.VZSerialPortAttachment)
		setConsolePort(spiceAgentConsoleDevice, 0, spiceAgentPort)
		consoleDeviceIDs = append(consoleDeviceIDs, spiceAgentConsoleDevice.Ptr())
	}

	// Storage.
	cachingMode := virtualization.VZDiskImageCachingModeAutomatic
	if vmConfig.OS == weaveplatform.OSLinux {
		// When not specified, use "cached" caching mode for Linux VMs to
		// prevent file-system corruption (cirruslabs/tart#675).
		cachingMode = virtualization.VZDiskImageCachingModeCached
	}
	if options.Caching != nil {
		cachingMode = *options.Caching
	}
	attachment, err := virtualization.VZDiskImageStorageDeviceAttachmentFromID(objcutil.AllocClass("VZDiskImageStorageDeviceAttachment")).
		InitWithURLReadOnlyCachingModeSynchronizationModeError(diskURL, false, cachingMode, options.Sync)
	if err != nil {
		return nil, err
	}

	deviceIDs := []objc.ID{
		virtualization.VZVirtioBlockDeviceConfigurationFromID(objcutil.AllocClass("VZVirtioBlockDeviceConfiguration")).
			InitWithAttachment(&attachment.VZStorageDeviceAttachment).Ptr(),
	}
	for _, device := range options.AdditionalStorageDevices {
		deviceIDs = append(deviceIDs, device.Ptr())
	}
	configuration.SetStorageDevices(objcutil.NSArrayFromIDs[*virtualization.VZStorageDeviceConfiguration](deviceIDs...))

	// Entropy.
	if !options.Suspendable {
		entropy := idiomatic.NewVirtioEntropyDeviceConfiguration().Unwrap()
		configuration.SetEntropyDevices(objcutil.NSArrayFromIDs[*virtualization.VZEntropyDeviceConfiguration](entropy.Ptr()))
	}

	// Directory sharing devices.
	sharingIDs := make([]objc.ID, 0, len(options.DirectorySharingDevices))
	for _, device := range options.DirectorySharingDevices {
		sharingIDs = append(sharingIDs, device.Ptr())
	}
	configuration.SetDirectorySharingDevices(objcutil.NSArrayFromIDs[*virtualization.VZDirectorySharingDeviceConfiguration](sharingIDs...))

	// Serial ports.
	serialIDs := make([]objc.ID, 0, len(options.SerialPorts))
	for _, port := range options.SerialPorts {
		serialIDs = append(serialIDs, port.Ptr())
	}
	configuration.SetSerialPorts(objcutil.NSArrayFromIDs[*virtualization.VZSerialPortConfiguration](serialIDs...))

	// Version console device: a dummy console device useful for implementing
	// host feature checks in the guest agent software. The "tart-version-"
	// port name is a wire contract — the Tart Guest Agent running inside
	// guest images discovers the host by this exact prefix, so it must not
	// be renamed to weave.
	consolePort := idiomatic.NewVirtioConsolePortConfiguration().Unwrap()
	consolePort.SetName(objcutil.NSStr("tart-version-" + ci.CIVersion()))
	consoleDevice := idiomatic.NewVirtioConsoleDeviceConfiguration().Unwrap()
	setConsolePort(consoleDevice, 0, consolePort)
	consoleDeviceIDs = append(consoleDeviceIDs, consoleDevice.Ptr())

	configuration.SetConsoleDevices(objcutil.NSArrayFromIDs[*virtualization.VZConsoleDeviceConfiguration](consoleDeviceIDs...))

	// Socket device.
	socketDevice := idiomatic.NewVirtioSocketDeviceConfiguration().Unwrap()
	configuration.SetSocketDevices(objcutil.NSArrayFromIDs[*virtualization.VZSocketDeviceConfiguration](socketDevice.Ptr()))

	if _, err := configuration.ValidateWithError(); err != nil {
		return nil, err
	}

	return configuration, nil
}

// setConsolePort mirrors Swift's consoleDevice.ports[0] = port subscript.
func setConsolePort(device *virtualization.VZVirtioConsoleDeviceConfiguration, index uint, port *virtualization.VZVirtioConsolePortConfiguration) {
	ports := device.Ports()
	ports.Ptr().Send(objc.RegisterName("setObject:atIndexedSubscript:"), port.Ptr(), index)
}

func keyboardArray(keyboards []*virtualization.VZKeyboardConfiguration) *foundation.NSArray[*virtualization.VZKeyboardConfiguration] {
	ids := make([]objc.ID, 0, len(keyboards))
	for _, keyboard := range keyboards {
		ids = append(ids, keyboard.Ptr())
	}
	return objcutil.NSArrayFromIDs[*virtualization.VZKeyboardConfiguration](ids...)
}

func pointingDeviceArray(devices []*virtualization.VZPointingDeviceConfiguration) *foundation.NSArray[*virtualization.VZPointingDeviceConfiguration] {
	ids := make([]objc.ID, 0, len(devices))
	for _, device := range devices {
		ids = append(ids, device.Ptr())
	}
	return objcutil.NSArrayFromIDs[*virtualization.VZPointingDeviceConfiguration](ids...)
}
