// MCP screen-interaction tools: capture/OCR a running VM's screen and drive
// its mouse and keyboard over VNC. These let an MCP client (an AI agent, or a
// human doing discovery) read and operate a macOS VM's GUI by name — e.g. to
// learn exactly what each Setup Assistant screen requires on a given build.
// They reuse the unattended-automation engine, so clicks use exact-match OCR
// and keys use the correct Apple/_VZVNCServer keysym mapping.
//
// A VM exposes its screen once it is running with the experimental VNC server
// ("run <vm> --vnc-experimental"), which records the endpoint the tools
// connect to.
//go:build darwin

package mcp

import (
	"context"
	"fmt"
	"image/png"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/deploymenttheory/weave/internal/ocr"
	"github.com/deploymenttheory/weave/internal/unattended"
	"github.com/deploymenttheory/weave/internal/vmstorage"
	weavevnc "github.com/deploymenttheory/weave/internal/vnc"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var mcpVNCEndpointPattern = regexp.MustCompile(`vnc://:([^@]+)@([\d.]+):(\d+)`)

// connectVMVNC opens a VNC client to a running VM via its recorded endpoint.
// The caller closes the returned client.
func connectVMVNC(ctx context.Context, name string) (*weavevnc.VNCClient, error) {
	storage, err := vmstorage.NewVMStorageLocal()
	if err != nil {
		return nil, err
	}
	vmDir, err := storage.Open(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(vmDir.VNCEndpointPath())
	if err != nil {
		return nil, fmt.Errorf("VM %q has no VNC endpoint; start it with: weave run %s --vnc-experimental --no-graphics", name, name)
	}
	match := mcpVNCEndpointPattern.FindStringSubmatch(strings.TrimSpace(string(data)))
	if match == nil {
		return nil, fmt.Errorf("malformed VNC endpoint for VM %q", name)
	}
	port, err := strconv.Atoi(match[3])
	if err != nil {
		return nil, err
	}
	return weavevnc.DialVNC(ctx, match[2], port, match[1])
}

// mcpScreenAction connects, runs one BootCommand against the VM, and closes.
func mcpScreenAction(ctx context.Context, name string, command unattended.BootCommand) error {
	vnc, err := connectVMVNC(ctx, name)
	if err != nil {
		return err
	}
	defer vnc.Close()
	return unattended.NewAutomation(vnc, false, "").Execute(ctx, command)
}

// registerScreenTools adds the screen-interaction tools to the MCP server.
func registerScreenTools(server *mcp.Server) {
	type vmArg struct {
		Name string `json:"name" jsonschema:"name of the running VM (must be running with --vnc-experimental)"`
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "weave_screenshot",
		Description: "Capture a running VM's screen, save it as a PNG, and return the recognized on-screen text with each item's centre coordinates (for use with weave_click_at).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args vmArg) (*mcp.CallToolResult, any, error) {
		vnc, err := connectVMVNC(ctx, args.Name)
		if err != nil {
			return errorResult(err)
		}
		defer vnc.Close()
		img, err := vnc.CaptureFramebuffer(ctx)
		if err != nil {
			return errorResult(err)
		}

		path := fmt.Sprintf("/tmp/weave-screenshot-%s.png", args.Name)
		if file, createErr := os.Create(path); createErr == nil {
			_ = png.Encode(file, img)
			_ = file.Close()
		}

		observations, _ := ocr.RecognizeText(img)
		var sb strings.Builder
		fmt.Fprintf(&sb, "screen %dx%d; PNG saved to %s\non-screen text (text @ centre x,y, confidence):\n", vnc.Width, vnc.Height, path)
		for _, observation := range observations {
			centre := observation.Center()
			fmt.Fprintf(&sb, "  %q @ (%d,%d) %.2f\n", observation.Text, centre.X, centre.Y, observation.Confidence)
		}
		return textResult("%s", sb.String()), nil, nil
	})

	type clickTextArg struct {
		Name string `json:"name" jsonschema:"name of the running VM"`
		Text string `json:"text" jsonschema:"on-screen text to click (exact match preferred, else substring)"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "weave_click_text",
		Description: "Find on-screen text via OCR and click its centre.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args clickTextArg) (*mcp.CallToolResult, any, error) {
		if err := mcpScreenAction(ctx, args.Name, unattended.BootCommand{Kind: unattended.BootCommandClickText, Text: args.Text}); err != nil {
			return errorResult(err)
		}
		return textResult("clicked %q on %s", args.Text, args.Name), nil, nil
	})

	type clickAtArg struct {
		Name string `json:"name" jsonschema:"name of the running VM"`
		X    int    `json:"x" jsonschema:"x coordinate in framebuffer pixels"`
		Y    int    `json:"y" jsonschema:"y coordinate in framebuffer pixels"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "weave_click_at",
		Description: "Click at framebuffer pixel coordinates.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args clickAtArg) (*mcp.CallToolResult, any, error) {
		if err := mcpScreenAction(ctx, args.Name, unattended.BootCommand{Kind: unattended.BootCommandClickAt, X: args.X, Y: args.Y}); err != nil {
			return errorResult(err)
		}
		return textResult("clicked at (%d,%d) on %s", args.X, args.Y, args.Name), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "weave_double_click_at",
		Description: "Double-click at framebuffer pixel coordinates.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args clickAtArg) (*mcp.CallToolResult, any, error) {
		vnc, err := connectVMVNC(ctx, args.Name)
		if err != nil {
			return errorResult(err)
		}
		defer vnc.Close()
		if err := vnc.DoubleClick(args.X, args.Y); err != nil {
			return errorResult(err)
		}
		return textResult("double-clicked at (%d,%d) on %s", args.X, args.Y, args.Name), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "weave_double_click_text",
		Description: "Find on-screen text via OCR and double-click its centre (e.g. to select and advance a list item).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args clickTextArg) (*mcp.CallToolResult, any, error) {
		vnc, err := connectVMVNC(ctx, args.Name)
		if err != nil {
			return errorResult(err)
		}
		defer vnc.Close()
		img, err := vnc.CaptureFramebuffer(ctx)
		if err != nil {
			return errorResult(err)
		}
		observations, _ := ocr.RecognizeText(img)
		observation, ok := ocr.FindText(args.Text, observations)
		if !ok {
			return errorResult(fmt.Errorf("text %q not found on screen", args.Text))
		}
		centre := observation.Center()
		if err := vnc.DoubleClick(centre.X, centre.Y); err != nil {
			return errorResult(err)
		}
		return textResult("double-clicked %q at (%d,%d) on %s", args.Text, centre.X, centre.Y, args.Name), nil, nil
	})

	type typeArg struct {
		Name string `json:"name" jsonschema:"name of the running VM"`
		Text string `json:"text" jsonschema:"text to type into the focused field"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "weave_type",
		Description: "Type text into the VM's focused field.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args typeArg) (*mcp.CallToolResult, any, error) {
		if err := mcpScreenAction(ctx, args.Name, unattended.BootCommand{Kind: unattended.BootCommandTypeText, Text: args.Text}); err != nil {
			return errorResult(err)
		}
		return textResult("typed %q on %s", args.Text, args.Name), nil, nil
	})

	type keyArg struct {
		Name string `json:"name" jsonschema:"name of the running VM"`
		Key  string `json:"key" jsonschema:"a key or hotkey: enter, space, tab, esc, up/down/left/right, f1-f12, or a combo like cmd+space, cmd+q, cmd+f, shift+cmd+3"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "weave_key",
		Description: "Press a key or hotkey combination in the VM.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args keyArg) (*mcp.CallToolResult, any, error) {
		command, err := unattended.ParseBootCommand("<" + strings.TrimSpace(args.Key) + ">")
		if err != nil {
			return errorResult(fmt.Errorf("unsupported key %q: %w", args.Key, err))
		}
		if err := mcpScreenAction(ctx, args.Name, command); err != nil {
			return errorResult(err)
		}
		return textResult("pressed %q on %s", args.Key, args.Name), nil, nil
	})
}
