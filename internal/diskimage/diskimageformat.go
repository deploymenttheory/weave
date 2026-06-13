// Port of tart's DiskImageFormat.swift. The ExpressibleByArgument
// conformance becomes ParseDiskImageFormat for the CLI layer.
//go:build darwin

package diskimage

import (
	"strings"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
)

// DiskImageFormat mirrors tart's DiskImageFormat enum.
type DiskImageFormat string

const (
	DiskImageFormatRaw  DiskImageFormat = "raw"
	DiskImageFormatASIF DiskImageFormat = "asif"
)

// DiskImageFormatAllCases mirrors CaseIterable.allCases.
var DiskImageFormatAllCases = []DiskImageFormat{DiskImageFormatRaw, DiskImageFormatASIF}

// DisplayName mirrors DiskImageFormat.displayName.
func (f DiskImageFormat) DisplayName() string {
	switch f {
	case DiskImageFormatASIF:
		return "ASIF (Apple Sparse Image Format)"
	default:
		return "RAW"
	}
}

// IsSupported reports whether the format is supported on the current system;
// ASIF requires macOS 26.
func (f DiskImageFormat) IsSupported() bool {
	switch f {
	case DiskImageFormatASIF:
		return foundation.NSProcessInfoProcessInfo().
			IsOperatingSystemAtLeastVersion(foundation.NSOperatingSystemVersion{MajorVersion: 26})
	default:
		return true
	}
}

// ParseDiskImageFormat ports the ExpressibleByArgument init?(argument:).
func ParseDiskImageFormat(argument string) (DiskImageFormat, bool) {
	format := DiskImageFormat(strings.ToLower(argument))
	switch format {
	case DiskImageFormatRaw, DiskImageFormatASIF:
		return format, true
	default:
		return "", false
	}
}

// DiskImageFormatAllValueStrings mirrors allValueStrings.
func DiskImageFormatAllValueStrings() []string {
	values := make([]string, 0, len(DiskImageFormatAllCases))
	for _, format := range DiskImageFormatAllCases {
		values = append(values, string(format))
	}
	return values
}
