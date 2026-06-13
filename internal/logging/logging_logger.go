// Port of tart's Logging/Logger.swift.
//go:build darwin

package logging

import (
	"fmt"
	"sync"

	"github.com/deploymenttheory/weave/internal/objcutil"
)

// Logger ports tart's Logger protocol.
type Logger interface {
	AppendNewLine(line string)
	UpdateLastLine(line string)
}

// DefaultLogger ports the DefaultLogger global: simple logging on CI,
// interactive (line-rewriting) logging otherwise.
var DefaultLogger = sync.OnceValue(func() Logger {
	if _, ok := objcutil.EnvironmentValue("CI"); ok {
		return &SimpleConsoleLogger{}
	}
	return &InteractiveConsoleLogger{}
})

// InteractiveConsoleLogger rewrites the last line using ANSI escapes.
type InteractiveConsoleLogger struct{}

const (
	eraseCursorDown     = "\x1b[J"  // clear entire line
	moveUp              = "\x1b[1A" // move one line up
	moveBeginningOfLine = "\r"
)

func (l *InteractiveConsoleLogger) AppendNewLine(line string) {
	fmt.Println(line)
}

func (l *InteractiveConsoleLogger) UpdateLastLine(line string) {
	fmt.Print(moveUp, moveBeginningOfLine, eraseCursorDown, line, "\n")
}

// SimpleConsoleLogger appends every update as a new line.
type SimpleConsoleLogger struct{}

func (l *SimpleConsoleLogger) AppendNewLine(line string) {
	fmt.Println(line)
}

func (l *SimpleConsoleLogger) UpdateLastLine(line string) {
	fmt.Println(line)
}
