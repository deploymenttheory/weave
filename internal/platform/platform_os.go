// Port of tart's Platform/OS.swift.
//go:build darwin

package platform

// OS mirrors tart's OS enum.
type OS string

const (
	OSDarwin OS = "darwin"
	OSLinux  OS = "linux"
)
