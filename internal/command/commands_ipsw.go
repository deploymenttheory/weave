// Port of lume's Commands/IPSW.swift: print the download URL of the latest
// supported macOS restore image, for manual download or use with
// "create --from-ipsw".
//go:build darwin

package command

import (
	"context"
	"fmt"

	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/terminal"
)

// IPSWCommand ports the ipsw command.
type IPSWCommand struct{}

func (c *IPSWCommand) Run(ctx context.Context) error {
	spinner := terminal.NewSpinner("Looking up the latest supported IPSW")
	spinner.Start()
	image, err := FetchLatestSupportedRestoreImage(ctx)
	if err != nil {
		spinner.Fail("Failed to look up the latest supported IPSW")
		return err
	}
	spinner.Stop()
	fmt.Println(objcutil.GoStr(image.URL().AbsoluteString()))
	return nil
}
