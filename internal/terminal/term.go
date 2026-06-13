// Port of tart's Term.swift: terminal raw mode and size queries. termios is
// pure POSIX, so this file uses ioctl(2) directly (TIOCGETA/TIOCSETA are the
// darwin equivalents of tcgetattr/tcsetattr with TCSANOW).
//go:build darwin

package terminal

import (
	"os"
	"syscall"
	"unsafe"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
)

// TermState ports tart's State struct: the terminal parameters captured
// before raw mode, used to restore the terminal afterwards.
type TermState struct {
	termios syscall.Termios
}

type winsize struct {
	Row    uint16
	Col    uint16
	Xpixel uint16
	Ypixel uint16
}

func TermIoctl(fd uintptr, request uint, argp unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(request), uintptr(argp))
	if errno != 0 {
		return errno
	}
	return nil
}

// TermIsTerminal ports Term.IsTerminal(): true when stdin is a terminal.
func TermIsTerminal() bool {
	var termios syscall.Termios
	return TermIoctl(os.Stdin.Fd(), syscall.TIOCGETA, unsafe.Pointer(&termios)) == nil
}

// TermMakeRaw ports Term.MakeRaw(): switches stdin to raw mode and returns
// the previous state for TermRestore.
func TermMakeRaw() (*TermState, error) {
	var termiosOrig syscall.Termios
	if err := TermIoctl(os.Stdin.Fd(), syscall.TIOCGETA, unsafe.Pointer(&termiosOrig)); err != nil {
		return nil, weaveerrors.ErrTerminalOperationFailed("failed to retrieve terminal parameters: %v", err)
	}

	termiosRaw := termiosOrig
	cfmakeraw(&termiosRaw)

	if err := TermIoctl(os.Stdin.Fd(), syscall.TIOCSETA, unsafe.Pointer(&termiosRaw)); err != nil {
		return nil, weaveerrors.ErrTerminalOperationFailed("failed to set terminal parameters: %v", err)
	}

	return &TermState{termios: termiosOrig}, nil
}

// TermRestore ports Term.Restore(_:).
func TermRestore(state *TermState) error {
	if err := TermIoctl(os.Stdin.Fd(), syscall.TIOCSETA, unsafe.Pointer(&state.termios)); err != nil {
		return weaveerrors.ErrTerminalOperationFailed("failed to set terminal parameters: %v", err)
	}
	return nil
}

// TermGetSize ports Term.GetSize(): the terminal dimensions of stdout.
func TermGetSize() (width uint16, height uint16, err error) {
	var size winsize
	if err := TermIoctl(os.Stdout.Fd(), syscall.TIOCGWINSZ, unsafe.Pointer(&size)); err != nil {
		return 0, 0, weaveerrors.ErrTerminalOperationFailed("failed to get terminal size: %v", err)
	}
	return size.Col, size.Row, nil
}

// cfmakeraw replicates cfmakeraw(3) from libc.
func cfmakeraw(termios *syscall.Termios) {
	termios.Iflag &^= syscall.IMAXBEL | syscall.IXOFF | syscall.INPCK | syscall.BRKINT |
		syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL |
		syscall.IXON | syscall.IGNPAR
	termios.Iflag |= syscall.IGNBRK
	termios.Oflag &^= syscall.OPOST
	termios.Lflag &^= syscall.ECHO | syscall.ECHOE | syscall.ECHOK | syscall.ECHONL |
		syscall.ICANON | syscall.ISIG | syscall.IEXTEN | syscall.NOFLSH | syscall.TOSTOP |
		syscall.PENDIN
	termios.Cflag &^= syscall.CSIZE | syscall.PARENB
	termios.Cflag |= syscall.CS8 | syscall.CREAD
	termios.Cc[syscall.VMIN] = 1
	termios.Cc[syscall.VTIME] = 0
}
