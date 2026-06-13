// Port of tart's FileLock.swift: an exclusive flock(2) held on an open file
// descriptor. Swift's deinit close becomes an explicit Close method.
//go:build darwin

package lock

import (
	"errors"
	"fmt"
	"syscall"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	"github.com/deploymenttheory/weave/internal/objcutil"
)

// ErrAlreadyLocked ports FileLockError.AlreadyLocked.
var ErrAlreadyLocked = errors.New("already locked")

// FileLockFailedError ports FileLockError.Failed.
type FileLockFailedError struct {
	Message string
}

func (e *FileLockFailedError) Error() string { return e.Message }

// FileLock ports tart's FileLock class.
type FileLock struct {
	URL *foundation.NSURL
	fd  int
}

// NewFileLock opens lockURL read-only for use with flock. Swift's init leaves
// open(2) unchecked and lets flock fail later with EBADF; here the open error
// surfaces immediately instead.
func NewFileLock(lockURL *foundation.NSURL) (*FileLock, error) {
	path := objcutil.GoStr(lockURL.Path())
	fd, err := syscall.Open(path, syscall.O_RDONLY, 0)
	if err != nil {
		return nil, &FileLockFailedError{Message: fmt.Sprintf("failed to lock %s: %v", path, err)}
	}
	return &FileLock{URL: lockURL, fd: fd}, nil
}

// Close releases the file descriptor (Swift's deinit).
func (l *FileLock) Close() error {
	return syscall.Close(l.fd)
}

// Trylock attempts a non-blocking exclusive lock; false means another
// process holds it.
func (l *FileLock) Trylock() (bool, error) {
	return l.flock(syscall.LOCK_EX | syscall.LOCK_NB)
}

// Lock blocks until the exclusive lock is acquired.
func (l *FileLock) Lock() error {
	_, err := l.flock(syscall.LOCK_EX)
	return err
}

// Unlock releases the lock.
func (l *FileLock) Unlock() error {
	_, err := l.flock(syscall.LOCK_UN)
	return err
}

func (l *FileLock) flock(operation int) (bool, error) {
	if err := syscall.Flock(l.fd, operation); err != nil {
		if operation&syscall.LOCK_NB != 0 && errors.Is(err, syscall.EWOULDBLOCK) {
			return false, nil
		}
		return false, &FileLockFailedError{Message: fmt.Sprintf("failed to lock %s: %v", objcutil.GoStr(l.URL.Path()), err)}
	}
	return true, nil
}
