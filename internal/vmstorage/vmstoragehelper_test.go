//go:build darwin

package vmstorage

import (
	"errors"
	"testing"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/vmdirectory"
)

// TestMissingVMWrapPIDLock verifies missingVMWrap converts a missing PID lock
// into the exit-2 VM-not-found error, as tart's VMStorageHelper does.
func TestMissingVMWrapPIDLock(t *testing.T) {
	_, err := missingVMWrap("ghost", func() (*vmdirectory.VMDirectory, error) {
		return nil, weaveerrors.ErrPIDLockMissing("no lock")
	})
	var vmErr *weaveerrors.VMError
	if !errors.As(err, &vmErr) || vmErr.Kind != weaveerrors.VMErrorNotFound {
		t.Fatalf("missingVMWrap returned %v, want weaveerrors.VMErrorNotFound", err)
	}
	if vmErr.ExitCode() != 2 {
		t.Fatalf("ExitCode() = %d, want 2", vmErr.ExitCode())
	}
}
