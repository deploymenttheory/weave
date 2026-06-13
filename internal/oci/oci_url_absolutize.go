// Port of tart's OCI/URL+Absolutize.swift.
//go:build darwin

package oci

import (
	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
)

// urlAbsolutize ports URL.absolutize(_:): resolves url against baseURL when
// url is relative.
func urlAbsolutize(url *foundation.NSURL, baseURL *foundation.NSURL) *foundation.NSURL {
	return foundation.NSURLURLWithStringRelativeToURL(url.AbsoluteString(), baseURL)
}
