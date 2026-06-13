// Port of tart's Serial.swift: allocate a PTY master for the VM's serial
// console. openpty(3) lives in libutil and has no binding, so this replicates
// it with the /dev/ptmx + TIOCPTY* ioctl sequence; the slave descriptor the
// Swift code opened and immediately closed is simply never opened.
//go:build darwin

package vm

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"github.com/deploymenttheory/weave/internal/terminal"
)

// darwin PTY ioctls (sys/ttycom.h); not exposed by package syscall.
const (
	tiocptygrant = 0x20007454 // grantpt(3)
	tiocptyunlk  = 0x20007452 // unlockpt(3)
	tiocptygname = 0x40807453 // ptsname(3), fills a 128-byte buffer
)

// CreatePTY ports Serial.swift's CreatePTY(): returns a non-blocking PTY
// master configured for 115200 baud, or -1 on failure.
func CreatePTY() int {
	masterFD, err := syscall.Open("/dev/ptmx", syscall.O_RDWR, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "openpty error: %v\n", err)
		return -1
	}

	if err := terminal.TermIoctl(uintptr(masterFD), tiocptygrant, nil); err != nil {
		fmt.Fprintf(os.Stderr, "openpty error: %v\n", err)
		return -1
	}
	if err := terminal.TermIoctl(uintptr(masterFD), tiocptyunlk, nil); err != nil {
		fmt.Fprintf(os.Stderr, "openpty error: %v\n", err)
		return -1
	}

	var ttyPath [128]byte
	if err := terminal.TermIoctl(uintptr(masterFD), tiocptygname, unsafe.Pointer(&ttyPath[0])); err != nil {
		fmt.Fprintf(os.Stderr, "openpty error: %v\n", err)
		return -1
	}

	// Set serial non-blocking.
	if err := syscall.SetNonblock(masterFD, true); err != nil {
		fmt.Fprintf(os.Stderr, "fcntl F_SETFL O_NONBLOCK error: %v\n", err)
		return -1
	}

	// Set baudrate to 115200.
	var termios syscall.Termios
	if err := terminal.TermIoctl(uintptr(masterFD), syscall.TIOCGETA, unsafe.Pointer(&termios)); err != nil {
		fmt.Fprintf(os.Stderr, "tcgetattr error: %v\n", err)
		return -1
	}
	termios.Ispeed = syscall.B115200
	termios.Ospeed = syscall.B115200
	if err := terminal.TermIoctl(uintptr(masterFD), syscall.TIOCSETA, unsafe.Pointer(&termios)); err != nil {
		fmt.Fprintf(os.Stderr, "tcsetattr error: %v\n", err)
		return -1
	}

	pathLen := 0
	for pathLen < len(ttyPath) && ttyPath[pathLen] != 0 {
		pathLen++
	}
	fmt.Printf("Successfully open pty %s\n", string(ttyPath[:pathLen]))

	return masterFD
}
