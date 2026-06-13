// Shared terminal helpers for the banner and the spinner: stdout TTY
// detection and ANSI colouring (suppressed when stdout is not a terminal or
// NO_COLOR is set).
//go:build darwin

package terminal

import (
	"os"
	"syscall"
	"unsafe"
)

const (
	ansiBlue  = "\033[34m"
	ansiBold  = "\033[1m"
	ansiDim   = "\033[2m"
	ansiGreen = "\033[32m"
	ansiRed   = "\033[31m"
	ansiReset = "\033[0m"
	ansiClear = "\033[K" // clear from cursor to end of line
)

// stdoutIsTerminal reports whether stdout is a terminal (reusing term.go's
// TermIoctl).
func stdoutIsTerminal() bool {
	var termios syscall.Termios
	return TermIoctl(os.Stdout.Fd(), syscall.TIOCGETA, unsafe.Pointer(&termios)) == nil
}

// colorEnabled reports whether ANSI colouring should be emitted.
func colorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return stdoutIsTerminal()
}

// paint wraps s in an ANSI code (and reset) when colouring is enabled.
func paint(code, s string) string {
	if !colorEnabled() {
		return s
	}
	return code + s + ansiReset
}

func blue(s string) string  { return paint(ansiBlue, s) }
func bold(s string) string  { return paint(ansiBold, s) }
func dim(s string) string   { return paint(ansiDim, s) }
func green(s string) string { return paint(ansiGreen, s) }
func red(s string) string   { return paint(ansiRed, s) }
