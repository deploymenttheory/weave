// Port of lume's Commands/Logs.swift: view the info/error log files written
// by the file logger, with tail-by-lines and follow support. Follow mode
// polls once per second and handles truncation/rotation by re-reading from
// the start, as Logs.swift's tailLogFile does.
//go:build darwin

package command

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/logging"
)

// LogsCommand ports the logs command.
type LogsCommand struct {
	Type   string // info, error or all
	Lines  int    // 0 means the whole file
	Follow bool
}

func (c *LogsCommand) Validate() error {
	switch c.Type {
	case "info", "error", "all":
		return nil
	default:
		return weaveerrors.ErrGeneric("usage: weave logs <info|error|all> [--lines N] [-f]")
	}
}

func (c *LogsCommand) LogFiles() []struct{ Path, Prefix string } {
	dir := logging.LogsDir()
	var files []struct{ Path, Prefix string }
	if c.Type == "info" || c.Type == "all" {
		files = append(files, struct{ Path, Prefix string }{filepath.Join(dir, logging.LogFileInfoName), "INFO: "})
	}
	if c.Type == "error" || c.Type == "all" {
		files = append(files, struct{ Path, Prefix string }{filepath.Join(dir, logging.LogFileErrorName), "ERROR: "})
	}
	// Prefixes only make sense when interleaving both files.
	if c.Type != "all" {
		files[0].Prefix = ""
	}
	return files
}

func (c *LogsCommand) Run(ctx context.Context) error {
	if logging.LogsDir() == "" {
		return weaveerrors.ErrGeneric("cannot determine the logs directory")
	}

	files := c.LogFiles()

	for _, file := range files {
		if err := printTail(file.Path, file.Prefix, c.Lines); err != nil {
			return err
		}
	}

	if !c.Follow {
		return nil
	}

	// Follow: poll for growth, re-reading from the start on truncation.
	offsets := make([]int64, len(files))
	for i, file := range files {
		if info, err := os.Stat(file.Path); err == nil {
			offsets[i] = info.Size()
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}

		for i, file := range files {
			info, err := os.Stat(file.Path)
			if err != nil {
				offsets[i] = 0
				continue
			}
			if info.Size() < offsets[i] {
				offsets[i] = 0 // truncated or rotated
			}
			if info.Size() == offsets[i] {
				continue
			}

			handle, err := os.Open(file.Path)
			if err != nil {
				continue
			}
			if _, err := handle.Seek(offsets[i], 0); err != nil {
				handle.Close()
				continue
			}
			data := make([]byte, info.Size()-offsets[i])
			n, _ := handle.Read(data)
			handle.Close()
			offsets[i] += int64(n)

			for line := range strings.SplitSeq(strings.TrimRight(string(data[:n]), "\n"), "\n") {
				if line != "" {
					fmt.Println(file.Prefix + line)
				}
			}
		}
	}
}

// printTail prints the last n lines of path (the whole file when n is 0).
// A missing file is not an error — there is just nothing to show yet.
func printTail(path string, prefix string, n int) error {
	return WriteTail(os.Stdout, path, prefix, n)
}

// WriteTail is printTail against an arbitrary writer (used by the HTTP
// logs endpoint).
func WriteTail(w io.Writer, path string, prefix string, n int) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for _, line := range lines {
		if line != "" {
			fmt.Fprintln(w, prefix+line)
		}
	}
	return nil
}
