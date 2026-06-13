// Port of tart's DeviceInfo/DeviceInfo.swift. The Sysctl package becomes
// syscall.Sysctl("hw.model").
//go:build darwin

package platform

import (
	"fmt"
	"sync"
	"syscall"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
)

// DeviceInfoOS ports DeviceInfo.os (memoized).
var DeviceInfoOS = sync.OnceValue(func() string {
	osVersion := foundation.NSProcessInfoProcessInfo().OperatingSystemVersion()
	return fmt.Sprintf("macOS %d.%d.%d", osVersion.MajorVersion, osVersion.MinorVersion, osVersion.PatchVersion)
})

// DeviceInfoModel ports DeviceInfo.model (memoized).
var DeviceInfoModel = sync.OnceValue(func() string {
	model, err := syscall.Sysctl("hw.model")
	if err != nil {
		return "unknown"
	}
	return model
})
