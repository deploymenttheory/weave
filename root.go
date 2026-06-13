// Port of tart's Root.swift command dispatch (the ArgumentParser
// configuration becomes hand-rolled flag.FlagSet parsing with interleaved
// positional arguments).
//go:build darwin

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/deploymenttheory/weave/internal/ci"
	weavecommand "github.com/deploymenttheory/weave/internal/command"
	"github.com/deploymenttheory/weave/internal/diskimage"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/macaddress"
	"github.com/deploymenttheory/weave/internal/mcp"
	weaveplatform "github.com/deploymenttheory/weave/internal/platform"
)

// commandRunner is the common surface of all ported commands.
type commandRunner interface {
	Run(ctx context.Context) error
}

// rootSubcommands lists the subcommands in Root.swift's order.
var rootSubcommands = []string{
	"create", "clone", "run", "set", "get", "list", "login", "logout", "ip",
	"exec", "ssh", "pull", "push", "import", "export", "prune", "rename", "stop",
	"delete", "fqn", "suspend", "ipsw", "images", "logs", "config", "serve",
	"setup",
}

// parseRootCommand parses os.Args[1:] into a runnable command. The returned
// name identifies the subcommand (used for spans and GC policy).
func parseRootCommand(args []string) (name string, runner commandRunner, err error) {
	if len(args) == 0 {
		return "", nil, weaveerrors.ErrGeneric("usage: weave <subcommand>\n\nsubcommands: %s", strings.Join(rootSubcommands, ", "))
	}

	name, rest := args[0], args[1:]

	switch name {
	case "--version", "version":
		fmt.Println(ci.CIVersion())
		return "version", nil, nil

	case "create":
		command := &weavecommand.CreateCommand{}
		fs := weavecommand.NewFlagSet(name)
		fs.StringVar(&command.FromIPSW, "from-ipsw", "", "")
		fs.BoolVar(&command.Linux, "linux", false, "")
		fs.StringVar(&command.NetProfile, "net-profile", "", "")
		diskSize := fs.Uint("disk-size", 50, "")
		diskFormat := fs.String("disk-format", "raw", "")
		positionals, err := weavecommand.ParseInterleaved(fs, rest)
		if err != nil {
			return name, nil, err
		}
		command.DiskSize = uint16(*diskSize)
		format, ok := diskimage.ParseDiskImageFormat(*diskFormat)
		if !ok {
			return name, nil, weaveerrors.ErrGeneric("unsupported disk format: %q", *diskFormat)
		}
		command.DiskFormat = format
		if len(positionals) != 1 {
			return name, nil, weaveerrors.ErrGeneric("usage: weave create <name>")
		}
		command.Name = positionals[0]
		return name, command, command.Validate()

	case "clone":
		command := &weavecommand.CloneCommand{}
		fs := weavecommand.NewFlagSet(name)
		fs.BoolVar(&command.Insecure, "insecure", false, "")
		fs.StringVar(&command.Registry, "registry", "", "")
		concurrency := fs.Uint("concurrency", 4, "")
		fs.BoolVar(&command.Deduplicate, "deduplicate", false, "")
		pruneLimit := fs.Uint("prune-limit", 100, "")
		positionals, err := weavecommand.ParseInterleaved(fs, rest)
		if err != nil {
			return name, nil, err
		}
		command.Concurrency = *concurrency
		command.PruneLimit = *pruneLimit
		if len(positionals) != 2 {
			return name, nil, weaveerrors.ErrGeneric("usage: weave clone [--registry <profile>] <source-name> <new-name>")
		}
		command.SourceName, command.NewName = positionals[0], positionals[1]
		return name, command, command.Validate()

	case "run":
		command := &weavecommand.RunCommand{}
		fs := weavecommand.NewFlagSet(name)
		fs.BoolVar(&command.NoGraphics, "no-graphics", false, "")
		fs.BoolVar(&command.Serial, "serial", false, "")
		fs.StringVar(&command.SerialPath, "serial-path", "", "")
		fs.BoolVar(&command.Graphics, "graphics", false, "")
		fs.BoolVar(&command.NoAudio, "no-audio", false, "")
		fs.BoolVar(&command.NoClipboard, "no-clipboard", false, "")
		fs.BoolVar(&command.Clipboard, "clipboard", false, "")
		fs.StringVar(&command.ClipboardUser, "clipboard-user", "weave", "")
		fs.StringVar(&command.ClipboardPassword, "clipboard-password", "weave", "")
		fs.StringVar(&command.ClipboardDirection, "clipboard-direction", "", "")
		fs.StringVar(&command.ClipboardFormats, "clipboard-formats", "", "")
		fs.StringVar(&command.ClipboardFiles, "clipboard-files", "", "")
		fs.IntVar(&command.ClipboardSessionMbps, "clipboard-session-mbps", 0, "")
		fs.IntVar(&command.ClipboardBandwidthPct, "clipboard-bandwidth-pct", 0, "")
		fs.Int64Var(&command.ClipboardMaxBytes, "clipboard-max-bytes", 0, "")
		fs.BoolVar(&command.Recovery, "recovery", false, "")
		fs.BoolVar(&command.VNC, "vnc", false, "")
		fs.BoolVar(&command.VNCExperimental, "vnc-experimental", false, "")
		fs.StringVar(&command.VNCPassword, "vnc-password", "", "")
		fs.BoolVar(&command.ShowScreen, "show-screen", false, "")
		var disks, dirs, sharedDirs, mounts, usbStorage, netBridged, netDevice weavecommand.StringSliceFlag
		fs.Var(&disks, "disk", "")
		fs.Var(&dirs, "dir", "")
		fs.Var(&sharedDirs, "shared-dir", "")
		fs.Var(&mounts, "mount", "")
		fs.Var(&usbStorage, "usb-storage", "")
		fs.Var(&netBridged, "net-bridged", "")
		fs.Var(&netDevice, "net-device", "")
		fs.StringVar(&command.NetProfile, "net-profile", "", "")
		fs.StringVar(&command.RosettaTag, "rosetta", "", "")
		fs.BoolVar(&command.Nested, "nested", false, "")
		fs.BoolVar(&command.NetSoftnet, "net-softnet", false, "")
		fs.StringVar(&command.NetSoftnetAllow, "net-softnet-allow", "", "")
		fs.StringVar(&command.NetSoftnetBlock, "net-softnet-block", "", "")
		fs.StringVar(&command.NetSoftnetExpose, "net-softnet-expose", "", "")
		fs.BoolVar(&command.NetHost, "net-host", false, "")
		fs.StringVar(&command.RootDiskOpts, "root-disk-opts", "", "")
		fs.BoolVar(&command.Suspendable, "suspendable", false, "")
		fs.BoolVar(&command.CaptureSystemKeys, "capture-system-keys", false, "")
		fs.BoolVar(&command.NoTrackpad, "no-trackpad", false, "")
		fs.BoolVar(&command.NoPointer, "no-pointer", false, "")
		fs.BoolVar(&command.NoKeyboard, "no-keyboard", false, "")
		positionals, err := weavecommand.ParseInterleaved(fs, rest)
		if err != nil {
			return name, nil, err
		}
		command.Disk, command.Dir, command.NetBridged = disks, dirs, netBridged
		command.SharedDir, command.USBStorage, command.NetDevice = sharedDirs, usbStorage, netDevice
		// --mount <iso> is sugar for a read-only --disk attachment.
		for _, mount := range mounts {
			command.Disk = append(command.Disk, mount+":ro")
		}
		// --show-screen is a view-only mode: it needs the experimental VNC
		// server to capture from, and runs headless so no native window can
		// forward the operator's input into the guest.
		if command.ShowScreen {
			command.VNCExperimental = true
			command.NoGraphics = true
		}
		if len(positionals) != 1 {
			return name, nil, weaveerrors.ErrGeneric("usage: weave run <name>")
		}
		command.Name = positionals[0]
		return name, runMainThreadAdapter{command}, command.Validate()

	case "set":
		command := &weavecommand.SetCommand{}
		fs := weavecommand.NewFlagSet(name)
		cpu := fs.Uint("cpu", 0, "")
		memory := fs.Uint64("memory", 0, "")
		display := fs.String("display", "", "")
		displayRefit := fs.Bool("display-refit", false, "")
		noDisplayRefit := fs.Bool("no-display-refit", false, "")
		fs.BoolVar(&command.RandomMAC, "random-mac", false, "")
		fs.BoolVar(&command.RandomSerial, "random-serial", false, "")
		fs.StringVar(&command.Disk, "disk", "", "")
		diskSize := fs.Uint("disk-size", 0, "")
		positionals, err := weavecommand.ParseInterleaved(fs, rest)
		if err != nil {
			return name, nil, err
		}
		if *cpu != 0 {
			cpu16 := uint16(*cpu)
			command.CPU = &cpu16
		}
		if *memory != 0 {
			command.Memory = memory
		}
		if *display != "" {
			displayConfig := weavecommand.ParseVMDisplayConfig(*display)
			command.Display = &displayConfig
		}
		if *displayRefit {
			value := true
			command.DisplayRefit = &value
		} else if *noDisplayRefit {
			value := false
			command.DisplayRefit = &value
		}
		if *diskSize != 0 {
			diskSize16 := uint16(*diskSize)
			command.DiskSize = &diskSize16
		}
		if len(positionals) != 1 {
			return name, nil, weaveerrors.ErrGeneric("usage: weave set <name>")
		}
		command.Name = positionals[0]
		return name, command, nil

	case "get":
		command := &weavecommand.GetCommand{Format: weavecommand.FormatText}
		fs := weavecommand.NewFlagSet(name)
		format := fs.String("format", "text", "")
		positionals, err := weavecommand.ParseInterleaved(fs, rest)
		if err != nil {
			return name, nil, err
		}
		parsedFormat, ok := weavecommand.ParseFormat(*format)
		if !ok {
			return name, nil, weaveerrors.ErrGeneric("unsupported format: %q", *format)
		}
		command.Format = parsedFormat
		if len(positionals) != 1 {
			return name, nil, weaveerrors.ErrGeneric("usage: weave get <name>")
		}
		command.Name = positionals[0]
		return name, command, nil

	case "list":
		command := &weavecommand.ListCommand{Format: weavecommand.FormatText}
		fs := weavecommand.NewFlagSet(name)
		fs.StringVar(&command.Source, "source", "", "")
		format := fs.String("format", "text", "")
		fs.BoolVar(&command.Quiet, "quiet", false, "")
		fs.BoolVar(&command.Quiet, "q", command.Quiet, "")
		if _, err := weavecommand.ParseInterleaved(fs, rest); err != nil {
			return name, nil, err
		}
		parsedFormat, ok := weavecommand.ParseFormat(*format)
		if !ok {
			return name, nil, weaveerrors.ErrGeneric("unsupported format: %q", *format)
		}
		command.Format = parsedFormat
		return name, command, command.Validate()

	case "login":
		command := &weavecommand.LoginCommand{}
		fs := weavecommand.NewFlagSet(name)
		fs.StringVar(&command.Username, "username", "", "")
		fs.BoolVar(&command.PasswordStdin, "password-stdin", false, "")
		fs.BoolVar(&command.Insecure, "insecure", false, "")
		fs.BoolVar(&command.NoValidate, "no-validate", false, "")
		positionals, err := weavecommand.ParseInterleaved(fs, rest)
		if err != nil {
			return name, nil, err
		}
		if len(positionals) != 1 {
			return name, nil, weaveerrors.ErrGeneric("usage: weave login <host>")
		}
		command.Host = positionals[0]
		return name, command, command.Validate()

	case "logout":
		if len(rest) != 1 {
			return name, nil, weaveerrors.ErrGeneric("usage: weave logout <host>")
		}
		return name, &weavecommand.LogoutCommand{Host: rest[0]}, nil

	case "ip":
		command := &weavecommand.IPCommand{Resolver: macaddress.IPResolutionStrategyDHCP}
		fs := weavecommand.NewFlagSet(name)
		wait := fs.Uint("wait", 0, "")
		resolver := fs.String("resolver", "dhcp", "")
		positionals, err := weavecommand.ParseInterleaved(fs, rest)
		if err != nil {
			return name, nil, err
		}
		command.Wait = uint16(*wait)
		strategy, ok := macaddress.ParseIPResolutionStrategy(*resolver)
		if !ok {
			return name, nil, weaveerrors.ErrGeneric("unsupported resolver: %q", *resolver)
		}
		command.Resolver = strategy
		if len(positionals) != 1 {
			return name, nil, weaveerrors.ErrGeneric("usage: weave ip <name>")
		}
		command.Name = positionals[0]
		return name, command, nil

	case "exec":
		// Flags must precede the VM name; everything after it is captured
		// verbatim as the remote command (ArgumentParser's
		// .captureForPassthrough).
		command := &weavecommand.ExecCommand{}
		i := 0
		for ; i < len(rest); i++ {
			switch rest[i] {
			case "-i":
				command.Interactive = true
			case "-t":
				command.TTY = true
			case "-it", "-ti":
				command.Interactive = true
				command.TTY = true
			default:
				goto positional
			}
		}
	positional:
		if i >= len(rest) {
			return name, nil, weaveerrors.ErrGeneric("usage: weave exec [-i] [-t] <name> <command> [args...]")
		}
		command.Name = rest[i]
		command.Command = rest[i+1:]
		if len(command.Command) == 0 {
			return name, nil, weaveerrors.ErrGeneric("usage: weave exec [-i] [-t] <name> <command> [args...]")
		}
		return name, command, nil

	case "ssh":
		// Flags must precede the VM name; everything after it is the remote
		// command (interactive shell when omitted), mirroring exec's
		// captureForPassthrough handling.
		command := &weavecommand.SSHCommand{User: "weave", Password: "weave", Timeout: 60, Resolver: macaddress.IPResolutionStrategyDHCP}
		fs := weavecommand.NewFlagSet(name)
		fs.StringVar(&command.User, "user", command.User, "")
		fs.StringVar(&command.User, "u", command.User, "")
		fs.StringVar(&command.Password, "password", command.Password, "")
		fs.StringVar(&command.Password, "p", command.Password, "")
		timeout := fs.Uint64("timeout", 60, "")
		fs.Uint64Var(timeout, "t", *timeout, "")
		wait := fs.Uint("wait", 0, "")
		resolver := fs.String("resolver", "dhcp", "")
		if err := fs.Parse(rest); err != nil {
			return name, nil, err
		}
		command.Timeout = *timeout
		command.Wait = uint16(*wait)
		strategy, ok := macaddress.ParseIPResolutionStrategy(*resolver)
		if !ok {
			return name, nil, weaveerrors.ErrGeneric("unsupported resolver: %q", *resolver)
		}
		command.Resolver = strategy
		positionals := fs.Args()
		if len(positionals) == 0 {
			return name, nil, weaveerrors.ErrGeneric("usage: weave ssh [--user admin] [--password admin] [--timeout 60] <name> [command...]")
		}
		command.Name = positionals[0]
		command.Command = positionals[1:]
		return name, command, nil

	case "pull":
		command := &weavecommand.PullCommand{}
		fs := weavecommand.NewFlagSet(name)
		fs.BoolVar(&command.Insecure, "insecure", false, "")
		fs.StringVar(&command.Registry, "registry", "", "")
		concurrency := fs.Uint("concurrency", 4, "")
		fs.BoolVar(&command.Deduplicate, "deduplicate", false, "")
		positionals, err := weavecommand.ParseInterleaved(fs, rest)
		if err != nil {
			return name, nil, err
		}
		command.Concurrency = *concurrency
		if len(positionals) != 1 {
			return name, nil, weaveerrors.ErrGeneric("usage: weave pull [--registry <profile>] [--insecure] <remote-name>")
		}
		command.RemoteName = positionals[0]
		return name, command, command.Validate()

	case "push":
		command := &weavecommand.PushCommand{}
		fs := weavecommand.NewFlagSet(name)
		fs.BoolVar(&command.Insecure, "insecure", false, "")
		fs.StringVar(&command.Registry, "registry", "", "")
		concurrency := fs.Uint("concurrency", 4, "")
		fs.IntVar(&command.ChunkSize, "chunk-size", 0, "")
		var labels weavecommand.StringSliceFlag
		fs.Var(&labels, "label", "")
		fs.BoolVar(&command.PopulateCache, "populate-cache", false, "")
		positionals, err := weavecommand.ParseInterleaved(fs, rest)
		if err != nil {
			return name, nil, err
		}
		command.Concurrency = *concurrency
		command.Labels = labels
		if len(positionals) < 2 {
			return name, nil, weaveerrors.ErrGeneric("usage: weave push [--registry <profile>] <local-name> <remote-name>...")
		}
		command.LocalName = positionals[0]
		command.RemoteNames = positionals[1:]
		return name, command, nil

	case "import":
		if len(rest) != 2 {
			return name, nil, weaveerrors.ErrGeneric("usage: weave import <path> <name>")
		}
		command := &weavecommand.ImportCommand{Path: rest[0], Name: rest[1]}
		return name, command, command.Validate()

	case "export":
		command := &weavecommand.ExportCommand{}
		positionals, err := weavecommand.ParseInterleaved(weavecommand.NewFlagSet(name), rest)
		if err != nil {
			return name, nil, err
		}
		switch len(positionals) {
		case 1:
			command.Name = positionals[0]
		case 2:
			command.Name, command.Path = positionals[0], positionals[1]
		default:
			return name, nil, weaveerrors.ErrGeneric("usage: weave export <name> [path]")
		}
		return name, command, nil

	case "prune":
		command := &weavecommand.PruneCommand{Entries: "caches"}
		fs := weavecommand.NewFlagSet(name)
		fs.StringVar(&command.Entries, "entries", "caches", "")
		olderThan := fs.Uint("older-than", 0, "")
		cacheBudget := fs.Uint("cache-budget", 0, "")
		spaceBudget := fs.Uint("space-budget", 0, "")
		fs.BoolVar(&command.GC, "gc", false, "")
		if _, err := weavecommand.ParseInterleaved(fs, rest); err != nil {
			return name, nil, err
		}
		if *olderThan != 0 {
			command.OlderThan = olderThan
		}
		if *spaceBudget != 0 {
			command.SpaceBudget = spaceBudget
		}
		// --cache-budget deprecation logic.
		if *cacheBudget != 0 {
			fmt.Println("--cache-budget is deprecated, please use --space-budget")
			if command.SpaceBudget != nil {
				return name, nil, weaveerrors.ErrGeneric("--cache-budget is deprecated, please use --space-budget")
			}
			command.SpaceBudget = cacheBudget
		}
		return name, pruneAdapter{command}, command.Validate()

	case "rename":
		if len(rest) != 2 {
			return name, nil, weaveerrors.ErrGeneric("usage: weave rename <name> <new-name>")
		}
		command := &weavecommand.RenameCommand{Name: rest[0], NewName: rest[1]}
		return name, command, command.Validate()

	case "stop":
		command := &weavecommand.StopCommand{Timeout: 30}
		fs := weavecommand.NewFlagSet(name)
		timeout := fs.Uint64("timeout", 30, "")
		fs.Uint64Var(timeout, "t", *timeout, "")
		positionals, err := weavecommand.ParseInterleaved(fs, rest)
		if err != nil {
			return name, nil, err
		}
		command.Timeout = *timeout
		if len(positionals) != 1 {
			return name, nil, weaveerrors.ErrGeneric("usage: weave stop <name>")
		}
		command.Name = positionals[0]
		return name, command, nil

	case "delete":
		if len(rest) == 0 {
			return name, nil, weaveerrors.ErrGeneric("usage: weave delete <name>...")
		}
		return name, &weavecommand.DeleteCommand{Names: rest}, nil

	case "fqn":
		if len(rest) != 1 {
			return name, nil, weaveerrors.ErrGeneric("usage: weave fqn <name>")
		}
		return name, &weavecommand.FQNCommand{Name: rest[0]}, nil

	case "suspend":
		// Only available on macOS 14+ (Root.main appends it conditionally).
		if !weaveplatform.MacOSAtLeast(14) {
			return name, nil, weaveerrors.ErrGeneric("unknown subcommand %q", name)
		}
		if len(rest) != 1 {
			return name, nil, weaveerrors.ErrGeneric("usage: weave suspend <name>")
		}
		return name, &weavecommand.SuspendCommand{Name: rest[0]}, nil

	case "ipsw":
		if len(rest) != 0 {
			return name, nil, weaveerrors.ErrGeneric("usage: weave ipsw")
		}
		return name, &weavecommand.IPSWCommand{}, nil

	case "config":
		// Sub-verbs parse their own flags.
		return name, &weavecommand.ConfigCommand{Args: rest}, nil

	case "setup":
		command := &weavecommand.SetupCommand{Mode: "preset", MaxIterations: 200}
		fs := weavecommand.NewFlagSet(name)
		fs.StringVar(&command.Mode, "mode", "preset", "")
		fs.StringVar(&command.Unattended, "unattended", "", "")
		fs.StringVar(&command.AnthropicKey, "anthropic-key", "", "")
		fs.StringVar(&command.Model, "model", "claude-sonnet-4-6", "")
		maxIterations := fs.Int("max-iterations", 200, "")
		fs.StringVar(&command.SystemPrompt, "system-prompt", "", "")
		fs.BoolVar(&command.Debug, "debug", false, "")
		fs.StringVar(&command.DebugDir, "debug-dir", "", "")
		fs.BoolVar(&command.ShowScreen, "show-screen", false, "")
		positionals, err := weavecommand.ParseInterleaved(fs, rest)
		if err != nil {
			return name, nil, err
		}
		command.MaxIterations = *maxIterations
		if len(positionals) != 1 {
			return name, nil, weaveerrors.ErrGeneric("usage: weave setup [--mode preset|agent] [--unattended <preset-or-path>] [--show-screen] <name>")
		}
		command.Name = positionals[0]
		return name, command, command.Validate()

	case "serve":
		command := &mcp.ServeCommand{}
		fs := weavecommand.NewFlagSet(name)
		port := fs.Uint("port", 7777, "")
		fs.BoolVar(&command.MCP, "mcp", false, "")
		if _, err := weavecommand.ParseInterleaved(fs, rest); err != nil {
			return name, nil, err
		}
		command.Port = uint16(*port)
		return name, command, nil

	case "images":
		command := &weavecommand.ImagesCommand{}
		fs := weavecommand.NewFlagSet(name)
		fs.BoolVar(&command.Insecure, "insecure", false, "")
		fs.StringVar(&command.Registry, "registry", "", "")
		fs.BoolVar(&command.Quiet, "quiet", false, "")
		fs.BoolVar(&command.Quiet, "q", command.Quiet, "")
		positionals, err := weavecommand.ParseInterleaved(fs, rest)
		if err != nil {
			return name, nil, err
		}
		if len(positionals) != 1 {
			return name, nil, weaveerrors.ErrGeneric("usage: weave images [--registry <profile>] <host>/<repository> | <repository>")
		}
		command.Repository = positionals[0]
		return name, command, nil

	case "logs":
		command := &weavecommand.LogsCommand{}
		fs := weavecommand.NewFlagSet(name)
		fs.IntVar(&command.Lines, "lines", 0, "")
		fs.BoolVar(&command.Follow, "follow", false, "")
		fs.BoolVar(&command.Follow, "f", command.Follow, "")
		positionals, err := weavecommand.ParseInterleaved(fs, rest)
		if err != nil {
			return name, nil, err
		}
		if len(positionals) != 1 {
			return name, nil, weaveerrors.ErrGeneric("usage: weave logs <info|error|all> [--lines N] [-f]")
		}
		command.Type = positionals[0]
		return name, command, command.Validate()

	default:
		return name, nil, weaveerrors.ErrGeneric("unknown subcommand %q\n\nsubcommands: %s", name, strings.Join(rootSubcommands, ", "))
	}
}

// runMainThreadAdapter marks the run command, which must own the main
// thread (MainThreadCommand in Root.swift).
type runMainThreadAdapter struct {
	command *weavecommand.RunCommand
}

func (a runMainThreadAdapter) Run(ctx context.Context) error {
	return a.command.RunMainThread()
}

// pruneAdapter adapts PruneCommand's context-less Run.
type pruneAdapter struct {
	command *weavecommand.PruneCommand
}

func (a pruneAdapter) Run(ctx context.Context) error {
	return a.command.Run()
}
