// Port of tart's OCI/Authentication.swift.
//go:build darwin

package oci

import "encoding/base64"

// Authentication ports tart's Authentication protocol.
type Authentication interface {
	Header() (string, string)
	IsValid() bool
}

// BasicAuthentication ports tart's BasicAuthentication struct.
type BasicAuthentication struct {
	User     string
	Password string
}

var _ Authentication = BasicAuthentication{}

func (a BasicAuthentication) Header() (string, string) {
	creds := base64.StdEncoding.EncodeToString([]byte(a.User + ":" + a.Password))
	return "Authorization", "Basic " + creds
}

func (a BasicAuthentication) IsValid() bool { return true }
