// Port of tart's Commands/Login.swift.
//go:build darwin

package command

import (
	"context"
	"io"
	"os"
	"strings"

	"github.com/deploymenttheory/weave/internal/credentials"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/oci"
)

// LoginCommand ports the Login command.
type LoginCommand struct {
	Host          string
	Username      string
	PasswordStdin bool
	Insecure      bool
	NoValidate    bool
}

func (c *LoginCommand) Validate() error {
	if (c.Username != "") != c.PasswordStdin {
		return weaveerrors.ErrGeneric("both --username and --password-stdin are required")
	}
	return nil
}

func (c *LoginCommand) Run(ctx context.Context) error {
	var user, password string

	if c.Username != "" {
		user = c.Username

		passwordData, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		// Support "echo $PASSWORD | tart login --username $USERNAME
		// --password-stdin $REGISTRY".
		password = strings.TrimRight(string(passwordData), "\r\n")
	} else {
		var err error
		user, password, err = credentials.StdinCredentialsRetrieve()
		if err != nil {
			return err
		}
	}

	credentialsProvider := &DictionaryCredentialsProvider{
		credentials: map[string][2]string{c.Host: {user, password}},
	}

	if !c.NoValidate {
		registry, err := oci.NewRegistry(c.Host, "", c.Insecure, []credentials.CredentialsProvider{credentialsProvider})
		if err != nil {
			return err
		}

		if err := registry.Ping(ctx); err != nil {
			return weaveerrors.ErrInvalidCredentials("invalid credentials: %v", err)
		}
	}

	return (&credentials.KeychainCredentialsProvider{}).Store(c.Host, user, password)
}

// DictionaryCredentialsProvider ports Login.swift's file-private provider.
type DictionaryCredentialsProvider struct {
	credentials map[string][2]string
}

var _ credentials.CredentialsProvider = (*DictionaryCredentialsProvider)(nil)

func (p *DictionaryCredentialsProvider) UserFriendlyName() string {
	return "static dictionary credentials provider"
}

func (p *DictionaryCredentialsProvider) Retrieve(host string) (string, string, bool, error) {
	creds, ok := p.credentials[host]
	if !ok {
		return "", "", false, nil
	}
	return creds[0], creds[1], true, nil
}

func (p *DictionaryCredentialsProvider) Store(host string, user string, password string) error {
	p.credentials[host] = [2]string{user, password}
	return nil
}
