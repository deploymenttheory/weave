// Host macOS version checks shared across packages.
//go:build darwin

package platform

import (
	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
)

// MacOSAtLeast mirrors Swift's #available(macOS N, *) checks.
func MacOSAtLeast(major int) bool {
	return foundation.NSProcessInfoProcessInfo().
		IsOperatingSystemAtLeastVersion(foundation.NSOperatingSystemVersion{MajorVersion: major})
}
