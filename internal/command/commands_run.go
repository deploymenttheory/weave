// Port of tart's Commands/Run.swift: boots a VM, optionally with a UI
// window, VNC, additional disks, directory shares and custom networking.
// The SwiftUI MainApp becomes a plain AppKit window hosting a
// VZVirtualMachineView; menus are reduced to the essentials.
//go:build darwin

package command

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/deploymenttheory/weave/internal/logging"
	"github.com/deploymenttheory/weave/internal/unattended"
	"github.com/deploymenttheory/weave/internal/vmconfig"

	"github.com/deploymenttheory/weave/internal/clipboard"
	"github.com/deploymenttheory/weave/internal/clipboardpolicy"
	weaveconfig "github.com/deploymenttheory/weave/internal/config"
	"github.com/deploymenttheory/weave/internal/controlsocket"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/fetcher"
	weavelock "github.com/deploymenttheory/weave/internal/lock"
	"github.com/deploymenttheory/weave/internal/macaddress"
	weavenetwork "github.com/deploymenttheory/weave/internal/network"
	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/oci"
	weaveplatform "github.com/deploymenttheory/weave/internal/platform"
	"github.com/deploymenttheory/weave/internal/screenviewer"
	"github.com/deploymenttheory/weave/internal/telemetry"
	"github.com/deploymenttheory/weave/internal/terminal"
	weavevm "github.com/deploymenttheory/weave/internal/vm"
	"github.com/deploymenttheory/weave/internal/vmdirectory"
	"github.com/deploymenttheory/weave/internal/vmstorage"
	weavevnc "github.com/deploymenttheory/weave/internal/vnc"

	"github.com/ebitengine/purego/objc"

	appkit "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/appkit"
	corefoundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/corefoundation"
	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	virtualization "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/virtualization"
	dispatch "github.com/deploymenttheory/go-bindings-macosplatform/internal/objc"
	"github.com/deploymenttheory/go-bindings-macosplatform/internal/pureobjc"
	"github.com/deploymenttheory/go-bindings-macosplatform/internal/pureobjc/objcerrors"
)

// vm ports tart's global `var vm: VM?` from Run.swift.
var vm *weavevm.VM

// parseDiskImageSynchronizationMode ports the VZDiskImageSynchronizationMode
// init(_ description:) extension.
func parseDiskImageSynchronizationMode(description string) (virtualization.VZDiskImageSynchronizationMode, error) {
	switch description {
	case "none":
		return virtualization.VZDiskImageSynchronizationModeNone, nil
	case "fsync":
		return virtualization.VZDiskImageSynchronizationModeFsync, nil
	case "full", "":
		return virtualization.VZDiskImageSynchronizationModeFull, nil
	default:
		return 0, weaveerrors.ErrVMConfigurationError("unsupported disk image synchronization mode: %q", description)
	}
}

// parseDiskSynchronizationMode ports the VZDiskSynchronizationMode
// init(_ description:) extension.
func parseDiskSynchronizationMode(description string) (virtualization.VZDiskSynchronizationMode, error) {
	switch description {
	case "none":
		return virtualization.VZDiskSynchronizationModeNone, nil
	case "full", "":
		return virtualization.VZDiskSynchronizationModeFull, nil
	default:
		return 0, weaveerrors.ErrVMConfigurationError("unsupported disk synchronization mode: %q", description)
	}
}

// parseDiskImageCachingMode ports the VZDiskImageCachingMode
// init?(_ description:) extension; ok=false mirrors the nil return for "".
func parseDiskImageCachingMode(description string) (virtualization.VZDiskImageCachingMode, bool, error) {
	switch description {
	case "automatic":
		return virtualization.VZDiskImageCachingModeAutomatic, true, nil
	case "cached":
		return virtualization.VZDiskImageCachingModeCached, true, nil
	case "uncached":
		return virtualization.VZDiskImageCachingModeUncached, true, nil
	case "":
		return 0, false, nil
	default:
		return 0, false, weaveerrors.ErrVMConfigurationError("unsupported disk image caching mode: %q", description)
	}
}

// RunCommand ports the Run command.
type RunCommand struct {
	Name              string
	NoGraphics        bool
	Serial            bool
	SerialPath        string
	Graphics          bool
	NoAudio           bool
	NoClipboard       bool
	Clipboard         bool
	ClipboardUser     string
	ClipboardPassword string
	// Enterprise clipboard policy overrides (empty/zero = inherit from the VM
	// config, then the settings default, then the built-in default).
	ClipboardDirection    string // disabled|bidirectional|hostToGuest|guestToHost
	ClipboardFormats      string // csv of text,rich,image
	ClipboardFiles        string // on|off
	ClipboardSessionMbps  int
	ClipboardBandwidthPct int
	ClipboardMaxBytes     int64

	// Resolved in RunMainThread, consumed in driveVM.
	clipboardPolicy   clipboardpolicy.Policy
	clipboardRun      bool
	guestGOOS         string
	guestGOARCH       string
	Recovery          bool
	VNC               bool
	VNCExperimental   bool
	VNCPassword       string
	Disk              []string
	RosettaTag        string
	Dir               []string
	SharedDir         []string
	USBStorage        []string
	Nested            bool
	NetProfile        string
	NetDevice         []string
	NetBridged        []string
	NetSoftnet        bool
	NetSoftnetAllow   string
	NetSoftnetBlock   string
	NetSoftnetExpose  string
	NetHost           bool
	RootDiskOpts      string
	Suspendable       bool
	CaptureSystemKeys bool
	NoTrackpad        bool
	NoPointer         bool
	NoKeyboard        bool
	ShowScreen        bool // serve a view-only browser viewer of the VM screen

	// primaryBridged records whether the resolved primary NIC is bridged, so
	// the VNC layer resolves the guest IP via ARP rather than DHCP. Set during
	// RunMainThread.
	primaryBridged bool
}

// Validate ports Run.validate().
func (c *RunCommand) Validate() error {
	if c.VNC && c.VNCExperimental {
		return weaveerrors.ErrGeneric("--vnc and --vnc-experimental are mutually exclusive")
	}

	// Automatically enable --net-softnet when any of its related options
	// are specified.
	if c.NetSoftnetAllow != "" || c.NetSoftnetBlock != "" || c.NetSoftnetExpose != "" {
		c.NetSoftnet = true
	}

	// Check that no more than one network option is specified.
	netFlags := 0
	if len(c.NetBridged) > 0 {
		netFlags++
	}
	if c.NetSoftnet {
		netFlags++
	}
	if c.NetHost {
		netFlags++
	}
	if netFlags > 1 {
		return weaveerrors.ErrGeneric("--net-bridged, --net-softnet and --net-host are mutually exclusive")
	}

	// The high-level --net-profile, the primitive --net-device list, and the
	// legacy --net-* flags are three mutually exclusive ways to select
	// networking.
	legacyNet := len(c.NetBridged) > 0 || c.NetSoftnet || c.NetHost
	surfaces := 0
	if c.NetProfile != "" {
		surfaces++
	}
	if len(c.NetDevice) > 0 {
		surfaces++
	}
	if legacyNet {
		surfaces++
	}
	if surfaces > 1 {
		return weaveerrors.ErrGeneric("--net-profile, --net-device and the legacy --net-* flags are mutually exclusive")
	}

	// Fail fast on a bad profile name or --net-device spec, before the heavy
	// VM boot, by resolving them up front.
	if c.NetProfile != "" {
		if _, err := weavenetwork.ExpandProfile(c.NetProfile, weavenetwork.ProfileOptions{}); err != nil {
			return err
		}
	}
	if len(c.NetDevice) > 0 {
		if _, err := weavenetwork.ParseNICDevices(c.NetDevice); err != nil {
			return err
		}
	}

	if c.Graphics && c.NoGraphics {
		return weaveerrors.ErrGeneric("--graphics and --no-graphics are mutually exclusive")
	}

	if (c.NoGraphics || c.VNC || c.VNCExperimental) && c.CaptureSystemKeys {
		return weaveerrors.ErrGeneric("--captures-system-keys can only be used with the default VM view")
	}

	if c.Nested {
		if !weaveplatform.MacOSAtLeast(15) {
			return weaveerrors.ErrGeneric("Nested virtualization is supported on hosts starting with macOS 15 (Sequoia), and later.")
		}
		if !virtualization.VZGenericPlatformConfigurationIsNestedVirtualizationSupported() {
			return weaveerrors.ErrGeneric("Nested virtualization is available for Mac with the M3 chip, and later.")
		}
	}

	localStorage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return err
	}
	vmDir, err := localStorage.Open(c.Name)
	if err != nil {
		return err
	}
	state, err := vmDir.State()
	if err != nil {
		return err
	}
	if state == vmdirectory.VMDirectoryStateSuspended {
		c.Suspendable = true
	}

	if c.Suspendable {
		config, err := vmconfig.NewVMConfigFromURL(vmDir.ConfigURL())
		if err != nil {
			return err
		}
		if _, ok := config.Platform.(vmconfig.PlatformSuspendable); !ok {
			return weaveerrors.ErrGeneric("You can only suspend macOS VMs")
		}

		if c.NoTrackpad {
			return weaveerrors.ErrGeneric("--no-trackpad cannot be used with --suspendable")
		}
		if c.NoKeyboard {
			return weaveerrors.ErrGeneric("--no-keyboard cannot be used with --suspendable")
		}
		if c.NoPointer {
			return weaveerrors.ErrGeneric("--no-pointer cannot be used with --suspendable")
		}
	}

	if c.NoTrackpad {
		config, err := vmconfig.NewVMConfigFromURL(vmDir.ConfigURL())
		if err != nil {
			return err
		}
		if config.OS != weaveplatform.OSDarwin {
			return weaveerrors.ErrGeneric("--no-trackpad can only be used with macOS VMs")
		}
	}

	for _, disk := range c.Disk {
		if strings.HasSuffix(disk, "-amd64.iso") {
			return weaveerrors.ErrGeneric("Seems you have a disk targeting x86 architecture (hence amd64 in the name). Please use an 'arm64' version of the disk.")
		}
	}

	return nil
}

// RunMainThread ports Run.runOnMainThread(); the caller must be on the
// process's main thread (it ends in NSApplication.run()).
func (c *RunCommand) RunMainThread() error {
	localStorage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return err
	}
	vmDir, err := localStorage.Open(c.Name)
	if err != nil {
		return err
	}

	// Validate disk format support.
	vmConfig, err := vmconfig.NewVMConfigFromURL(vmDir.ConfigURL())
	if err != nil {
		return err
	}
	if !vmConfig.DiskFormat.IsSupported() {
		return weaveerrors.ErrGeneric("Disk format '%s' is not supported on this system.", vmConfig.DiskFormat)
	}

	c.resolveClipboard(vmConfig)

	config, err := weaveconfig.NewConfig()
	if err != nil {
		return err
	}
	storageLock, err := weavelock.NewFileLock(config.WeaveHomeDir)
	if err != nil {
		return err
	}
	defer storageLock.Close()
	if err := storageLock.Lock(); err != nil {
		return err
	}

	// Check if there is a running VM with the same MAC address but a
	// different name.
	vmDirMAC, err := vmDir.MACAddress()
	if err != nil {
		return err
	}
	entries, err := localStorage.List()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		running, err := entry.VMDir.Running()
		if err != nil || !running {
			continue
		}
		mac, err := entry.VMDir.MACAddress()
		if err == nil && mac == vmDirMAC && entry.Name != vmDir.Name() {
			fmt.Println("There is already a running VM with the same MAC address!")
			fmt.Println("Resetting VM to assign a new MAC address...")
			if err := vmDir.RegenerateMACAddress(); err != nil {
				return err
			}
			break
		}
	}

	var serialPorts []*virtualization.VZSerialPortConfiguration
	if c.Serial {
		ttyFD := weavevm.CreatePTY()
		if ttyFD < 0 {
			return weaveerrors.ErrVMConfigurationError("Failed to create PTY")
		}
		ttyRead := foundation.NSFileHandleFromID(objcutil.AllocClass("NSFileHandle")).
			InitWithFileDescriptorCloseOnDealloc(ttyFD, false)
		ttyWrite := foundation.NSFileHandleFromID(objcutil.AllocClass("NSFileHandle")).
			InitWithFileDescriptorCloseOnDealloc(ttyFD, false)
		serialPorts = append(serialPorts, createSerialPortConfiguration(ttyRead, ttyWrite))
	} else if c.SerialPath != "" {
		ttyRead := foundation.NSFileHandleFileHandleForReadingAtPath(objcutil.NSStr(c.SerialPath))
		ttyWrite := foundation.NSFileHandleFileHandleForWritingAtPath(objcutil.NSStr(c.SerialPath))
		if ttyRead == nil || ttyWrite == nil {
			return weaveerrors.ErrVMConfigurationError("Failed to open PTY")
		}
		serialPorts = append(serialPorts, createSerialPortConfiguration(ttyRead, ttyWrite))
	}

	// Parse root disk options.
	diskOptions := parseDiskOptions(c.RootDiskOpts)
	syncMode, err := parseDiskImageSynchronizationMode(diskOptions.syncModeRaw)
	if err != nil {
		return err
	}
	var caching *virtualization.VZDiskImageCachingMode
	if cachingMode, ok, err := parseDiskImageCachingMode(diskOptions.cachingModeRaw); err != nil {
		return err
	} else if ok {
		caching = &cachingMode
	}

	nics, err := c.resolveNICs(vmDir)
	if err != nil {
		return err
	}
	c.primaryBridged = primaryNICIsBridged(nics)

	// Softnet needs the SUID bit (or passwordless sudo) before the helper can
	// be spawned; prompt for it interactively when any resolved NIC is softnet.
	if topologyNeedsSoftnet(nics) && isInteractiveSession() {
		if err := weavenetwork.SoftnetConfigureSUIDBitIfNeeded(); err != nil {
			return err
		}
	}

	additionalStorageDevices, err := c.additionalDiskAttachments()
	if err != nil {
		return err
	}
	usbStorageDevices, err := c.usbMassStorageDevices()
	if err != nil {
		return err
	}
	additionalStorageDevices = append(additionalStorageDevices, usbStorageDevices...)
	directorySharingDevices, err := c.directoryShares()
	if err != nil {
		return err
	}
	rosettaShares, err := c.rosettaDirectoryShare()
	if err != nil {
		return err
	}
	directorySharingDevices = append(directorySharingDevices, rosettaShares...)

	vm, err = weavevm.NewVM(vmDir, weavevm.VMOptions{
		NICs:                     nics,
		AdditionalStorageDevices: additionalStorageDevices,
		DirectorySharingDevices:  directorySharingDevices,
		SerialPorts:              serialPorts,
		Suspendable:              c.Suspendable,
		Nested:                   c.Nested,
		NoAudio:                  c.NoAudio,
		NoClipboard:              c.NoClipboard,
		ClipboardPolicyEnabled:   c.clipboardRun,
		Sync:                     syncMode,
		Caching:                  caching,
		NoTrackpad:               c.NoTrackpad,
		NoPointer:                c.NoPointer,
		NoKeyboard:               c.NoKeyboard,
	})
	if err != nil {
		return err
	}
	// Publish the VM for control socket clients (the monolith's package
	// global; the controlsocket package now receives it explicitly).
	controlsocket.SetConnector(vm)

	var vncImpl weavevnc.VNC
	switch {
	case c.VNC:
		vncImpl = weavevnc.NewScreenSharingVNC(vmConfig)
	case c.VNCExperimental:
		vncImpl = weavevnc.NewFullFledgedVNC(vm, c.VNCPassword)
	}

	// Lock the VM. More specifically, lock "config.json", because we can't
	// lock directories with fcntl(2)-based locking and we'd better not
	// interfere with the VM's disk and NVRAM (they are opened directly by
	// the Virtualization.Framework's process).
	lock, err := vmDir.Lock()
	if err != nil {
		return err
	}
	acquired, err := lock.Trylock()
	if err != nil {
		return err
	}
	if !acquired {
		return weaveerrors.ErrVMAlreadyRunning("VM \"%s\" is already running!", c.Name)
	}

	// Now the VM state will return "running", so we can unlock.
	if err := storageLock.Unlock(); err != nil {
		return err
	}

	runCtx, cancelRun := context.WithCancel(context.Background())

	go c.driveVM(runCtx, localStorage, vmDir, vncImpl)

	// "weave stop" support.
	sigint := make(chan os.Signal, 1)
	signal.Notify(sigint, syscall.SIGINT)
	go func() {
		<-sigint
		cancelRun()
	}()

	// "weave suspend" / UI window closing support.
	sigusr1 := make(chan os.Signal, 1)
	signal.Notify(sigusr1, syscall.SIGUSR1)
	go func() {
		for range sigusr1 {
			c.suspendVM(vmDir, cancelRun)
		}
	}()

	// Graceful shutdown support: for macOS guests this brings up a dialog
	// asking the user if they are sure they want to shut down.
	sigusr2 := make(chan os.Signal, 1)
	signal.Notify(sigusr2, syscall.SIGUSR2)
	go func() {
		for range sigusr2 {
			fmt.Println("Requesting guest OS to stop...")
			dispatch.RunOnMainThread(func() {
				_, _ = vm.VirtualMachine.RequestStopWithError()
			})
		}
	}()

	runSuspendableFlag.Store(c.Suspendable)

	useVNCWithoutGraphics := (c.VNC || c.VNCExperimental) && !c.Graphics
	if c.NoGraphics || useVNCWithoutGraphics {
		// Enter the main event loop without bringing up any UI, waiting for
		// the VM to exit.
		app := appkit.NSApplicationSharedApplication()
		app.SetActivationPolicy(appkit.NSApplicationActivationPolicyProhibited)
		app.Run()
	} else {
		c.runUI()
	}

	return nil
}

// driveVM ports the inner Task of Run.runOnMainThread(): restores a
// snapshot if present, starts the VM, brings up VNC and the control socket,
// then waits for the VM to finish.
func (c *RunCommand) driveVM(ctx context.Context, localStorage *vmstorage.VMStorageLocal, vmDir *vmdirectory.VMDirectory, vncImpl weavevnc.VNC) {
	fail := func(err error) {
		fmt.Fprintln(os.Stderr, err)
		telemetry.OTelShared().Flush()
		os.Exit(1)
	}

	resume := false
	if weaveplatform.MacOSAtLeast(14) &&
		foundation.NSFileManagerDefaultManager().FileExistsAtPath(vmDir.StateURL().Path()) {
		fmt.Println("restoring VM state from a snapshot...")
		if err := vm.RestoreMachineStateFrom(vmDir.StateURL()); err != nil {
			fail(err)
			return
		}
		if _, err := foundation.NSFileManagerDefaultManager().RemoveItemAtURLError(vmDir.StateURL()); err != nil {
			fail(err)
			return
		}
		resume = true
		fmt.Println("resuming VM...")
	}

	if err := vm.Start(c.Recovery, resume); err != nil {
		var objcErr *objcerrors.ObjCError
		if errors.As(err, &objcErr) && objcErr.Domain == "VZErrorDomain" &&
			objcErr.Code == int64(virtualization.VZErrorVirtualMachineLimitExceeded) {
			hint := ""
			if entries, listErr := localStorage.List(); listErr == nil {
				var runningVMs []string
				for _, entry := range entries {
					if running, err := entry.VMDir.Running(); err == nil && running {
						runningVMs = append(runningVMs, entry.Name)
					}
				}
				if len(runningVMs) > 0 {
					hint = " (other running VMs: " + strings.Join(runningVMs, ", ") + ")"
				}
			}
			fail(weaveerrors.ErrVirtualMachineLimitExceeded(hint))
			return
		}
		fail(err)
		return
	}

	// Enterprise clipboard engine (policy-driven, via the guest agent). Resolved
	// in RunMainThread; when active it owns the clipboard and the SPICE agent
	// clipboard is disabled (see VMOptions.ClipboardPolicyEnabled).
	if c.clipboardRun {
		if mac, err := vmDir.MACAddress(); err == nil {
			if vmMAC, ok := macaddress.NewMACAddress(mac); ok {
				engine := clipboard.NewEngine(c.clipboardPolicy, c.Name, vmDir, vmMAC,
					c.ClipboardUser, c.ClipboardPassword, c.guestGOOS, c.guestGOARCH)
				go engine.Run(ctx)
			}
		}
	}

	if vncImpl != nil {
		vncURL, err := vncImpl.WaitForURL(ctx, c.primaryBridged)
		if err != nil {
			fail(err)
			return
		}

		// Record the VNC endpoint so other processes (the MCP screen tools)
		// can connect to drive or view this VM by name; clear it on exit.
		endpointPath := vmDir.VNCEndpointPath()
		_ = os.WriteFile(endpointPath, []byte(objcutil.GoStr(vncURL.AbsoluteString())), 0o600)
		defer os.Remove(endpointPath)

		_, onCI := objcutil.EnvironmentValue("CI")
		if c.NoGraphics || onCI || c.ShowScreen {
			fmt.Printf("VNC server is running at %s\n", objcutil.GoStr(vncURL.AbsoluteString()))
		} else {
			fmt.Printf("Opening %s...\n", objcutil.GoStr(vncURL.AbsoluteString()))
			appkit.NSWorkspaceSharedWorkspace().OpenURL(vncURL)
		}

		// View-only screen viewer: a dedicated VNC client continuously
		// captures the screen and serves it as MJPEG to a browser, with no
		// path for the operator to send input into the guest.
		if c.ShowScreen {
			if match := unattended.VNCURLPattern.FindStringSubmatch(objcutil.GoStr(vncURL.AbsoluteString())); match != nil {
				if viewerPort, convErr := strconv.Atoi(match[3]); convErr == nil {
					if server, srvErr := screenviewer.NewScreenServer(); srvErr == nil {
						go screenviewer.StreamVNCToViewer(ctx, match[2], viewerPort, match[1], server)
						fmt.Printf("View-only screen: open %s in a browser to watch (no input reaches the VM).\n", server.URL())
						screenviewer.OpenInBrowser(server.URL())
					}
				}
			}
		}
	}

	if weaveplatform.MacOSAtLeast(14) {
		go func() {
			controlSocket := controlsocket.NewControlSocket(vmDir.ControlSocketURL())
			_ = controlSocket.Run(ctx)
		}()
	}

	if err := vm.Run(ctx); err != nil {
		fail(err)
		return
	}

	if vncImpl != nil {
		if err := vncImpl.Stop(); err != nil {
			fail(err)
			return
		}
	}

	telemetry.OTelShared().Flush()
	os.Exit(0)
}

// suspendVM ports the SIGUSR1 handler: snapshot the VM and shut down.
// resolveClipboard computes the effective enterprise clipboard policy for this
// run (CLI flags > per-VM config > settings default > built-in default) and
// records whether the engine should run plus the guest's OS/arch for agent
// deployment. The engine runs when --clipboard is passed or a policy is
// configured (per-VM or settings), and the resolved policy is active.
func (c *RunCommand) resolveClipboard(vmConfig *vmconfig.VMConfig) {
	override := clipboardpolicy.Override{}
	if c.Clipboard {
		enabled := true
		override.Enabled = &enabled
	}
	if c.ClipboardDirection != "" {
		direction := clipboardpolicy.Direction(c.ClipboardDirection)
		override.Direction = &direction
	}
	if c.ClipboardFormats != "" {
		set := parseCSVSet(c.ClipboardFormats)
		plain, rich, image := set["text"], set["rich"], set["image"]
		override.PlainText, override.RichText, override.Image = &plain, &rich, &image
	}
	if c.ClipboardFiles != "" {
		files := isOn(c.ClipboardFiles)
		override.FileTransfer = &files
	}
	if c.ClipboardSessionMbps > 0 {
		override.SessionMbps = &c.ClipboardSessionMbps
	}
	if c.ClipboardBandwidthPct > 0 {
		override.BandwidthPct = &c.ClipboardBandwidthPct
	}
	if c.ClipboardMaxBytes > 0 {
		override.MaxContentBytes = &c.ClipboardMaxBytes
	}

	var settingsDefault *clipboardpolicy.Policy
	if settings, err := weaveconfig.LoadSettings(); err == nil {
		settingsDefault = settings.DefaultClipboardPolicy
	}
	perVM := vmConfig.ClipboardPolicy

	policy := clipboardpolicy.Resolve(settingsDefault, perVM, override)
	c.clipboardPolicy = policy
	c.clipboardRun = (c.Clipboard || perVM != nil || settingsDefault != nil) && policy.Active()
	c.guestGOOS = string(vmConfig.OS)
	c.guestGOARCH = string(vmConfig.Arch)
}

func parseCSVSet(csv string) map[string]bool {
	set := map[string]bool{}
	for field := range strings.SplitSeq(csv, ",") {
		if field = strings.TrimSpace(field); field != "" {
			set[strings.ToLower(field)] = true
		}
	}
	return set
}

func isOn(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "true", "1", "yes", "enable", "enabled":
		return true
	default:
		return false
	}
}

func (c *RunCommand) suspendVM(vmDir *vmdirectory.VMDirectory, cancelRun context.CancelFunc) {
	if !weaveplatform.MacOSAtLeast(14) {
		fmt.Println(weaveerrors.ErrSuspendFailed("this functionality is only supported on macOS 14 (Sonoma) or newer"))
		telemetry.OTelShared().Flush()
		os.Exit(1)
	}

	var validateErr error
	dispatch.RunOnMainThread(func() {
		_, validateErr = vm.Configuration.ValidateSaveRestoreSupportWithError()
	})
	if validateErr == nil {
		fmt.Println("pausing VM to take a snapshot...")
		validateErr = vm.SendErrorCompletion("pauseWithCompletionHandler:")
	}
	if validateErr == nil {
		fmt.Println("creating a snapshot...")
		validateErr = vm.SaveMachineStateTo(vmDir.StateURL())
	}
	if validateErr != nil {
		fmt.Println(weaveerrors.ErrSuspendFailed(validateErr.Error()))
		telemetry.OTelShared().Flush()
		os.Exit(1)
	}

	fmt.Println("snapshot created successfully! shutting down the VM...")
	cancelRun()
}

func createSerialPortConfiguration(ttyRead *foundation.NSFileHandle, ttyWrite *foundation.NSFileHandle) *virtualization.VZSerialPortConfiguration {
	serialPortConfiguration := virtualization.VZVirtioConsoleDeviceSerialPortConfigurationFromID(
		objcutil.AllocClass("VZVirtioConsoleDeviceSerialPortConfiguration")).Init()
	serialPortAttachment := virtualization.VZFileHandleSerialPortAttachmentFromID(
		objcutil.AllocClass("VZFileHandleSerialPortAttachment")).
		InitWithFileHandleForReadingFileHandleForWriting(ttyRead, ttyWrite)
	serialPortConfiguration.SetAttachment(&serialPortAttachment.VZSerialPortAttachment)
	return &serialPortConfiguration.VZSerialPortConfiguration
}

func isInteractiveSession() bool {
	return terminal.TermIsTerminal()
}

// resolveNICs resolves the run's network topology into a concrete NIC list.
// Precedence: --net-profile, then --net-device, then the legacy --net-* flags,
// then the VM config's persisted NICs (a single NAT NIC for legacy configs).
func (c *RunCommand) resolveNICs(vmDir *vmdirectory.VMDirectory) ([]vmconfig.NICConfig, error) {
	switch {
	case c.NetProfile != "":
		return weavenetwork.ExpandProfile(c.NetProfile, weavenetwork.ProfileOptions{
			BridgedInterface: firstOrEmpty(c.NetBridged),
			SoftnetExpose:    c.NetSoftnetExpose,
		})

	case len(c.NetDevice) > 0:
		return weavenetwork.ParseNICDevices(c.NetDevice)

	case len(c.NetBridged) > 0:
		nics := make([]vmconfig.NICConfig, 0, len(c.NetBridged))
		for i, name := range c.NetBridged {
			if _, found := weavenetwork.FindBridgedInterface(name); !found {
				return nil, weaveerrors.ErrGeneric("no bridge interfaces matched %q, available interfaces: %s",
					name, strings.Join(weavenetwork.BridgeInterfaces(), ", "))
			}
			nics = append(nics, vmconfig.NICConfig{
				Mode:             vmconfig.NICModeBridged,
				IsPrimary:        i == 0,
				BridgedInterface: name,
			})
		}
		return nics, nil

	case c.NetSoftnet, c.NetHost:
		return []vmconfig.NICConfig{{
			Mode:            vmconfig.NICModeSoftnet,
			IsPrimary:       true,
			SoftnetHostMode: c.NetHost,
			SoftnetAllow:    c.NetSoftnetAllow,
			SoftnetBlock:    c.NetSoftnetBlock,
			SoftnetExpose:   c.NetSoftnetExpose,
		}}, nil

	default:
		config, err := vmconfig.NewVMConfigFromURL(vmDir.ConfigURL())
		if err != nil {
			return nil, err
		}
		return config.EnsureNICs(), nil
	}
}

// primaryNICIsBridged reports whether the primary NIC is bridged, so the VNC
// layer resolves the guest IP via ARP rather than DHCP.
func primaryNICIsBridged(nics []vmconfig.NICConfig) bool {
	for _, nic := range nics {
		if nic.IsPrimary {
			return nic.Mode == vmconfig.NICModeBridged
		}
	}
	return len(nics) > 0 && nics[0].Mode == vmconfig.NICModeBridged
}

// topologyNeedsSoftnet reports whether any NIC uses the softnet engine.
func topologyNeedsSoftnet(nics []vmconfig.NICConfig) bool {
	for _, nic := range nics {
		if nic.Mode == vmconfig.NICModeSoftnet {
			return true
		}
	}
	return false
}

// firstOrEmpty returns the first element of s, or "".
func firstOrEmpty(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}

// additionalDiskAttachments ports Run.additionalDiskAttachments().
func (c *RunCommand) additionalDiskAttachments() ([]*virtualization.VZStorageDeviceConfiguration, error) {
	var configurations []*virtualization.VZStorageDeviceConfiguration
	for _, disk := range c.Disk {
		configuration, err := craftAdditionalDisk(disk)
		if err != nil {
			return nil, err
		}
		configurations = append(configurations, configuration)
	}
	return configurations, nil
}

// usbMassStorageDevices ports lume's --usb-storage: each image is attached
// read-write as a USB mass storage device (macOS 13+).
func (c *RunCommand) usbMassStorageDevices() ([]*virtualization.VZStorageDeviceConfiguration, error) {
	if len(c.USBStorage) == 0 {
		return nil, nil
	}
	if !weaveplatform.MacOSAtLeast(13) {
		return nil, weavevm.NewUnsupportedOSError("USB mass storage devices", "are")
	}

	var configurations []*virtualization.VZStorageDeviceConfiguration
	for _, imagePath := range c.USBStorage {
		attachment, err := virtualization.VZDiskImageStorageDeviceAttachmentFromID(
			objcutil.AllocClass("VZDiskImageStorageDeviceAttachment")).
			InitWithURLReadOnlyError(objcutil.NSURLFromPath(objcutil.ExpandTilde(imagePath)), false)
		if err != nil {
			return nil, err
		}
		device := virtualization.VZUSBMassStorageDeviceConfigurationFromID(
			objcutil.AllocClass("VZUSBMassStorageDeviceConfiguration")).
			InitWithAttachment(&attachment.VZStorageDeviceAttachment)
		configurations = append(configurations, &device.VZStorageDeviceConfiguration)
	}
	return configurations, nil
}

// runUI ports Run.runUI/MainApp: an AppKit window hosting the
// VZVirtualMachineView.
func (c *RunCommand) runUI() {
	app := appkit.NSApplicationSharedApplication()
	app.SetActivationPolicy(appkit.NSApplicationActivationPolicyRegular)

	contentRect := corefoundation.CGRect{
		Origin: corefoundation.CGPoint{X: 0, Y: 0},
		Size: corefoundation.CGSize{
			Width:  float64(vm.Config.Display.Width),
			Height: float64(vm.Config.Display.Height),
		},
	}
	styleMask := appkit.NSWindowStyleMaskTitled | appkit.NSWindowStyleMaskClosable |
		appkit.NSWindowStyleMaskMiniaturizable | appkit.NSWindowStyleMaskResizable

	window := appkit.NSWindowFromID(objcutil.AllocClass("NSWindow")).
		InitWithContentRectStyleMaskBackingDefer(contentRect, styleMask, appkit.NSBackingStoreBuffered, false)
	window.SetTitle(objcutil.NSStr(vm.Name))

	machineView := virtualization.VZVirtualMachineViewFromID(
		objc.Send[objc.ID](objcutil.AllocClass("VZVirtualMachineView"), objc.RegisterName("init")))
	machineView.SetCapturesSystemKeys(c.CaptureSystemKeys)

	// If not specified, enable automatic display reconfiguration for guests
	// that support it. Disabled for Linux because of poor HiDPI support.
	displayRefit := vm.Config.OS != weaveplatform.OSLinux
	if vm.Config.DisplayRefit != nil {
		displayRefit = *vm.Config.DisplayRefit
	}
	if weaveplatform.MacOSAtLeast(14) && displayRefit {
		machineView.SetAutomaticallyReconfiguresDisplay(true)
	}
	machineView.SetVirtualMachine(vm.VirtualMachine)

	window.SetContentView(&machineView.NSView)
	window.SetDelegate(objc.ID(runWindowDelegateClass()).Send(objc.RegisterName("new")))
	window.Center()
	window.MakeKeyAndOrderFront(0)

	app.ActivateIgnoringOtherApps(true)
	app.Run()
}

// runWindowDelegateClass registers the window delegate that translates a
// window close into SIGUSR1 (suspendable) or SIGINT, mirroring MainApp's
// onDisappear handler.
var runWindowDelegateClass = sync.OnceValue(func() objc.Class {
	class, err := objc.RegisterClass("OrinRunWindowDelegate", objc.GetClass("NSObject"),
		[]*objc.Protocol{objc.GetProtocol("NSWindowDelegate")},
		nil,
		[]objc.MethodDef{
			{
				Cmd: objc.RegisterName("windowWillClose:"),
				Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
					signum := syscall.SIGINT
					if runSuspendableFlag.Load() {
						signum = syscall.SIGUSR1
					}
					_ = syscall.Kill(syscall.Getpid(), signum)
				},
			},
		})
	if err != nil {
		panic(fmt.Sprintf("failed to register OrinRunWindowDelegate: %v", err))
	}
	return class
})

// runSuspendableFlag mirrors MainApp.suspendable for the window delegate.
var runSuspendableFlag atomic.Bool

// diskOptions ports Run.swift's DiskOptions struct.
type diskOptions struct {
	readOnly              bool
	syncModeRaw           string
	cachingModeRaw        string
	foundAtLeastOneOption bool
}

func parseDiskOptions(parseFrom string) diskOptions {
	var options diskOptions

	for option := range strings.SplitSeq(parseFrom, ",") {
		switch {
		case option == "ro":
			options.readOnly = true
			options.foundAtLeastOneOption = true
		case strings.HasPrefix(option, "sync="):
			options.syncModeRaw = strings.TrimPrefix(option, "sync=")
			options.foundAtLeastOneOption = true
		case strings.HasPrefix(option, "caching="):
			options.cachingModeRaw = strings.TrimPrefix(option, "caching=")
			options.foundAtLeastOneOption = true
		}
	}

	return options
}

// craftAdditionalDisk ports Run.swift's AdditionalDisk: a disk image path,
// block device, remote VM name or NBD URL with optional :options suffix.
func craftAdditionalDisk(parseFrom string) (*virtualization.VZStorageDeviceConfiguration, error) {
	diskPath, options := parseAdditionalDiskOptions(parseFrom)

	syncMode, err := parseDiskSynchronizationMode(options.syncModeRaw)
	if err != nil {
		return nil, err
	}

	// Network Block Devices.
	if scheme, _, ok := strings.Cut(diskPath, "://"); ok &&
		(scheme == "nbd" || scheme == "nbds" || scheme == "nbd+unix" || scheme == "nbds+unix") {
		if !weaveplatform.MacOSAtLeast(14) {
			return nil, weavevm.NewUnsupportedOSError("attaching Network Block Devices", "are")
		}

		nbdAttachment, err := virtualization.VZNetworkBlockDeviceStorageDeviceAttachmentFromID(
			objcutil.AllocClass("VZNetworkBlockDeviceStorageDeviceAttachment")).
			InitWithURLTimeoutForcedReadOnlySynchronizationModeError(
				foundation.NSURLURLWithString(objcutil.NSStr(diskPath)), 30, options.readOnly, syncMode)
		if err != nil {
			return nil, err
		}
		device := virtualization.VZVirtioBlockDeviceConfigurationFromID(objcutil.AllocClass("VZVirtioBlockDeviceConfiguration")).
			InitWithAttachment(&nbdAttachment.VZStorageDeviceAttachment)
		return &device.VZStorageDeviceConfiguration, nil
	}

	// Expand the tilde (~) since at this point we're dealing with a local
	// path; doing it earlier corrupts remote URLs like nbd://.
	expandedDiskPath := objcutil.ExpandTilde(diskPath)

	// Block devices.
	if pathHasMode(expandedDiskPath, syscall.S_IFBLK) {
		if !weaveplatform.MacOSAtLeast(14) {
			return nil, weavevm.NewUnsupportedOSError("attaching block devices", "are")
		}

		openMode := os.O_RDWR
		if options.readOnly {
			openMode = os.O_RDONLY
		}
		fd, err := syscall.Open(expandedDiskPath, openMode, 0)
		if err != nil {
			switch {
			case errors.Is(err, syscall.EBUSY):
				return nil, weaveerrors.ErrFailedToOpenBlockDevice(expandedDiskPath, "already in use, try umounting it via \"diskutil unmountDisk\" (when the whole disk) or \"diskutil umount\" (when mounting a single partition)")
			case errors.Is(err, syscall.EACCES):
				return nil, weaveerrors.ErrFailedToOpenBlockDevice(expandedDiskPath, fmt.Sprintf("permission denied, consider changing the disk's owner using \"sudo chown $USER %s\" or run Weave as a superuser (see --disk help for more details on how to do that correctly)", expandedDiskPath))
			default:
				return nil, weaveerrors.ErrFailedToOpenBlockDevice(expandedDiskPath, err.Error())
			}
		}

		fileHandle := foundation.NSFileHandleFromID(objcutil.AllocClass("NSFileHandle")).
			InitWithFileDescriptorCloseOnDealloc(fd, true)
		blockAttachment, err := virtualization.VZDiskBlockDeviceStorageDeviceAttachmentFromID(
			objcutil.AllocClass("VZDiskBlockDeviceStorageDeviceAttachment")).
			InitWithFileHandleReadOnlySynchronizationModeError(fileHandle, options.readOnly, syncMode)
		if err != nil {
			return nil, err
		}
		device := virtualization.VZVirtioBlockDeviceConfigurationFromID(objcutil.AllocClass("VZVirtioBlockDeviceConfiguration")).
			InitWithAttachment(&blockAttachment.VZStorageDeviceAttachment)
		return &device.VZStorageDeviceConfiguration, nil
	}

	// Support remote VM names in the --disk command-line argument.
	if remoteName, err := oci.NewRemoteName(diskPath); err == nil {
		ociStorage, err := vmstorage.NewVMStorageOCI()
		if err != nil {
			return nil, err
		}
		vmDir, err := ociStorage.Open(remoteName, time.Now())
		if err != nil {
			return nil, err
		}

		// VZDiskImageStorageDeviceAttachment does not support FileHandle,
		// so clone the disk into an intermediate tmp location.
		config, err := weaveconfig.NewConfig()
		if err != nil {
			return nil, err
		}
		clonedDiskURL := config.WeaveTmpDir.URLByAppendingPathComponent(
			objcutil.NSStr("run-disk-" + objcutil.GoStr(foundation.NSUUIDUUID().UUIDString())))

		if _, err := foundation.NSFileManagerDefaultManager().
			CopyItemAtURLToURLError(vmDir.DiskURL(), clonedDiskURL); err != nil {
			return nil, err
		}

		cloneLock, err := weavelock.NewFileLock(clonedDiskURL)
		if err != nil {
			return nil, err
		}
		if err := cloneLock.Lock(); err != nil {
			return nil, err
		}

		diskImageAttachment, err := virtualization.VZDiskImageStorageDeviceAttachmentFromID(
			objcutil.AllocClass("VZDiskImageStorageDeviceAttachment")).
			InitWithURLReadOnlyError(clonedDiskURL, options.readOnly)
		if err != nil {
			return nil, err
		}
		device := virtualization.VZVirtioBlockDeviceConfigurationFromID(objcutil.AllocClass("VZVirtioBlockDeviceConfiguration")).
			InitWithAttachment(&diskImageAttachment.VZStorageDeviceAttachment)
		return &device.VZStorageDeviceConfiguration, nil
	}

	diskFileURL := objcutil.NSURLFromPath(expandedDiskPath)

	// Error out if the disk is locked by the host (e.g. it was mounted in
	// Finder), see cirruslabs/tart#323.
	if !options.readOnly {
		diskLock, err := weavelock.NewFileLock(diskFileURL)
		if err == nil {
			acquired, lockErr := diskLock.Trylock()
			if lockErr == nil && !acquired {
				_ = diskLock.Close()
				return nil, weaveerrors.ErrDiskAlreadyInUse("disk %s seems to be already in use, unmount it first in Finder", expandedDiskPath)
			}
			_ = diskLock.Close()
		}
	}

	cachingMode := virtualization.VZDiskImageCachingModeAutomatic
	if mode, ok, err := parseDiskImageCachingMode(options.cachingModeRaw); err != nil {
		return nil, err
	} else if ok {
		cachingMode = mode
	}
	imageSyncMode, err := parseDiskImageSynchronizationMode(options.syncModeRaw)
	if err != nil {
		return nil, err
	}

	diskImageAttachment, err := virtualization.VZDiskImageStorageDeviceAttachmentFromID(
		objcutil.AllocClass("VZDiskImageStorageDeviceAttachment")).
		InitWithURLReadOnlyCachingModeSynchronizationModeError(diskFileURL, options.readOnly, cachingMode, imageSyncMode)
	if err != nil {
		return nil, err
	}
	device := virtualization.VZVirtioBlockDeviceConfigurationFromID(objcutil.AllocClass("VZVirtioBlockDeviceConfiguration")).
		InitWithAttachment(&diskImageAttachment.VZStorageDeviceAttachment)
	return &device.VZStorageDeviceConfiguration, nil
}

// parseAdditionalDiskOptions ports AdditionalDisk.parseOptions(_:).
func parseAdditionalDiskOptions(parseFrom string) (string, diskOptions) {
	arguments := strings.Split(parseFrom, ":")

	options := parseDiskOptions(arguments[len(arguments)-1])
	if options.foundAtLeastOneOption {
		arguments = arguments[:len(arguments)-1]
	}

	return strings.Join(arguments, ":"), options
}

// directoryShare ports Run.swift's DirectoryShare struct.
type directoryShare struct {
	name     string
	path     string // local path or http(s) URL
	readOnly bool
	mountTag string
}

func parseDirectoryShare(parseFrom string) directoryShare {
	share := directoryShare{
		mountTag: objcutil.GoStr(virtualization.VZVirtioFileSystemDeviceConfigurationMacOSGuestAutomountTag()),
	}

	// Consume options.
	arguments := strings.Split(parseFrom, ":")
	found := false
	for option := range strings.SplitSeq(arguments[len(arguments)-1], ",") {
		switch {
		case option == "ro":
			share.readOnly = true
			found = true
		case strings.HasPrefix(option, "tag="):
			share.mountTag = strings.TrimPrefix(option, "tag=")
			found = true
		}
	}
	if found {
		arguments = arguments[:len(arguments)-1]
	}
	rest := strings.Join(arguments, ":")

	// Special case for URLs.
	if strings.HasPrefix(rest, "http:") || strings.HasPrefix(rest, "https:") {
		share.path = rest
		return share
	}

	if name, path, ok := strings.Cut(rest, ":"); ok {
		share.name = name
		share.path = objcutil.ExpandTilde(path)
	} else {
		share.path = objcutil.ExpandTilde(rest)
	}
	return share
}

// parseSharedDirectoryShare parses lume's --shared-dir syntax
// (path[:ro|rw], default read-write) into the same directoryShare struct as
// --dir. The macOS automount tag is always used.
func parseSharedDirectoryShare(parseFrom string) (directoryShare, error) {
	share := directoryShare{
		mountTag: objcutil.GoStr(virtualization.VZVirtioFileSystemDeviceConfigurationMacOSGuestAutomountTag()),
	}

	path := parseFrom
	if prefix, suffix, ok := strings.Cut(parseFrom, ":"); ok {
		switch suffix {
		case "ro":
			share.readOnly = true
		case "rw":
			share.readOnly = false
		default:
			return share, weaveerrors.ErrGeneric("invalid --shared-dir format: expected <path>[:ro|rw], got %q", parseFrom)
		}
		path = prefix
	}
	if path == "" {
		return share, weaveerrors.ErrGeneric("invalid --shared-dir format: expected <path>[:ro|rw], got %q", parseFrom)
	}
	share.path = objcutil.ExpandTilde(path)
	return share, nil
}

// directoryShares ports Run.directoryShares(), extended with lume's
// --shared-dir entries which funnel into the same sharing devices.
func (c *RunCommand) directoryShares() ([]*virtualization.VZDirectorySharingDeviceConfiguration, error) {
	if len(c.Dir) == 0 && len(c.SharedDir) == 0 {
		return nil, nil
	}

	if !weaveplatform.MacOSAtLeast(13) {
		return nil, weavevm.NewUnsupportedOSError("directory sharing", "is")
	}

	allShares := make([]directoryShare, 0, len(c.Dir)+len(c.SharedDir))
	for _, rawDir := range c.Dir {
		allShares = append(allShares, parseDirectoryShare(rawDir))
	}
	for _, rawDir := range c.SharedDir {
		share, err := parseSharedDirectoryShare(rawDir)
		if err != nil {
			return nil, err
		}
		allShares = append(allShares, share)
	}

	sharesByTag := map[string][]directoryShare{}
	var tagOrder []string
	for _, share := range allShares {
		if _, ok := sharesByTag[share.mountTag]; !ok {
			tagOrder = append(tagOrder, share.mountTag)
		}
		sharesByTag[share.mountTag] = append(sharesByTag[share.mountTag], share)
	}

	var devices []*virtualization.VZDirectorySharingDeviceConfiguration
	for _, mountTag := range tagOrder {
		shares := sharesByTag[mountTag]

		sharingDevice := virtualization.VZVirtioFileSystemDeviceConfigurationFromID(
			objcutil.AllocClass("VZVirtioFileSystemDeviceConfiguration")).InitWithTag(objcutil.NSStr(mountTag))

		allNamedShares := true
		for _, share := range shares {
			if share.name == "" {
				allNamedShares = false
			}
		}

		if len(shares) == 1 && shares[0].name == "" {
			sharedDirectory, err := shares[0].createConfiguration()
			if err != nil {
				return nil, err
			}
			singleShare := virtualization.VZSingleDirectoryShareFromID(
				objcutil.AllocClass("VZSingleDirectoryShare")).InitWithDirectory(sharedDirectory)
			sharingDevice.SetShare(&singleShare.VZDirectoryShare)
		} else if !allNamedShares {
			return nil, weaveerrors.ErrGeneric("invalid --dir syntax: for multiple directory shares each one of them should be named")
		} else {
			directories := objc.Send[objc.ID](objc.ID(objc.GetClass("NSMutableDictionary")), objcutil.SelDictionary)
			for _, share := range shares {
				sharedDirectory, err := share.createConfiguration()
				if err != nil {
					return nil, err
				}
				directories.Send(objcutil.SelSetObjectForKey, sharedDirectory.Ptr(), pureobjc.NSString(share.name))
			}
			multipleShare := virtualization.VZMultipleDirectoryShareFromID(
				objcutil.AllocClass("VZMultipleDirectoryShare")).
				InitWithDirectories(foundation.NSDictionaryFromID[*foundation.NSString, *virtualization.VZSharedDirectory](pureobjc.Retain(directories)))
			sharingDevice.SetShare(&multipleShare.VZDirectoryShare)
		}

		devices = append(devices, &sharingDevice.VZDirectorySharingDeviceConfiguration)
	}

	return devices, nil
}

// createConfiguration ports DirectoryShare.createConfiguration(): local
// paths are shared directly; remote archives are downloaded (with an
// on-disk cache, mirroring Swift's URLCache usage) and unpacked into a
// temporary directory with tar.
func (s directoryShare) createConfiguration() (*virtualization.VZSharedDirectory, error) {
	if !strings.HasPrefix(s.path, "http:") && !strings.HasPrefix(s.path, "https:") {
		return virtualization.VZSharedDirectoryFromID(objcutil.AllocClass("VZSharedDirectory")).
			InitWithURLReadOnly(objcutil.NSURLFromPath(s.path), s.readOnly), nil
	}

	config, err := weaveconfig.NewConfig()
	if err != nil {
		return nil, err
	}

	// Cache the downloaded archive by URL digest.
	cachePath := objcutil.GoStr(config.WeaveCacheDir.URLByAppendingPathComponent(
		objcutil.NSStr("dir-archive-" + strings.TrimPrefix(oci.DigestHash([]byte(s.path)), "sha256:") + ".tgz")).Path())

	if _, err := os.Stat(cachePath); err != nil {
		fmt.Printf("Downloading %s...\n", s.path)
		chunks, response, err := fetcher.FetcherFetch(context.Background(),
			foundation.NSURLRequestRequestWithURL(foundation.NSURLURLWithString(objcutil.NSStr(s.path))), true)
		if err != nil {
			return nil, err
		}

		// Known-size transfer: disk-space guard up front, then percentage
		// progress (the spinner is only for indeterminate waits — see
		// terminal/spinner.go).
		var progress *logging.DownloadProgress
		if expectedLength := response.ExpectedContentLength(); expectedLength > 0 {
			if err := vmstorage.EnsureDiskSpace(uint64(expectedLength), nil); err != nil {
				return nil, err
			}
			progress = logging.NewDownloadProgress(expectedLength)
			logging.NewProgressObserver(progress).Log(logging.DefaultLogger())
		}

		archive, err := os.Create(cachePath)
		if err != nil {
			return nil, err
		}
		empty := true
		for chunk := range chunks {
			if chunk.Err != nil {
				archive.Close()
				_ = os.Remove(cachePath)
				return nil, chunk.Err
			}
			if len(chunk.Data) > 0 {
				empty = false
			}
			if _, err := archive.Write(chunk.Data); err != nil {
				archive.Close()
				_ = os.Remove(cachePath)
				return nil, err
			}
			if progress != nil {
				progress.Add(int64(len(chunk.Data)))
			}
		}
		if err := archive.Close(); err != nil {
			return nil, err
		}
		if empty {
			_ = os.Remove(cachePath)
			return nil, weaveerrors.ErrGeneric("Remote archive is empty!")
		}
		fmt.Println("Cached for future invocations!")
	} else {
		fmt.Printf("Using cached archive for %s...\n", s.path)
	}

	temporaryLocation := config.WeaveTmpDir.URLByAppendingPathComponent(
		objcutil.NSStr(objcutil.GoStr(foundation.NSUUIDUUID().UUIDString()) + ".volume"))
	temporaryPath := objcutil.GoStr(temporaryLocation.Path())
	if err := os.MkdirAll(temporaryPath, 0o755); err != nil {
		return nil, err
	}
	tmpLock, err := weavelock.NewFileLock(temporaryLocation)
	if err != nil {
		return nil, err
	}
	if err := tmpLock.Lock(); err != nil {
		return nil, err
	}

	tarURL := objcutil.ResolveBinaryPath("tar")
	if tarURL == nil {
		return nil, weaveerrors.ErrGeneric("tar not found in PATH")
	}

	task := foundation.NSTaskFromID(objc.Send[objc.ID](objc.ID(objc.GetClass("NSTask")), objc.RegisterName("new")))
	task.SetExecutableURL(tarURL)
	task.SetCurrentDirectoryURL(temporaryLocation)
	task.SetArguments(objcutil.NSStringArray([]string{"-xzf", cachePath}))

	if _, err := task.LaunchAndReturnError(); err != nil {
		return nil, err
	}
	task.WaitUntilExit()

	if !(task.TerminationReason() == foundation.NSTaskTerminationReasonExit && task.TerminationStatus() == 0) {
		return nil, weaveerrors.ErrGeneric("Unarchiving failed!")
	}

	fmt.Println("Unarchived into a temporary directory!")

	return virtualization.VZSharedDirectoryFromID(objcutil.AllocClass("VZSharedDirectory")).
		InitWithURLReadOnly(temporaryLocation, s.readOnly), nil
}

// rosettaDirectoryShare ports Run.rosettaDirectoryShare().
func (c *RunCommand) rosettaDirectoryShare() ([]*virtualization.VZDirectorySharingDeviceConfiguration, error) {
	if c.RosettaTag == "" {
		return nil, nil
	}
	if runtime.GOARCH != "arm64" {
		// There is no Rosetta on Intel.
		return nil, nil
	}
	if !weaveplatform.MacOSAtLeast(13) {
		return nil, weavevm.NewUnsupportedOSError("Rosetta directory share", "is")
	}

	switch virtualization.VZLinuxRosettaDirectoryShareAvailability() {
	case virtualization.VZLinuxRosettaAvailabilityNotInstalled:
		return nil, &weavevm.UnsupportedOSError{What: "Rosetta directory share", Plural: "is", Requires: "that have Rosetta installed"}
	case virtualization.VZLinuxRosettaAvailabilityNotSupported:
		return nil, &weavevm.UnsupportedOSError{What: "Rosetta directory share", Plural: "is", Requires: "running Apple silicon"}
	}

	if _, err := virtualization.VZVirtioFileSystemDeviceConfigurationValidateTagError(objcutil.NSStr(c.RosettaTag)); err != nil {
		return nil, err
	}
	device := virtualization.VZVirtioFileSystemDeviceConfigurationFromID(
		objcutil.AllocClass("VZVirtioFileSystemDeviceConfiguration")).InitWithTag(objcutil.NSStr(c.RosettaTag))
	rosettaShare, err := virtualization.VZLinuxRosettaDirectoryShareFromID(
		objcutil.AllocClass("VZLinuxRosettaDirectoryShare")).InitWithError()
	if err != nil {
		return nil, err
	}
	device.SetShare(&rosettaShare.VZDirectoryShare)

	return []*virtualization.VZDirectorySharingDeviceConfiguration{&device.VZDirectorySharingDeviceConfiguration}, nil
}

// pathHasMode ports Run.swift's pathHasMode(_:mode:).
func pathHasMode(path string, mode uint16) bool {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return false
	}
	return st.Mode&syscall.S_IFMT == mode
}
