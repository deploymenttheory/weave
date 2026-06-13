// Port of lume's Commands/Setup.swift: run unattended Setup Assistant
// automation against a freshly created VM, either scripted (preset mode,
// YAML boot commands + OCR) or driven by Claude's computer-use tool (agent
// mode).
//go:build darwin

package command

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/passphrase"
	"github.com/deploymenttheory/weave/internal/screenviewer"
	"github.com/deploymenttheory/weave/internal/unattended"
	"github.com/deploymenttheory/weave/internal/vmstorage"
	weavevnc "github.com/deploymenttheory/weave/internal/vnc"
)

// SetupCommand ports the setup command.
type SetupCommand struct {
	Name          string
	Mode          string // "preset" or "agent"
	Unattended    string // preset name or YAML path (preset mode)
	AnthropicKey  string // agent mode; falls back to ANTHROPIC_API_KEY
	Model         string
	MaxIterations int
	SystemPrompt  string
	Debug         bool
	DebugDir      string
	ShowScreen    bool // open a view-only browser viewer of the VM screen
}

func (c *SetupCommand) Validate() error {
	switch c.Mode {
	case "preset":
		if c.Unattended == "" {
			return weaveerrors.ErrGeneric("preset mode requires --unattended <preset-or-path> (available presets: %s)",
				strings.Join(unattended.AvailableUnattendedPresets(), ", "))
		}
	case "agent":
		if c.AnthropicKey == "" {
			if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
				c.AnthropicKey = key
			} else {
				return weaveerrors.ErrGeneric("agent mode requires --anthropic-key or the ANTHROPIC_API_KEY environment variable")
			}
		}
	default:
		return weaveerrors.ErrGeneric("unknown setup mode %q: expected preset or agent", c.Mode)
	}
	return nil
}

func (c *SetupCommand) Run(ctx context.Context) error {
	if c.Mode == "preset" {
		config, err := unattended.LoadUnattendedConfig(c.Unattended)
		if err != nil {
			return err
		}
		return unattended.RunUnattendedSetup(ctx, unattended.SetupOptions{
			Name:       c.Name,
			Debug:      c.Debug,
			DebugDir:   c.DebugDir,
			ShowScreen: c.ShowScreen,
		}, config)
	}

	return c.runAgentMode(ctx)
}

// runAgentMode starts the VM with VNC (same path as preset mode) and hands
// the session to the computer-use loop (UnattendedInstaller.installWithAgent
// used a fixed 60s boot wait).
func (c *SetupCommand) runAgentMode(ctx context.Context) error {
	storage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return err
	}
	vmDir, err := storage.Open(c.Name)
	if err != nil {
		return err
	}
	if running, err := vmDir.Running(); err != nil {
		return err
	} else if running {
		return weaveerrors.ErrVMIsRunning(c.Name)
	}

	password := strings.Join(passphrase.GeneratePassphrase(4), "-")
	host, port, err := unattended.StartVMWithVNC(ctx, c.Name, password)
	if err != nil {
		return err
	}

	var viewer *screenviewer.ScreenServer
	if c.ShowScreen {
		if viewer, err = screenviewer.NewScreenServer(); err != nil {
			return err
		}
		defer viewer.Close()
		fmt.Printf("View-only screen: open %s in a browser to watch (no input reaches the VM).\n", viewer.URL())
		screenviewer.OpenInBrowser(viewer.URL())
	}

	const bootWait = 60
	fmt.Printf("Waiting %ds for the VM to boot...\n", bootWait)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(bootWait * time.Second):
	}

	vnc, err := weavevnc.DialVNC(ctx, host, port, password)
	if err != nil {
		return err
	}
	defer vnc.Close()
	fmt.Printf("Connected to VNC (%dx%d framebuffer)\n", vnc.Width, vnc.Height)

	runner := unattended.NewAgentSetupRunner(vnc, c.AnthropicKey, c.Model, c.MaxIterations, c.SystemPrompt)
	runner.Viewer = viewer
	if err := runner.Run(ctx); err != nil {
		return err
	}

	fmt.Println("Agent setup finished; VM left running.")
	return nil
}
