// Port of tart's Commands/Export.swift.
//go:build darwin

package command

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/deploymenttheory/weave/internal/vmstorage"
)

// ExportCommand ports the Export command.
type ExportCommand struct {
	Name string
	Path string // optional; defaults to "<name>.tvm"
}

func (c *ExportCommand) Run(ctx context.Context) error {
	correctedPath := c.Path

	if correctedPath == "" {
		correctedPath = c.Name + ".tvm"

		if _, err := os.Stat(correctedPath); err == nil {
			if !userWantsOverwrite(correctedPath) {
				return nil
			}
		}
	}

	fmt.Println("exporting...")

	vmDir, err := vmstorage.VMStorageHelperOpen(c.Name)
	if err != nil {
		return err
	}
	return vmDir.ExportToArchive(correctedPath)
}

func userWantsOverwrite(filename string) bool {
	fmt.Printf("file %s already exists, are you sure you want to overwrite it? (yes, [no])? ", filename)

	answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	return strings.TrimSpace(answer) == "yes"
}
