// Port of tart's OCI/AuthenticationKeeper.swift. The Swift actor becomes a
// mutex-guarded struct.
//go:build darwin

package oci

import "sync"

// AuthenticationKeeper ports tart's AuthenticationKeeper actor.
type AuthenticationKeeper struct {
	mu             sync.Mutex
	authentication Authentication
}

func (k *AuthenticationKeeper) Set(authentication Authentication) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.authentication = authentication
}

// Header returns the authentication header, or ok=false when no valid
// authentication is set (expired tokens suggest no headers).
func (k *AuthenticationKeeper) Header() (string, string, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.authentication == nil || !k.authentication.IsValid() {
		return "", "", false
	}

	name, value := k.authentication.Header()
	return name, value, true
}
