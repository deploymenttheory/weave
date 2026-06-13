// Port of tart's URL+Prunable.swift. Swift conforms URL itself to Prunable;
// Go cannot extend NSURL, so PrunableURL adapts a plain file URL instead.
// The XAttr package dependency becomes raw getxattr/setxattr syscalls.
//go:build darwin

package prune

import (
	"fmt"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/objcutil"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego"
)

const deduplicatedBytesXattr = "run.weave.deduplicated-bytes"

// PrunableURL adapts a plain file NSURL to the Prunable interface.
type PrunableURL struct {
	url *foundation.NSURL
}

var _ Prunable = (*PrunableURL)(nil)

func NewPrunableURL(url *foundation.NSURL) *PrunableURL {
	return &PrunableURL{url: url}
}

func (p *PrunableURL) URL() *foundation.NSURL { return p.url }

func (p *PrunableURL) Delete() error {
	_, err := foundation.NSFileManagerDefaultManager().RemoveItemAtURLError(p.url)
	return err
}

func (p *PrunableURL) AccessDate() (time.Time, error) {
	return URLAccessDate(p.url)
}

func (p *PrunableURL) AllocatedSizeBytes() (int, error) {
	return p.intResourceValue(objcutil.WrapperID(foundation.NSURLTotalFileAllocatedSizeKey()), "totalFileAllocatedSize")
}

func (p *PrunableURL) SizeBytes() (int, error) {
	return p.intResourceValue(objcutil.WrapperID(foundation.NSURLTotalFileSizeKey()), "totalFileSize")
}

// DeduplicatedSizeBytes ports URL.deduplicatedSizeBytes(): the recorded
// deduplicated byte count, valid only while the file may share content with
// its clone origin.
func (p *PrunableURL) DeduplicatedSizeBytes() (int, error) {
	mayShareID, err := objcutil.URLResourceValue(p.url, objcutil.WrapperID(foundation.NSURLMayShareFileContentKey()))
	if err != nil {
		return 0, err
	}
	if mayShareID != 0 && foundation.NSNumberFromID(purego.Retain(mayShareID)).BoolValue() {
		return int(p.DeduplicatedBytes()), nil
	}
	return 0, nil
}

// SetDeduplicatedBytes ports URL.setDeduplicatedBytes(_:). The Swift original
// uses try! — failure to write the xattr is equally fatal here.
func (p *PrunableURL) SetDeduplicatedBytes(size uint64) {
	value := fmt.Appendf(nil, "%d", size)
	if err := setxattrFile(objcutil.GoStr(p.url.Path()), deduplicatedBytesXattr, value); err != nil {
		panic(err)
	}
}

// DeduplicatedBytes ports URL.deduplicatedBytes(): 0 on any failure.
func (p *PrunableURL) DeduplicatedBytes() uint64 {
	data, err := getxattrFile(objcutil.GoStr(p.url.Path()), deduplicatedBytesXattr)
	if err != nil {
		return 0
	}
	value, err := strconv.ParseUint(string(data), 10, 64)
	if err != nil {
		return 0
	}
	return value
}

func (p *PrunableURL) intResourceValue(keyID purego.ID, name string) (int, error) {
	id, err := objcutil.URLResourceValue(p.url, keyID)
	if err != nil {
		return 0, err
	}
	if id == 0 {
		return 0, weaveerrors.ErrGeneric("missing %s resource value for %s", name, objcutil.GoStr(p.url.Path()))
	}
	return foundation.NSNumberFromID(purego.Retain(id)).IntegerValue(), nil
}

// setxattrFile and getxattrFile replace the Swift XAttr package; package
// syscall exposes no xattr wrappers on darwin.

func setxattrFile(path string, name string, value []byte) error {
	pathPtr, err := syscall.BytePtrFromString(path)
	if err != nil {
		return err
	}
	namePtr, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	var valuePtr unsafe.Pointer
	if len(value) > 0 {
		valuePtr = unsafe.Pointer(&value[0])
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_SETXATTR,
		uintptr(unsafe.Pointer(pathPtr)), uintptr(unsafe.Pointer(namePtr)),
		uintptr(valuePtr), uintptr(len(value)), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func getxattrFile(path string, name string) ([]byte, error) {
	pathPtr, err := syscall.BytePtrFromString(path)
	if err != nil {
		return nil, err
	}
	namePtr, err := syscall.BytePtrFromString(name)
	if err != nil {
		return nil, err
	}

	size, _, errno := syscall.Syscall6(syscall.SYS_GETXATTR,
		uintptr(unsafe.Pointer(pathPtr)), uintptr(unsafe.Pointer(namePtr)), 0, 0, 0, 0)
	if errno != 0 {
		return nil, errno
	}
	if size == 0 {
		return nil, nil
	}

	buffer := make([]byte, size)
	read, _, errno := syscall.Syscall6(syscall.SYS_GETXATTR,
		uintptr(unsafe.Pointer(pathPtr)), uintptr(unsafe.Pointer(namePtr)),
		uintptr(unsafe.Pointer(&buffer[0])), size, 0, 0)
	if errno != 0 {
		return nil, errno
	}
	return buffer[:read], nil
}
