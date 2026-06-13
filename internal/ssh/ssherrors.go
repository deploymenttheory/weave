// Port of lume's SSH/SSHErrors.swift.
//go:build darwin

package ssh

import "fmt"

type SSHErrorKind int

const (
	SSHErrorConnectionFailed SSHErrorKind = iota
	SSHErrorAuthenticationFailed
	SSHErrorTimeout
	SSHErrorCommandFailed
	SSHErrorNoIPAddress
	SSHErrorNotAvailable
)

type SSHError struct {
	Kind    SSHErrorKind
	message string
}

func (e *SSHError) Error() string { return e.message }

func (e *SSHError) ExitCode() int32 { return 1 }

func sshError(kind SSHErrorKind, format string, params ...any) *SSHError {
	return &SSHError{Kind: kind, message: fmt.Sprintf(format, params...)}
}

func ErrSSHConnectionFailed(message string) *SSHError {
	return sshError(SSHErrorConnectionFailed, "SSH connection failed: %s", message)
}

func ErrSSHAuthenticationFailed() *SSHError {
	return sshError(SSHErrorAuthenticationFailed, "SSH authentication failed. Check username and password.")
}

func ErrSSHTimeout() *SSHError {
	return sshError(SSHErrorTimeout, "SSH operation timed out")
}

func ErrSSHCommandFailed(exitCode int32, message string) *SSHError {
	return sshError(SSHErrorCommandFailed, "Command failed with exit code %d: %s", exitCode, message)
}

func ErrSSHNoIPAddress(name string) *SSHError {
	return sshError(SSHErrorNoIPAddress, "VM '%s' has no IP address. Wait for it to boot completely.", name)
}

func ErrSSHNotAvailable(name string) *SSHError {
	return sshError(SSHErrorNotAvailable, "SSH is not available on VM '%s'. Ensure SSH/Remote Login is enabled.", name)
}
