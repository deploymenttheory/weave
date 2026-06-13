// Port of tart's VMStorageLocal.swift: the ~/.weave/vms local VM store.
//go:build darwin

package vmstorage

import (
	"time"

	weaveconfig "github.com/deploymenttheory/weave/internal/config"
	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/prune"
	"github.com/deploymenttheory/weave/internal/vmdirectory"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
)

// VMStorageLocal ports tart's VMStorageLocal class.
type VMStorageLocal struct {
	BaseURL *foundation.NSURL
}

var _ prune.PrunableStorage = (*VMStorageLocal)(nil)

// NewVMStorageLocal ports VMStorageLocal.init().
func NewVMStorageLocal() (*VMStorageLocal, error) {
	config, err := weaveconfig.NewConfig()
	if err != nil {
		return nil, err
	}
	return &VMStorageLocal{
		BaseURL: config.WeaveHomeDir.URLByAppendingPathComponentIsDirectory(objcutil.NSStr("vms"), true),
	}, nil
}

func (s *VMStorageLocal) vmURL(name string) *foundation.NSURL {
	return s.BaseURL.URLByAppendingPathComponentIsDirectory(objcutil.NSStr(name), true)
}

// Exists ports VMStorageLocal.exists(_:).
func (s *VMStorageLocal) Exists(name string) bool {
	return vmdirectory.NewVMDirectory(s.vmURL(name)).Initialized()
}

// Open ports VMStorageLocal.open(_:).
func (s *VMStorageLocal) Open(name string) (*vmdirectory.VMDirectory, error) {
	vmDir := vmdirectory.NewVMDirectory(s.vmURL(name))

	if err := vmDir.Validate(name); err != nil {
		return nil, err
	}

	if err := prune.URLUpdateAccessDate(vmDir.BaseURL, time.Now()); err != nil {
		return nil, err
	}

	return vmDir, nil
}

// Create ports VMStorageLocal.create(_:overwrite:).
func (s *VMStorageLocal) Create(name string, overwrite bool) (*vmdirectory.VMDirectory, error) {
	vmDir := vmdirectory.NewVMDirectory(s.vmURL(name))

	if err := vmDir.Initialize(overwrite); err != nil {
		return nil, err
	}

	return vmDir, nil
}

// Move ports VMStorageLocal.move(_:from:).
func (s *VMStorageLocal) Move(name string, from *vmdirectory.VMDirectory) error {
	if _, err := foundation.NSFileManagerDefaultManager().
		CreateDirectoryAtURLWithIntermediateDirectoriesAttributesError(s.BaseURL, true, nil); err != nil {
		return err
	}
	return FileManagerReplaceItem(s.vmURL(name), from.BaseURL)
}

// Rename ports VMStorageLocal.rename(_:_:).
func (s *VMStorageLocal) Rename(name string, newName string) error {
	return FileManagerReplaceItem(s.vmURL(newName), s.vmURL(name))
}

// Delete ports VMStorageLocal.delete(_:).
func (s *VMStorageLocal) Delete(name string) error {
	return vmdirectory.NewVMDirectory(s.vmURL(name)).Delete()
}

// LocalVMEntry is one element of VMStorageLocal.list()'s (name, VMDirectory)
// result tuple.
type LocalVMEntry struct {
	Name  string
	VMDir *vmdirectory.VMDirectory
}

// List ports VMStorageLocal.list().
func (s *VMStorageLocal) List() ([]LocalVMEntry, error) {
	entries, err := foundation.NSFileManagerDefaultManager().
		ContentsOfDirectoryAtURLIncludingPropertiesForKeysOptionsError(
			s.BaseURL, objcutil.EmptyNSArray[*foundation.NSString](),
			foundation.NSDirectoryEnumerationSkipsSubdirectoryDescendants)
	if err != nil {
		if isFileNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	var dirs []LocalVMEntry
	for _, url := range objcutil.NSArrayURLs(entries) {
		vmDir := vmdirectory.NewVMDirectory(url)
		if !vmDir.Initialized() {
			continue
		}
		dirs = append(dirs, LocalVMEntry{Name: vmDir.Name(), VMDir: vmDir})
	}
	return dirs, nil
}

// Prunables ports VMStorageLocal.prunables(): every VM not currently running.
func (s *VMStorageLocal) Prunables() ([]prune.Prunable, error) {
	dirs, err := s.List()
	if err != nil {
		return nil, err
	}

	var prunables []prune.Prunable
	for _, entry := range dirs {
		running, err := entry.VMDir.Running()
		if err != nil {
			return nil, err
		}
		if !running {
			prunables = append(prunables, entry.VMDir)
		}
	}
	return prunables, nil
}

// HasVMsWithMACAddress ports VMStorageLocal.hasVMsWithMACAddress(macAddress:).
func (s *VMStorageLocal) HasVMsWithMACAddress(macAddress string) (bool, error) {
	dirs, err := s.List()
	if err != nil {
		return false, err
	}
	for _, entry := range dirs {
		mac, err := entry.VMDir.MACAddress()
		if err != nil {
			return false, err
		}
		if mac == macAddress {
			return true, nil
		}
	}
	return false, nil
}

// FileManagerReplaceItem mirrors FileManager.replaceItemAt(_:withItemAt:):
// atomically replace originalItem with newItem. The generated binding for
// replaceItemAtURL:… cannot express its NSURL** out-parameter, so this uses
// remove + move, which is equivalent for tart's same-volume usage.
func FileManagerReplaceItem(originalItem *foundation.NSURL, newItem *foundation.NSURL) error {
	fileManager := foundation.NSFileManagerDefaultManager()
	if fileManager.FileExistsAtPath(originalItem.Path()) {
		if _, err := fileManager.RemoveItemAtURLError(originalItem); err != nil {
			return err
		}
	}
	_, err := fileManager.MoveItemAtURLToURLError(newItem, originalItem)
	return err
}
