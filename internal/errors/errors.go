// Port of lume's Errors/Errors.swift: domain-specific error types replacing
// the former tart-style RuntimeError. Each type carries a Kind so callers can
// match with errors.As, and implements HasExitCode (vmstoragehelper.go) so
// main.go handleError keeps tart's exit-code semantics: VM not-found /
// not-running / already-running exit 2, everything else exits 1.
//
// ResticError and VmrunError from Errors.swift are deliberately not ported:
// weave has no restic backup or VMware vmrun fallback subsystem.
//go:build darwin

package errors

import "fmt"

// ---------------------------------------------------------------------------
// UsageError — CLI usage/validation failures. lume has no generic case; the
// former RuntimeErrorGeneric call sites (argument parsing, flag validation)
// are not VM errors, so they get their own type.
// ---------------------------------------------------------------------------

type UsageError struct {
	message string
}

func (e *UsageError) Error() string { return e.message }

func (e *UsageError) ExitCode() int32 { return 1 }

func ErrGeneric(format string, params ...any) *UsageError {
	return &UsageError{message: fmt.Sprintf(format, params...)}
}

// ---------------------------------------------------------------------------
// VMError — VM lifecycle/state errors (lume VMError enum), extended with
// weave-specific kinds carried over from tart's RuntimeError where lume has
// no equivalent case.
// ---------------------------------------------------------------------------

type VMErrorKind int

const (
	// Kinds ported from lume's VMError enum.
	VMErrorAlreadyExists VMErrorKind = iota
	VMErrorNotFound
	VMErrorNotInitialized
	VMErrorNotRunning
	VMErrorAlreadyRunning
	VMErrorInstallNotStarted
	VMErrorStopTimeout
	VMErrorResizeTooSmall
	VMErrorVNCNotConfigured
	VMErrorVNCPortBindingFailed
	VMErrorInternal
	VMErrorUnsupportedOS
	VMErrorInvalidDisplayResolution
	VMErrorStillProvisioning

	// weave-specific kinds carried over from tart's RuntimeError. These keep
	// exit code 1 (only NotFound/NotRunning/AlreadyRunning exit 2, matching
	// tart: VMIsRunning was never an exit-2 case).
	VMErrorIsRunning
	VMErrorNoIPAddressFound
	VMErrorDiskAlreadyInUse
	VMErrorFailedToOpenBlockDevice
	VMErrorFailedToCreateDisk
	VMErrorFailedToResizeDisk
	VMErrorTerminationFailed
	VMErrorSocketFailed
	VMErrorExportFailed
	VMErrorImportFailed
	VMErrorSuspendFailed
	VMErrorSoftnetFailed
	VMErrorLimitExceeded
	VMErrorTerminalOperationFailed
)

type VMError struct {
	Kind    VMErrorKind
	message string
}

func (e *VMError) Error() string { return e.message }

func (e *VMError) ExitCode() int32 {
	switch e.Kind {
	case VMErrorNotFound, VMErrorNotRunning, VMErrorAlreadyRunning:
		return 2
	default:
		return 1
	}
}

func vmError(kind VMErrorKind, format string, params ...any) *VMError {
	return &VMError{Kind: kind, message: fmt.Sprintf(format, params...)}
}

// Constructors keep the tart message wording where call sites predate the
// lume port, so user-visible output is unchanged.

func ErrVMAlreadyExists(name string) *VMError {
	return vmError(VMErrorAlreadyExists, "Virtual machine already exists with name: %s", name)
}

func ErrVMDoesNotExist(name string) *VMError {
	return vmError(VMErrorNotFound, "the specified VM %q does not exist", name)
}

func ErrVMNotRunning(name string) *VMError {
	return vmError(VMErrorNotRunning, "VM %q is not running", name)
}

func ErrVMAlreadyRunning(format string, params ...any) *VMError {
	return vmError(VMErrorAlreadyRunning, format, params...)
}

func ErrVMIsRunning(name string) *VMError {
	return vmError(VMErrorIsRunning, "VM %q is running", name)
}

func ErrVMStopTimeout(name string) *VMError {
	return vmError(VMErrorStopTimeout, "Timeout while stopping virtual machine: %s", name)
}

func ErrVMInternal(format string, params ...any) *VMError {
	return vmError(VMErrorInternal, format, params...)
}

func ErrNoIPAddressFound(format string, params ...any) *VMError {
	return vmError(VMErrorNoIPAddressFound, format, params...)
}

func ErrDiskAlreadyInUse(format string, params ...any) *VMError {
	return vmError(VMErrorDiskAlreadyInUse, format, params...)
}

func ErrFailedToOpenBlockDevice(path string, explanation string) *VMError {
	return vmError(VMErrorFailedToOpenBlockDevice, "failed to open block device %s: %s", path, explanation)
}

func ErrFailedToCreateDisk(format string, params ...any) *VMError {
	return vmError(VMErrorFailedToCreateDisk, format, params...)
}

func ErrFailedToResizeDisk(format string, params ...any) *VMError {
	return vmError(VMErrorFailedToResizeDisk, format, params...)
}

func ErrVMTerminationFailed(format string, params ...any) *VMError {
	return vmError(VMErrorTerminationFailed, format, params...)
}

func ErrVMSocketFailed(port uint32, explanation string) *VMError {
	return vmError(VMErrorSocketFailed, "Failed to establish a VM socket connection to port %d: %s", port, explanation)
}

func ErrExportFailed(message string) *VMError {
	return vmError(VMErrorExportFailed, "VM export failed: %s", message)
}

func ErrImportFailed(message string) *VMError {
	return vmError(VMErrorImportFailed, "VM import failed: %s", message)
}

func ErrSuspendFailed(message string) *VMError {
	return vmError(VMErrorSuspendFailed, "Failed to suspend the VM: %s", message)
}

func ErrSoftnetFailed(message string) *VMError {
	return vmError(VMErrorSoftnetFailed, "Softnet failed: %s", message)
}

func ErrVirtualMachineLimitExceeded(hint string) *VMError {
	return vmError(VMErrorLimitExceeded, "The number of VMs exceeds the system limit%s", hint)
}

func ErrTerminalOperationFailed(format string, params ...any) *VMError {
	return vmError(VMErrorTerminalOperationFailed, format, params...)
}

// ---------------------------------------------------------------------------
// VMConfigError — VM configuration validation errors (lume VMConfigError).
// ---------------------------------------------------------------------------

type VMConfigErrorKind int

const (
	// VMConfigErrorInvalid is the generic configuration-error kind used by
	// tart-era call sites that carry a free-form message.
	VMConfigErrorInvalid VMConfigErrorKind = iota
	VMConfigErrorInvalidDiskSize
	VMConfigErrorInvalidDisplayResolution
	VMConfigErrorMalformedSizeInput
	VMConfigErrorNoBridgeInterfaceFound
	VMConfigErrorInvalidHardwareModel
	VMConfigErrorInvalidMachineIdentifier
)

type VMConfigError struct {
	Kind    VMConfigErrorKind
	message string
}

func (e *VMConfigError) Error() string { return e.message }

func (e *VMConfigError) ExitCode() int32 { return 1 }

func vmConfigError(kind VMConfigErrorKind, format string, params ...any) *VMConfigError {
	return &VMConfigError{Kind: kind, message: fmt.Sprintf(format, params...)}
}

func ErrVMConfigurationError(format string, params ...any) *VMConfigError {
	return vmConfigError(VMConfigErrorInvalid, format, params...)
}

func ErrInvalidDiskSize(format string, params ...any) *VMConfigError {
	return vmConfigError(VMConfigErrorInvalidDiskSize, format, params...)
}

func ErrInvalidDisplayResolution(resolution string) *VMConfigError {
	return vmConfigError(VMConfigErrorInvalidDisplayResolution, "Invalid display resolution: %s", resolution)
}

func ErrMalformedSizeInput(input string) *VMConfigError {
	return vmConfigError(VMConfigErrorMalformedSizeInput, "Malformed size input: %s", input)
}

func ErrNoBridgeInterfaceFound(requested string, available string) *VMConfigError {
	if requested != "" {
		return vmConfigError(VMConfigErrorNoBridgeInterfaceFound,
			"Bridge network interface '%s' not found. Available interfaces: %s", requested, available)
	}
	return vmConfigError(VMConfigErrorNoBridgeInterfaceFound,
		"No bridge network interfaces available on this host. Available: %s", available)
}

// ---------------------------------------------------------------------------
// VMDirectoryError — VM directory/config file operations (lume
// VMDirectoryError), extended with weave kinds for tart's directory-level
// RuntimeError cases (PID locks, access-date updates).
// ---------------------------------------------------------------------------

type VMDirectoryErrorKind int

const (
	// Kinds ported from lume's VMDirectoryError enum.
	VMDirectoryErrorConfigNotFound VMDirectoryErrorKind = iota
	VMDirectoryErrorInvalidConfigData
	VMDirectoryErrorDiskOperationFailed
	VMDirectoryErrorFileCreationFailed

	// weave-specific kinds carried over from tart's RuntimeError.
	VMDirectoryErrorMissingFiles
	VMDirectoryErrorAlreadyInitialized
	VMDirectoryErrorPIDLockFailed
	VMDirectoryErrorPIDLockMissing
	VMDirectoryErrorFailedToUpdateAccessDate
)

type VMDirectoryError struct {
	Kind    VMDirectoryErrorKind
	message string
}

func (e *VMDirectoryError) Error() string { return e.message }

func (e *VMDirectoryError) ExitCode() int32 { return 1 }

func vmDirectoryError(kind VMDirectoryErrorKind, format string, params ...any) *VMDirectoryError {
	return &VMDirectoryError{Kind: kind, message: fmt.Sprintf(format, params...)}
}

func ErrConfigNotFound() *VMDirectoryError {
	return vmDirectoryError(VMDirectoryErrorConfigNotFound, "VM configuration file not found")
}

func ErrInvalidConfigData(format string, params ...any) *VMDirectoryError {
	return vmDirectoryError(VMDirectoryErrorInvalidConfigData, format, params...)
}

func ErrVMMissingFiles(format string, params ...any) *VMDirectoryError {
	return vmDirectoryError(VMDirectoryErrorMissingFiles, format, params...)
}

func ErrVMDirectoryAlreadyInitialized(format string, params ...any) *VMDirectoryError {
	return vmDirectoryError(VMDirectoryErrorAlreadyInitialized, format, params...)
}

func ErrPIDLockFailed(format string, params ...any) *VMDirectoryError {
	return vmDirectoryError(VMDirectoryErrorPIDLockFailed, format, params...)
}

func ErrPIDLockMissing(format string, params ...any) *VMDirectoryError {
	return vmDirectoryError(VMDirectoryErrorPIDLockMissing, format, params...)
}

func ErrFailedToUpdateAccessDate(format string, params ...any) *VMDirectoryError {
	return vmDirectoryError(VMDirectoryErrorFailedToUpdateAccessDate, format, params...)
}

// ---------------------------------------------------------------------------
// PullError — image pulling/registry errors (lume PullError), extended with
// weave kinds for tart's registry-flavoured RuntimeError cases.
// ---------------------------------------------------------------------------

type PullErrorKind int

const (
	// PullErrorFailed is the generic pull-failure kind used by tart-era call
	// sites that carry a free-form message.
	PullErrorFailed PullErrorKind = iota
	PullErrorLayerDownloadFailed
	PullErrorManifestFetchFailed
	PullErrorTokenFetchFailed

	// weave-specific kinds carried over from tart's RuntimeError.
	PullErrorFailedToParseRemoteName
	PullErrorImproperlyFormattedHost
	PullErrorInvalidCredentials
	PullErrorOCIStorage
)

type PullError struct {
	Kind    PullErrorKind
	message string
}

func (e *PullError) Error() string { return e.message }

func (e *PullError) ExitCode() int32 { return 1 }

func pullError(kind PullErrorKind, format string, params ...any) *PullError {
	return &PullError{Kind: kind, message: fmt.Sprintf(format, params...)}
}

func ErrPullFailed(format string, params ...any) *PullError {
	return pullError(PullErrorFailed, format, params...)
}

func ErrLayerDownloadFailed(digest string) *PullError {
	return pullError(PullErrorLayerDownloadFailed, "Failed to download layer: %s", digest)
}

func ErrFailedToParseRemoteName(cause string) *PullError {
	return pullError(PullErrorFailedToParseRemoteName, "failed to parse remote name: %s", cause)
}

func ErrImproperlyFormattedHost(host string, hint string) *PullError {
	return pullError(PullErrorImproperlyFormattedHost, "improperly formatted host %q was provided%s", host, hint)
}

func ErrInvalidCredentials(format string, params ...any) *PullError {
	return pullError(PullErrorInvalidCredentials, format, params...)
}

func ErrOCIStorageError(message string) *PullError {
	return pullError(PullErrorOCIStorage, "OCI storage error: %s", message)
}

// ---------------------------------------------------------------------------
// HomeError — storage-location/settings errors (lume HomeError). Consumed by
// the config command and settings file handling.
// ---------------------------------------------------------------------------

type HomeErrorKind int

const (
	HomeErrorDirectoryCreationFailed HomeErrorKind = iota
	HomeErrorDirectoryAccessDenied
	HomeErrorInvalidHomeDirectory
	HomeErrorDefaultStorageNotDefined
	HomeErrorStorageLocationNotFound
	HomeErrorStorageLocationNotADirectory
	HomeErrorStorageLocationNotWritable
	HomeErrorInvalidStorageLocation
)

type HomeError struct {
	Kind    HomeErrorKind
	message string
}

func (e *HomeError) Error() string { return e.message }

func (e *HomeError) ExitCode() int32 { return 1 }

func homeError(kind HomeErrorKind, format string, params ...any) *HomeError {
	return &HomeError{Kind: kind, message: fmt.Sprintf(format, params...)}
}

func ErrDirectoryCreationFailed(path string) *HomeError {
	return homeError(HomeErrorDirectoryCreationFailed, "Failed to create directory at path: %s", path)
}

func ErrDirectoryAccessDenied(path string) *HomeError {
	return homeError(HomeErrorDirectoryAccessDenied, "Access denied to directory at path: %s", path)
}

func ErrInvalidHomeDirectory() *HomeError {
	return homeError(HomeErrorInvalidHomeDirectory, "Invalid home directory configuration")
}

func ErrDefaultStorageNotDefined() *HomeError {
	return homeError(HomeErrorDefaultStorageNotDefined, "Default storage location is not defined.")
}

func ErrStorageLocationNotFound(name string) *HomeError {
	return homeError(HomeErrorStorageLocationNotFound, "Storage location not found: %s", name)
}

func ErrStorageLocationNotADirectory(path string) *HomeError {
	return homeError(HomeErrorStorageLocationNotADirectory, "Storage location is not a directory: %s", path)
}

func ErrStorageLocationNotWritable(path string) *HomeError {
	return homeError(HomeErrorStorageLocationNotWritable, "Storage location is not writable: %s", path)
}

func ErrInvalidStorageLocation(name string) *HomeError {
	return homeError(HomeErrorInvalidStorageLocation, "Invalid storage location specified: %s", name)
}

// ---------------------------------------------------------------------------
// UnattendedError — unattended Setup Assistant automation errors (lume
// UnattendedError). Consumed by the setup command.
// ---------------------------------------------------------------------------

type UnattendedErrorKind int

const (
	UnattendedErrorConfigLoadFailed UnattendedErrorKind = iota
	UnattendedErrorTextNotFound
	UnattendedErrorOCRFailed
	UnattendedErrorVNCAutomationFailed
	UnattendedErrorFramebufferCaptureFailed
	UnattendedErrorInputSimulationFailed
	UnattendedErrorCommandExecutionFailed
	UnattendedErrorTimeout
	UnattendedErrorHealthCheckFailed
)

type UnattendedError struct {
	Kind    UnattendedErrorKind
	message string
}

func (e *UnattendedError) Error() string { return e.message }

func (e *UnattendedError) ExitCode() int32 { return 1 }

func UnattendedErrorf(kind UnattendedErrorKind, format string, params ...any) *UnattendedError {
	return &UnattendedError{Kind: kind, message: fmt.Sprintf(format, params...)}
}

func ErrConfigLoadFailed(reason string) *UnattendedError {
	return UnattendedErrorf(UnattendedErrorConfigLoadFailed, "Failed to load unattended config: %s", reason)
}

func ErrTextNotFound(text string, timeoutSeconds int) *UnattendedError {
	return UnattendedErrorf(UnattendedErrorTextNotFound, "Text '%s' not found on screen after %d seconds", text, timeoutSeconds)
}

func ErrOCRFailed(reason string) *UnattendedError {
	return UnattendedErrorf(UnattendedErrorOCRFailed, "OCR text recognition failed: %s", reason)
}

func ErrVNCAutomationFailed(reason string) *UnattendedError {
	return UnattendedErrorf(UnattendedErrorVNCAutomationFailed, "VNC automation failed: %s", reason)
}

func ErrFramebufferCaptureFailed(reason string) *UnattendedError {
	return UnattendedErrorf(UnattendedErrorFramebufferCaptureFailed, "Failed to capture VNC framebuffer: %s", reason)
}

func ErrInputSimulationFailed(reason string) *UnattendedError {
	return UnattendedErrorf(UnattendedErrorInputSimulationFailed, "Failed to simulate input: %s", reason)
}

func ErrCommandExecutionFailed(command string) *UnattendedError {
	return UnattendedErrorf(UnattendedErrorCommandExecutionFailed, "Failed to execute boot command: %s", command)
}

func ErrUnattendedTimeout(operation string) *UnattendedError {
	return UnattendedErrorf(UnattendedErrorTimeout, "Timeout during unattended operation: %s", operation)
}

func ErrHealthCheckFailed(reason string) *UnattendedError {
	return UnattendedErrorf(UnattendedErrorHealthCheckFailed, "Health check failed: %s", reason)
}

// HasExitCode ports tart's HasExitCode protocol: errors that map to a
// specific process exit code.
type HasExitCode interface {
	ExitCode() int32
}

// ExecCustomExitCodeError ports the error of the same name: not a failure,
// just a custom exit code propagated from the remote command.
type ExecCustomExitCodeError struct {
	Code int32
}

func (e *ExecCustomExitCodeError) Error() string {
	return fmt.Sprintf("remote command exited with code %d", e.Code)
}

// ExitCode lets the root error handler propagate the remote exit code.
func (e *ExecCustomExitCodeError) ExitCode() int32 { return e.Code }
