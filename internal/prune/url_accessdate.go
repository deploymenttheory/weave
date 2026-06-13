// Port of tart's URL+AccessDate.swift. Go cannot extend NSURL, so the
// extension methods become package-level functions.
//go:build darwin

package prune

import (
	"syscall"
	"time"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/objcutil"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego"
)

// URLAccessDate ports URL.accessDate(): the contentAccessDate resource value.
func URLAccessDate(url *foundation.NSURL) (time.Time, error) {
	dateID, err := objcutil.URLResourceValue(url, objcutil.WrapperID(foundation.NSURLContentAccessDateKey()))
	if err != nil {
		return time.Time{}, err
	}
	if dateID == 0 {
		// Swift force-unwraps attrs.contentAccessDate; surface an error
		// instead of panicking.
		return time.Time{}, weaveerrors.ErrGeneric("missing contentAccessDate resource value for %s", objcutil.GoStr(url.Path()))
	}

	seconds := foundation.NSDateFromID(purego.Retain(dateID)).TimeIntervalSince1970()
	return time.Unix(0, int64(seconds*float64(time.Second))), nil
}

// URLUpdateAccessDate ports URL.updateAccessDate(_:), preserving the Swift
// original's behaviour of passing the previous access date as the new
// modification time to utimes(2).
func URLUpdateAccessDate(url *foundation.NSURL, accessDate time.Time) error {
	modificationDate, err := URLAccessDate(url)
	if err != nil {
		return err
	}

	times := []syscall.Timeval{
		dateAsTimeval(accessDate),
		dateAsTimeval(modificationDate),
	}
	if err := syscall.Utimes(objcutil.GoStr(url.Path()), times); err != nil {
		return weaveerrors.ErrFailedToUpdateAccessDate("utimes(2) failed: %v", err)
	}

	return nil
}

// dateAsTimeval ports Date.asTimeval().
func dateAsTimeval(date time.Time) syscall.Timeval {
	return syscall.Timeval{Sec: date.Unix(), Usec: 0}
}
