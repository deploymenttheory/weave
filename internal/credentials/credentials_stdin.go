// Port of tart's Credentials/StdinCredentials.swift. readpassphrase(3) has
// no binding, so sensitive reads disable terminal echo via termios.
//go:build darwin

package credentials

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"github.com/deploymenttheory/weave/internal/terminal"
)

const stdinCredentialMaxCharacters = 8192

// StdinCredentialsError ports the StdinCredentialsError enum.
type StdinCredentialsError struct {
	Message string
}

func (e *StdinCredentialsError) Error() string { return e.Message }

// StdinCredentialsRetrieve ports StdinCredentials.retrieve().
func StdinCredentialsRetrieve() (string, string, error) {
	user, err := readStdinCredential("username", "User: ", false)
	if err != nil {
		return "", "", err
	}
	password, err := readStdinCredential("password", "Password: ", true)
	if err != nil {
		return "", "", err
	}
	return user, password, nil
}

func readStdinCredential(name string, prompt string, isSensitive bool) (string, error) {
	fmt.Fprint(os.Stderr, prompt)

	if isSensitive {
		var termios syscall.Termios
		echoDisabled := false
		if err := terminal.TermIoctl(os.Stdin.Fd(), syscall.TIOCGETA, unsafe.Pointer(&termios)); err == nil {
			withoutEcho := termios
			withoutEcho.Lflag &^= syscall.ECHO
			if err := terminal.TermIoctl(os.Stdin.Fd(), syscall.TIOCSETA, unsafe.Pointer(&withoutEcho)); err == nil {
				echoDisabled = true
			}
		}
		if echoDisabled {
			defer func() {
				_ = terminal.TermIoctl(os.Stdin.Fd(), syscall.TIOCSETA, unsafe.Pointer(&termios))
				fmt.Fprintln(os.Stderr)
			}()
		}
	}

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", &StdinCredentialsError{Message: name + " is required"}
	}
	credential := strings.Trim(line, "\r\n")

	if len(credential) > stdinCredentialMaxCharacters {
		return "", &StdinCredentialsError{
			Message: fmt.Sprintf("%s should contain no more than %d characters", name, stdinCredentialMaxCharacters)}
	}

	return credential, nil
}
