// Port of tart's Credentials/CredentialsProvider.swift.
//go:build darwin

package credentials

import "fmt"

// CredentialsProviderError ports the CredentialsProviderError enum.
type CredentialsProviderError struct {
	Message string
}

func (e *CredentialsProviderError) Error() string { return e.Message }

func credentialsProviderFailed(format string, params ...any) *CredentialsProviderError {
	return &CredentialsProviderError{Message: fmt.Sprintf(format, params...)}
}

// CredentialsProvider ports tart's CredentialsProvider protocol. Retrieve
// returns ok=false when the provider has no credentials for host (Swift's
// nil tuple).
type CredentialsProvider interface {
	UserFriendlyName() string
	Retrieve(host string) (user string, password string, ok bool, err error)
	Store(host string, user string, password string) error
}
