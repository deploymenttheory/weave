// Port of tart's VMDirectory.swift: the on-disk layout of a single VM
// (config.json, disk.img, nvram.bin, state.vzvmsave, control.sock).
// CryptoKit's Insecure.MD5 becomes crypto/md5.
//go:build darwin

package vmdirectory

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"time"

	weaveconfig "github.com/deploymenttheory/weave/internal/config"
	"github.com/deploymenttheory/weave/internal/diskimage"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	weavelock "github.com/deploymenttheory/weave/internal/lock"
	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/prune"
	"github.com/deploymenttheory/weave/internal/vmconfig"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	virtualization "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/virtualization"
)

// VMDirectoryState mirrors VMDirectory.State.
type VMDirectoryState string

const (
	VMDirectoryStateRunning   VMDirectoryState = "running"
	VMDirectoryStateSuspended VMDirectoryState = "suspended"
	VMDirectoryStateStopped   VMDirectoryState = "stopped"
)

// VMDirectory mirrors tart's VMDirectory struct.
type VMDirectory struct {
	BaseURL *foundation.NSURL
}

var _ prune.Prunable = (*VMDirectory)(nil)

func NewVMDirectory(baseURL *foundation.NSURL) *VMDirectory {
	return &VMDirectory{BaseURL: baseURL}
}

func (d *VMDirectory) ConfigURL() *foundation.NSURL {
	return d.BaseURL.URLByAppendingPathComponent(objcutil.NSStr("config.json"))
}

func (d *VMDirectory) DiskURL() *foundation.NSURL {
	return d.BaseURL.URLByAppendingPathComponent(objcutil.NSStr("disk.img"))
}

func (d *VMDirectory) NvramURL() *foundation.NSURL {
	return d.BaseURL.URLByAppendingPathComponent(objcutil.NSStr("nvram.bin"))
}

func (d *VMDirectory) StateURL() *foundation.NSURL {
	return d.BaseURL.URLByAppendingPathComponent(objcutil.NSStr("state.vzvmsave"))
}

func (d *VMDirectory) ManifestURL() *foundation.NSURL {
	return d.BaseURL.URLByAppendingPathComponent(objcutil.NSStr("manifest.json"))
}

// ControlSocketURL is created relative to the base URL so ControlSocket.Run
// can chdir and bind the short relative path (104-byte sun_path limit).
func (d *VMDirectory) ControlSocketURL() *foundation.NSURL {
	return foundation.NSURLFileURLWithPathRelativeToURL(objcutil.NSStr("control.sock"), d.BaseURL)
}

func (d *VMDirectory) ExplicitlyPulledMark() *foundation.NSURL {
	return d.BaseURL.URLByAppendingPathComponent(objcutil.NSStr(".explicitly-pulled"))
}

// VNCEndpointPath is where a running VM with an experimental VNC server
// records its vnc:// URL, so other processes (e.g. the MCP screen tools) can
// connect to drive or view the VM by name. It is removed when the VM stops.
func (d *VMDirectory) VNCEndpointPath() string {
	return objcutil.GoStr(d.BaseURL.URLByAppendingPathComponent(objcutil.NSStr(".vnc-endpoint")).Path())
}

func (d *VMDirectory) Name() string {
	return objcutil.GoStr(d.BaseURL.LastPathComponent())
}

func (d *VMDirectory) URL() *foundation.NSURL {
	return d.BaseURL
}

// Lock ports VMDirectory.lock().
func (d *VMDirectory) Lock() (*weavelock.PIDLock, error) {
	return weavelock.NewPIDLock(d.ConfigURL())
}

// Running ports VMDirectory.running(). A failure to instantiate the PIDLock
// is reported as "not running": the most common reason is a race with
// "tart delete" (ENOENT), and the cost of a false positive is far less than
// crashing "tart list" on a busy machine.
func (d *VMDirectory) Running() (bool, error) {
	lock, err := d.Lock()
	if err != nil {
		return false, nil
	}
	defer lock.Close()

	pid, err := lock.PID()
	if err != nil {
		return false, err
	}
	return pid != 0, nil
}

// State ports VMDirectory.state().
func (d *VMDirectory) State() (VMDirectoryState, error) {
	running, err := d.Running()
	if err != nil {
		return "", err
	}
	if running {
		return VMDirectoryStateRunning, nil
	}
	if foundation.NSFileManagerDefaultManager().FileExistsAtPath(d.StateURL().Path()) {
		return VMDirectoryStateSuspended, nil
	}
	return VMDirectoryStateStopped, nil
}

// VMDirectoryTemporary ports VMDirectory.temporary().
func VMDirectoryTemporary() (*VMDirectory, error) {
	config, err := weaveconfig.NewConfig()
	if err != nil {
		return nil, err
	}
	tmpDir := config.WeaveTmpDir.URLByAppendingPathComponent(foundation.NSUUIDUUID().UUIDString())
	if _, err := foundation.NSFileManagerDefaultManager().
		CreateDirectoryAtURLWithIntermediateDirectoriesAttributesError(tmpDir, false, nil); err != nil {
		return nil, err
	}
	return NewVMDirectory(tmpDir), nil
}

// VMDirectoryTemporaryDeterministic ports VMDirectory.temporaryDeterministic
// (key:): a tmp directory whose name is the MD5 hash of key.
func VMDirectoryTemporaryDeterministic(key string) (*VMDirectory, error) {
	config, err := weaveconfig.NewConfig()
	if err != nil {
		return nil, err
	}
	hash := md5.Sum([]byte(key))
	tmpDir := config.WeaveTmpDir.URLByAppendingPathComponent(objcutil.NSStr(hex.EncodeToString(hash[:])))
	if _, err := foundation.NSFileManagerDefaultManager().
		CreateDirectoryAtURLWithIntermediateDirectoriesAttributesError(tmpDir, true, nil); err != nil {
		return nil, err
	}
	return NewVMDirectory(tmpDir), nil
}

// Initialized ports VMDirectory.initialized.
func (d *VMDirectory) Initialized() bool {
	fileManager := foundation.NSFileManagerDefaultManager()
	return fileManager.FileExistsAtPath(d.ConfigURL().Path()) &&
		fileManager.FileExistsAtPath(d.DiskURL().Path()) &&
		fileManager.FileExistsAtPath(d.NvramURL().Path())
}

// Initialize ports VMDirectory.initialize(overwrite:).
func (d *VMDirectory) Initialize(overwrite bool) error {
	if !overwrite && d.Initialized() {
		return weaveerrors.ErrVMDirectoryAlreadyInitialized("VM directory is already initialized, preventing overwrite")
	}

	fileManager := foundation.NSFileManagerDefaultManager()
	if _, err := fileManager.CreateDirectoryAtURLWithIntermediateDirectoriesAttributesError(d.BaseURL, true, nil); err != nil {
		return err
	}

	_, _ = fileManager.RemoveItemAtURLError(d.ConfigURL())
	_, _ = fileManager.RemoveItemAtURLError(d.DiskURL())
	_, _ = fileManager.RemoveItemAtURLError(d.NvramURL())

	return nil
}

// Validate ports VMDirectory.validate(userFriendlyName:).
func (d *VMDirectory) Validate(userFriendlyName string) error {
	if !foundation.NSFileManagerDefaultManager().FileExistsAtPath(d.BaseURL.Path()) {
		return weaveerrors.ErrVMDoesNotExist(userFriendlyName)
	}

	if !d.Initialized() {
		return weaveerrors.ErrVMMissingFiles("VM is missing some of its files (%s, %s or %s)",
			objcutil.GoStr(d.ConfigURL().LastPathComponent()),
			objcutil.GoStr(d.DiskURL().LastPathComponent()),
			objcutil.GoStr(d.NvramURL().LastPathComponent()))
	}

	return nil
}

// Clone ports VMDirectory.clone(to:generateMAC:).
func (d *VMDirectory) Clone(to *VMDirectory, generateMAC bool) error {
	fileManager := foundation.NSFileManagerDefaultManager()

	if _, err := fileManager.CopyItemAtURLToURLError(d.ConfigURL(), to.ConfigURL()); err != nil {
		return err
	}
	if _, err := fileManager.CopyItemAtURLToURLError(d.NvramURL(), to.NvramURL()); err != nil {
		return err
	}
	if _, err := fileManager.CopyItemAtURLToURLError(d.DiskURL(), to.DiskURL()); err != nil {
		return err
	}
	_, _ = fileManager.CopyItemAtURLToURLError(d.StateURL(), to.StateURL())

	// Re-generate MAC address.
	if generateMAC {
		return to.RegenerateMACAddress()
	}
	return nil
}

// MACAddress ports VMDirectory.macAddress().
func (d *VMDirectory) MACAddress() (string, error) {
	config, err := vmconfig.NewVMConfigFromURL(d.ConfigURL())
	if err != nil {
		return "", err
	}
	return objcutil.GoStr(config.MACAddress.String()), nil
}

// RegenerateMACAddress ports VMDirectory.regenerateMACAddress().
func (d *VMDirectory) RegenerateMACAddress() error {
	config, err := vmconfig.NewVMConfigFromURL(d.ConfigURL())
	if err != nil {
		return err
	}

	config.MACAddress = virtualization.VZMACAddressRandomLocallyAdministeredAddress()
	// Cleanup state if any.
	_, _ = foundation.NSFileManagerDefaultManager().RemoveItemAtURLError(d.StateURL())

	return config.Save(d.ConfigURL())
}

// ResizeDisk ports VMDirectory.resizeDisk(_:format:).
func (d *VMDirectory) ResizeDisk(sizeGB uint16, format diskimage.DiskImageFormat) error {
	if foundation.NSFileManagerDefaultManager().FileExistsAtPath(d.DiskURL().Path()) {
		return d.resizeExistingDisk(sizeGB)
	}
	return d.createDisk(sizeGB, format)
}

func (d *VMDirectory) resizeExistingDisk(sizeGB uint16) error {
	config, err := vmconfig.NewVMConfigFromURL(d.ConfigURL())
	if err != nil {
		return err
	}

	if config.DiskFormat == diskimage.DiskImageFormatASIF {
		return d.resizeASIFDisk(sizeGB)
	}
	return d.resizeRawDisk(sizeGB)
}

func (d *VMDirectory) resizeRawDisk(sizeGB uint16) error {
	diskFileHandle, err := foundation.NSFileHandleFileHandleForWritingToURLError(d.DiskURL())
	if err != nil {
		return err
	}

	var currentDiskFileLength uint64
	if _, err := diskFileHandle.SeekToEndReturningOffsetError(&currentDiskFileLength); err != nil {
		return err
	}
	desiredDiskFileLength := uint64(sizeGB) * 1000 * 1000 * 1000

	if desiredDiskFileLength < currentDiskFileLength {
		return weaveerrors.ErrInvalidDiskSize("new disk size of %s should be larger than the current disk size of %s",
			ByteCountString(int64(desiredDiskFileLength)), ByteCountString(int64(currentDiskFileLength)))
	} else if desiredDiskFileLength > currentDiskFileLength {
		if _, err := diskFileHandle.TruncateAtOffsetError(desiredDiskFileLength); err != nil {
			return err
		}
	}
	_, err = diskFileHandle.CloseAndReturnError()
	return err
}

func (d *VMDirectory) resizeASIFDisk(sizeGB uint16) error {
	diskImageInfo, err := diskimage.DiskutilImageInfo(d.DiskURL())
	if err != nil {
		return weaveerrors.ErrFailedToResizeDisk("%v", err)
	}

	currentSizeBytes, err := diskImageInfo.TotalBytes()
	if err != nil {
		return weaveerrors.ErrFailedToResizeDisk("%v", err)
	}
	desiredSizeBytes := uint64(sizeGB) * 1000 * 1000 * 1000

	if desiredSizeBytes < uint64(currentSizeBytes) {
		return weaveerrors.ErrInvalidDiskSize("New disk size of %s should be larger than the current disk size of %s",
			ByteCountString(int64(desiredSizeBytes)), ByteCountString(int64(currentSizeBytes)))
	} else if desiredSizeBytes > uint64(currentSizeBytes) {
		// Resize the ASIF disk image using diskutil.
		return d.performASIFResize(sizeGB)
	}
	// If sizes are equal, no action needed.
	return nil
}

func (d *VMDirectory) performASIFResize(sizeGB uint16) error {
	if objcutil.ResolveBinaryPath("diskutil") == nil {
		return weaveerrors.ErrFailedToResizeDisk("diskutil not found in PATH")
	}

	stdout, stderr, err := diskimage.DiskutilRun([]string{
		"image", "resize",
		"--size", fmt.Sprintf("%dG", sizeGB),
		objcutil.GoStr(d.DiskURL().Path()),
	})
	if err != nil {
		return weaveerrors.ErrFailedToResizeDisk("Failed to resize ASIF disk image: %v", err)
	}
	_ = stdout
	_ = stderr
	return nil
}

func (d *VMDirectory) createDisk(sizeGB uint16, format diskimage.DiskImageFormat) error {
	if format == diskimage.DiskImageFormatASIF {
		return diskimage.DiskutilImageCreate(d.DiskURL(), sizeGB)
	}
	return d.createRawDisk(sizeGB)
}

func (d *VMDirectory) createRawDisk(sizeGB uint16) error {
	// Create a traditional raw disk image. The contents argument cannot be a
	// nil *NSData (the generated binding dereferences it), so pass an empty
	// NSData for Swift's contents: nil.
	foundation.NSFileManagerDefaultManager().CreateFileAtPathContentsAttributes(d.DiskURL().Path(), objcutil.BytesToNSData(nil), nil)

	diskFileHandle, err := foundation.NSFileHandleFileHandleForWritingToURLError(d.DiskURL())
	if err != nil {
		return err
	}
	desiredDiskFileLength := uint64(sizeGB) * 1000 * 1000 * 1000
	if _, err := diskFileHandle.TruncateAtOffsetError(desiredDiskFileLength); err != nil {
		return err
	}
	_, err = diskFileHandle.CloseAndReturnError()
	return err
}

// Delete ports VMDirectory.delete().
func (d *VMDirectory) Delete() error {
	lock, err := d.Lock()
	if err != nil {
		return err
	}
	defer lock.Close()

	acquired, err := lock.Trylock()
	if err != nil {
		return err
	}
	if !acquired {
		return weaveerrors.ErrVMIsRunning(d.Name())
	}

	if _, err := foundation.NSFileManagerDefaultManager().RemoveItemAtURLError(d.BaseURL); err != nil {
		return err
	}

	return lock.Unlock()
}

func (d *VMDirectory) AccessDate() (time.Time, error) {
	return prune.URLAccessDate(d.BaseURL)
}

func (d *VMDirectory) AllocatedSizeBytes() (int, error) {
	return d.sumComponents(func(p *prune.PrunableURL) (int, error) { return p.AllocatedSizeBytes() })
}

func (d *VMDirectory) AllocatedSizeGB() (int, error) {
	bytes, err := d.AllocatedSizeBytes()
	return bytes / 1000 / 1000 / 1000, err
}

func (d *VMDirectory) DeduplicatedSizeBytes() (int, error) {
	return d.sumComponents(func(p *prune.PrunableURL) (int, error) { return p.DeduplicatedSizeBytes() })
}

func (d *VMDirectory) DeduplicatedSizeGB() (int, error) {
	bytes, err := d.DeduplicatedSizeBytes()
	return bytes / 1000 / 1000 / 1000, err
}

func (d *VMDirectory) SizeBytes() (int, error) {
	return d.sumComponents(func(p *prune.PrunableURL) (int, error) { return p.SizeBytes() })
}

func (d *VMDirectory) SizeGB() (int, error) {
	bytes, err := d.SizeBytes()
	return bytes / 1000 / 1000 / 1000, err
}

// DiskSizeBytes ports VMDirectory.diskSizeBytes().
func (d *VMDirectory) DiskSizeBytes() (int, error) {
	config, err := vmconfig.NewVMConfigFromURL(d.ConfigURL())
	if err != nil {
		return 0, err
	}

	if config.DiskFormat == diskimage.DiskImageFormatASIF {
		info, err := diskimage.DiskutilImageInfo(d.DiskURL())
		if err != nil {
			return 0, err
		}
		return info.TotalBytes()
	}
	return d.SizeBytes()
}

func (d *VMDirectory) DiskSizeGB() (int, error) {
	bytes, err := d.DiskSizeBytes()
	return bytes / 1000 / 1000 / 1000, err
}

// MarkExplicitlyPulled ports VMDirectory.markExplicitlyPulled().
func (d *VMDirectory) MarkExplicitlyPulled() {
	foundation.NSFileManagerDefaultManager().
		CreateFileAtPathContentsAttributes(d.ExplicitlyPulledMark().Path(), objcutil.BytesToNSData(nil), nil)
}

// IsExplicitlyPulled ports VMDirectory.isExplicitlyPulled().
func (d *VMDirectory) IsExplicitlyPulled() bool {
	return foundation.NSFileManagerDefaultManager().
		FileExistsAtPath(d.ExplicitlyPulledMark().Path())
}

func (d *VMDirectory) sumComponents(size func(*prune.PrunableURL) (int, error)) (int, error) {
	total := 0
	for _, url := range []*foundation.NSURL{d.ConfigURL(), d.DiskURL(), d.NvramURL()} {
		n, err := size(prune.NewPrunableURL(url))
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// ByteCountString mirrors ByteCountFormatter().string(fromByteCount:).
func ByteCountString(byteCount int64) string {
	return objcutil.GoStr(foundation.NSByteCountFormatterStringFromByteCountCountStyle(
		byteCount, foundation.NSByteCountFormatterCountStyleFile))
}
