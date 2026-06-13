//go:build darwin

package errors

import (
	"testing"
)

// TestErrorExitCodes pins the tart exit-code contract across the lume-style
// domain error types: only VM not-found / not-running / already-running exit
// with 2, everything else with 1.
func TestErrorExitCodes(t *testing.T) {
	cases := []struct {
		name string
		err  HasExitCode
		want int32
	}{
		{"VMDoesNotExist", ErrVMDoesNotExist("vm"), 2},
		{"VMNotRunning", ErrVMNotRunning("vm"), 2},
		{"VMAlreadyRunning", ErrVMAlreadyRunning("VM %q is already running", "vm"), 2},
		{"VMIsRunning", ErrVMIsRunning("vm"), 1},
		{"VMAlreadyExists", ErrVMAlreadyExists("vm"), 1},
		{"VMInternal", ErrVMInternal("boom"), 1},
		{"Generic", ErrGeneric("usage"), 1},
		{"VMConfigurationError", ErrVMConfigurationError("bad config"), 1},
		{"InvalidDiskSize", ErrInvalidDiskSize("bad size"), 1},
		{"VMMissingFiles", ErrVMMissingFiles("missing"), 1},
		{"PIDLockMissing", ErrPIDLockMissing("missing"), 1},
		{"PullFailed", ErrPullFailed("pull"), 1},
		{"InvalidCredentials", ErrInvalidCredentials("creds"), 1},
		{"OCIStorageError", ErrOCIStorageError("oci"), 1},
		{"StorageLocationNotFound", ErrStorageLocationNotFound("loc"), 1},
		{"HealthCheckFailed", ErrHealthCheckFailed("probe"), 1},
	}
	for _, c := range cases {
		if got := c.err.ExitCode(); got != c.want {
			t.Errorf("%s: ExitCode() = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestErrorMessagesPreserved pins user-visible messages that predate the lume
// error port so CLI output stays byte-identical to tart's wording.
func TestErrorMessagesPreserved(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{ErrVMDoesNotExist("foo"), `the specified VM "foo" does not exist`},
		{ErrVMNotRunning("foo"), `VM "foo" is not running`},
		{ErrVMIsRunning("foo"), `VM "foo" is running`},
		{ErrFailedToParseRemoteName("bad"), "failed to parse remote name: bad"},
		{ErrSuspendFailed("nope"), "Failed to suspend the VM: nope"},
		{ErrVirtualMachineLimitExceeded("!"), "The number of VMs exceeds the system limit!"},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("Error() = %q, want %q", got, c.want)
		}
	}
}
