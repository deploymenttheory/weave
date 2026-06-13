// Port of tart's VMStorageHelper.swift: the VMStorageHelper open/delete
// dispatchers, HasExitCode and the file-not-found NSError helpers. The former
// RuntimeError enum has been replaced by the lume-style domain error types in
// errors.go.
//go:build darwin

package vmstorage

import (
	"errors"
	"slices"
	"time"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/oci"
	"github.com/deploymenttheory/weave/internal/vmdirectory"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	"github.com/deploymenttheory/go-bindings-macosplatform/internal/pureobjc/objcerrors"
)

// VMStorageHelperOpen ports VMStorageHelper.open(_:): dispatches to the OCI
// or local storage depending on whether name parses as a RemoteName.
func VMStorageHelperOpen(name string) (*vmdirectory.VMDirectory, error) {
	return missingVMWrap(name, func() (*vmdirectory.VMDirectory, error) {
		if remoteName, err := oci.NewRemoteName(name); err == nil {
			storage, err := NewVMStorageOCI()
			if err != nil {
				return nil, err
			}
			return storage.Open(remoteName, time.Now())
		}

		storage, err := NewVMStorageLocal()
		if err != nil {
			return nil, err
		}
		return storage.Open(name)
	})
}

// VMStorageHelperDelete ports VMStorageHelper.delete(_:).
func VMStorageHelperDelete(name string) error {
	_, err := missingVMWrap(name, func() (*vmdirectory.VMDirectory, error) {
		if remoteName, err := oci.NewRemoteName(name); err == nil {
			storage, err := NewVMStorageOCI()
			if err != nil {
				return nil, err
			}
			return nil, storage.Delete(remoteName)
		}

		storage, err := NewVMStorageLocal()
		if err != nil {
			return nil, err
		}
		return nil, storage.Delete(name)
	})
	return err
}

// missingVMWrap ports VMStorageHelper.missingVMWrap(_:closure:): PIDLock
// and file-not-found failures become VMDoesNotExist.
func missingVMWrap(name string, closure func() (*vmdirectory.VMDirectory, error)) (*vmdirectory.VMDirectory, error) {
	result, err := closure()
	if err == nil {
		return result, nil
	}

	var dirErr *weaveerrors.VMDirectoryError
	if errors.As(err, &dirErr) && dirErr.Kind == weaveerrors.VMDirectoryErrorPIDLockMissing {
		return nil, weaveerrors.ErrVMDoesNotExist(name)
	}
	if isFileNotFound(err) {
		return nil, weaveerrors.ErrVMDoesNotExist(name)
	}

	return nil, err
}

// isFileNotFound ports tart's NSError/Error isFileNotFound() extensions: true
// when err (or any of its underlying errors) is an NSError with a Cocoa
// file-not-found code.
func isFileNotFound(err error) bool {
	var objcErr *objcerrors.ObjCError
	if !errors.As(err, &objcErr) {
		return false
	}
	return objcErrIsFileNotFound(objcErr) || slices.ContainsFunc(objcErr.Underlying, objcErrIsFileNotFound)
}

func objcErrIsFileNotFound(err *objcerrors.ObjCError) bool {
	return err.Code == foundation.NSFileNoSuchFileError || err.Code == foundation.NSFileReadNoSuchFileError
}
