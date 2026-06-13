// Port of lume's Server/MCPServer.swift on the official MCP Go SDK: a stdio
// Model Context Protocol server exposing VM operations as tools so AI
// agents can drive weave directly. Tool names are lume's, renamed to the
// weave_ prefix. stdout is the protocol channel: the real stdout is handed
// to the transport and os.Stdout is pointed at stderr so stray prints from
// in-process commands cannot corrupt the framing.
//go:build darwin

package mcp

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/deploymenttheory/weave/internal/ci"
	weavecommand "github.com/deploymenttheory/weave/internal/command"
	"github.com/deploymenttheory/weave/internal/diskimage"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/logging"
	weavessh "github.com/deploymenttheory/weave/internal/ssh"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpUsageGuide = `# weave VM management

weave manages macOS and Linux virtual machines on Apple Silicon using the
Virtualization framework.

Typical workflows:

1. Create a sandbox VM from an OCI image:
   - weave_clone_vm from a previously pulled image, or
   - weave_create_vm to build a fresh VM from the latest macOS restore image.
2. Start it with weave_run_vm (headless by default; pass vnc to view it).
3. Find it with weave_list_vms / weave_get_vm (includes the IP address once
   the VM is booted).
4. Run commands inside it with weave_exec (requires SSH/Remote Login enabled
   in the guest; weave presets provision weave/weave, pulled tart-style
   images use admin/admin).
5. Stop it with weave_stop_vm and remove it with weave_delete_vm.

VM state lives under ~/.weave (or WEAVE_HOME). VMs must be stopped before
their configuration is changed or they are deleted.`

func textResult(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}},
	}
}

func errorResult(err error) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
	}, nil, nil
}

// RunMCPServer serves MCP over stdio until ctx is cancelled.
func RunMCPServer(ctx context.Context) error {
	// Protect the protocol channel from in-process command prints.
	realStdout := os.Stdout
	os.Stdout = os.Stderr
	defer func() { os.Stdout = realStdout }()

	server := mcp.NewServer(&mcp.Implementation{Name: "weave", Version: ci.CIVersion()}, nil)

	// Screen-interaction tools (screenshot/OCR, click, type, key).
	registerScreenTools(server)

	type listArgs struct {
		Source string `json:"source,omitempty" jsonschema:"VM source to list: local, oci or empty for both"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "weave_list_vms",
		Description: "List virtual machines (name, state, disk usage, last access).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args listArgs) (*mcp.CallToolResult, any, error) {
		infos, err := collectVMInfos(args.Source)
		if err != nil {
			return errorResult(err)
		}
		if len(infos) == 0 {
			return textResult("No virtual machines found."), nil, nil
		}
		var sb strings.Builder
		for _, info := range infos {
			fmt.Fprintf(&sb, "%s (%s): state=%s disk=%dGB used=%dGB accessed=%s\n",
				info.Name, info.Source, info.State, info.Disk, info.Size, info.Accessed)
		}
		return textResult("%s", sb.String()), nil, nil
	})

	type nameArgs struct {
		Name string `json:"name" jsonschema:"name of the virtual machine"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "weave_get_vm",
		Description: "Get details of one virtual machine, including its IP address when running.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args nameArgs) (*mcp.CallToolResult, any, error) {
		details, err := collectVMDetails(ctx, args.Name)
		if err != nil {
			return errorResult(err)
		}
		return textResult("name=%s os=%s cpu=%d memoryMB=%d diskGB=%d display=%s running=%v state=%s ip=%s",
			details.Name, details.OS, details.CPU, details.MemoryMB, details.DiskGB,
			details.Display, details.Running, details.State, details.IPAddress), nil, nil
	})

	type runArgs struct {
		Name       string   `json:"name" jsonschema:"name of the virtual machine"`
		VNC        bool     `json:"vnc,omitempty" jsonschema:"start a VNC server and report its URL"`
		SharedDirs []string `json:"sharedDirectories,omitempty" jsonschema:"host directories to share, path[:ro|rw]"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "weave_run_vm",
		Description: "Start a virtual machine (headless). Returns once the VM reports running.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args runArgs) (*mcp.CallToolResult, any, error) {
		extraArgs := []string{"--no-graphics"}
		if args.VNC {
			extraArgs = append(extraArgs, "--vnc-experimental")
		}
		for _, dir := range args.SharedDirs {
			extraArgs = append(extraArgs, "--shared-dir", dir)
		}
		if err := spawnDetachedRun(args.Name, extraArgs); err != nil {
			return errorResult(err)
		}
		if err := waitForVMRunning(ctx, args.Name, 30*time.Second); err != nil {
			return errorResult(err)
		}
		return textResult("VM %q is running.", args.Name), nil, nil
	})

	type stopArgs struct {
		Name    string `json:"name" jsonschema:"name of the virtual machine"`
		Timeout uint64 `json:"timeout,omitempty" jsonschema:"seconds to wait for graceful shutdown (default 30)"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "weave_stop_vm",
		Description: "Stop a running virtual machine gracefully, killing it after the timeout.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args stopArgs) (*mcp.CallToolResult, any, error) {
		timeout := args.Timeout
		if timeout == 0 {
			timeout = 30
		}
		command := &weavecommand.StopCommand{Name: args.Name, Timeout: timeout}
		if err := command.Run(ctx); err != nil {
			return errorResult(err)
		}
		return textResult("VM %q stopped.", args.Name), nil, nil
	})

	type cloneArgs struct {
		Name    string `json:"name" jsonschema:"source VM or OCI image name"`
		NewName string `json:"newName" jsonschema:"name for the new local VM"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "weave_clone_vm",
		Description: "Clone a local VM or a pulled OCI image into a new local VM.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args cloneArgs) (*mcp.CallToolResult, any, error) {
		command := &weavecommand.CloneCommand{SourceName: args.Name, NewName: args.NewName, Concurrency: 4, PruneLimit: 100}
		if err := command.Validate(); err != nil {
			return errorResult(err)
		}
		if err := command.Run(ctx); err != nil {
			return errorResult(err)
		}
		return textResult("Cloned %q to %q.", args.Name, args.NewName), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "weave_delete_vm",
		Description: "Delete a virtual machine. The VM must be stopped.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args nameArgs) (*mcp.CallToolResult, any, error) {
		command := &weavecommand.DeleteCommand{Names: []string{args.Name}}
		if err := command.Run(ctx); err != nil {
			return errorResult(err)
		}
		return textResult("VM %q deleted.", args.Name), nil, nil
	})

	type createArgs struct {
		Name       string `json:"name" jsonschema:"name for the new virtual machine"`
		Linux      bool   `json:"linux,omitempty" jsonschema:"create a Linux VM instead of macOS"`
		DiskSizeGB uint16 `json:"diskSize,omitempty" jsonschema:"disk size in GB (default 50)"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "weave_create_vm",
		Description: "Create a new VM: a macOS VM from the latest restore image (long download) or an empty Linux VM.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args createArgs) (*mcp.CallToolResult, any, error) {
		command := &weavecommand.CreateCommand{Name: args.Name, Linux: args.Linux, DiskSize: 50, DiskFormat: diskimage.DiskImageFormatRaw}
		if args.DiskSizeGB != 0 {
			command.DiskSize = args.DiskSizeGB
		}
		if !args.Linux {
			command.FromIPSW = "latest"
		}
		if err := command.Run(ctx); err != nil {
			return errorResult(err)
		}
		return textResult("VM %q created.", args.Name), nil, nil
	})

	type execArgs struct {
		Name     string `json:"name" jsonschema:"name of the running virtual machine"`
		Command  string `json:"command" jsonschema:"shell command to execute inside the VM"`
		User     string `json:"user,omitempty" jsonschema:"SSH user (default admin)"`
		Password string `json:"password,omitempty" jsonschema:"SSH password (default admin)"`
		Timeout  uint64 `json:"timeout,omitempty" jsonschema:"command timeout in seconds (default 60)"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "weave_exec",
		Description: "Execute a shell command inside a running VM over SSH and return its output.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args execArgs) (*mcp.CallToolResult, any, error) {
		details, err := collectVMDetails(ctx, args.Name)
		if err != nil {
			return errorResult(err)
		}
		if !details.Running {
			return errorResult(weaveerrors.ErrVMNotRunning(args.Name))
		}
		if details.IPAddress == "" {
			return errorResult(weavessh.ErrSSHNoIPAddress(args.Name))
		}
		user, password := args.User, args.Password
		if user == "" {
			user = "weave"
		}
		if password == "" {
			password = "weave"
		}
		timeout := args.Timeout
		if timeout == 0 {
			timeout = 60
		}
		client := weavessh.NewSSHClient(details.IPAddress, 22, user, password)
		result, err := client.Execute(ctx, args.Command, time.Duration(timeout)*time.Second)
		if err != nil {
			return errorResult(err)
		}
		response := textResult("exit code: %d\n%s", result.ExitCode, result.Output)
		response.IsError = result.ExitCode != 0
		return response, nil, nil
	})

	server.AddResource(&mcp.Resource{
		URI:         "weave://usage-guide",
		Name:        "weave usage guide",
		Description: "How to manage VMs with the weave tools.",
		MIMEType:    "text/markdown",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      "weave://usage-guide",
				MIMEType: "text/markdown",
				Text:     mcpUsageGuide,
			}},
		}, nil
	})

	prompts := []struct {
		name, description, text string
	}{
		{
			"create-sandbox",
			"Create a fresh sandbox VM for experiments",
			"Create a sandbox VM: list the available VMs and pulled images with weave_list_vms, clone the most suitable base image to a new VM named 'sandbox' with weave_clone_vm, then start it with weave_run_vm and report its IP address via weave_get_vm.",
		},
		{
			"run-in-sandbox",
			"Run a command inside the sandbox VM",
			"Ensure the VM named 'sandbox' is running (weave_get_vm, weave_run_vm if needed), then execute the user's command inside it with weave_exec and report the output.",
		},
		{
			"reset-sandbox",
			"Reset the sandbox VM to a clean state",
			"Stop the VM named 'sandbox' if it is running (weave_stop_vm), delete it (weave_delete_vm), then recreate it from the same base image with weave_clone_vm and start it again with weave_run_vm.",
		},
	}
	for _, prompt := range prompts {
		text := prompt.text
		server.AddPrompt(&mcp.Prompt{
			Name:        prompt.name,
			Description: prompt.description,
		}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{
				Messages: []*mcp.PromptMessage{{
					Role:    "user",
					Content: &mcp.TextContent{Text: text},
				}},
			}, nil
		})
	}

	logging.LogInfo("MCP server started (stdio)")
	return server.Run(ctx, &mcp.IOTransport{Reader: os.Stdin, Writer: realStdout})
}
