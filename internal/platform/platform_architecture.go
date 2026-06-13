// Port of tart's Platform/Architecture.swift.
//go:build darwin

package platform

import "runtime"

// Architecture mirrors tart's Architecture enum.
type Architecture string

const (
	ArchitectureARM64 Architecture = "arm64"
	ArchitectureAMD64 Architecture = "amd64"
)

// CurrentArchitecture ports CurrentArchitecture().
func CurrentArchitecture() Architecture {
	if runtime.GOARCH == "amd64" {
		return ArchitectureAMD64
	}
	return ArchitectureARM64
}
