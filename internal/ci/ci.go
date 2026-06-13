// Port of tart's CI/CI.swift: build version information. The CIRRUS_TAG
// template substitution becomes an ldflags-settable variable:
//
//	go build -ldflags "-X github.com/deploymenttheory/weave/internal/ci.ciRawVersion=v1.2.3"
//go:build darwin

package ci

import "strings"

var ciRawVersion = ""

// CIVersion ports CI.version.
func CIVersion() string {
	if ciVersionExpanded() {
		return ciRawVersion
	}
	return "SNAPSHOT"
}

// CIRelease ports CI.release; empty string mirrors Swift's nil.
func CIRelease() string {
	if ciVersionExpanded() {
		return "weave@" + ciRawVersion
	}
	return ""
}

func ciVersionExpanded() bool {
	return ciRawVersion != "" && !strings.HasPrefix(ciRawVersion, "$")
}
