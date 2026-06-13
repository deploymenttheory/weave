// weave is a virtual machine manager for macOS and Linux guests on Apple
// Silicon, built on this repository's purego Virtualization.framework
// bindings. It is a complete Go port of tart
// (https://github.com/cirruslabs/tart), extended with the feature set of
// lume (https://github.com/trycua/cua, libs/lume) ported from Swift.
//
// # Features ported from lume
//
// Domain error types: the tart-style RuntimeError enum is replaced by
// lume's domain-specific error types (VMError, HomeError, PullError,
// VMConfigError, VMDirectoryError, UnattendedError, SSHError, plus a
// UsageError for CLI validation), each kinded for errors.As matching and
// each carrying an exit code. The tart exit-code contract is preserved:
// VM not-found / not-running / already-running exit 2, everything else 1,
// and remote command exit codes pass through verbatim (errors.go).
//
// SSH client and command: an in-process SSH client (golang.org/x/crypto/ssh;
// password auth, combined output, interactive PTY sessions with window
// resizing) with an automatic fallback to the system ssh binary via
// SSH_ASKPASS when a direct TCP connection cannot be established, e.g. in
// sandboxed environments (sshclient.go, sshclient_system.go, commands_ssh.go).
//
// Clipboard sync: "run --clipboard" starts a bidirectional clipboard watcher
// that polls the host NSPasteboard once a second and the guest (pbpaste/
// pbcopy over SSH) with failure backoff, base64 transport, a 1MB content cap
// and sync-loop prevention. This is independent of the SPICE agent clipboard,
// which remains on unless --no-clipboard is given (clipboardwatcher.go).
//
// Run command refinements: "--shared-dir path[:ro|rw]" (lume-style directory
// sharing through the macOS automount tag, mixable with tart's --dir),
// "--mount <iso>" (read-only disk attachment sugar), "--usb-storage <img>"
// (USB mass storage devices, macOS 13+), and "--vnc-password <pw>" (a fixed
// password for the experimental VNC server instead of a generated
// passphrase — required by unattended setup).
//
// IPSW lookup: "ipsw" prints the download URL of the latest supported macOS
// restore image (commands_ipsw.go).
//
// Remote image listing: "images <host>/<repository>" lists the tags of a
// remote OCI repository via the distribution tags-list endpoint, reusing the
// registry client and its authentication (commands_images.go).
//
// File logging and the logs command: commands append to
// $WEAVE_HOME/logs/weave.{info,error}.log (10MB rotation) and
// "logs info|error|all [--lines N] [-f]" tails and follows them
// (logging_filelogger.go, commands_logs.go).
//
// Settings file and the config command: a YAML settings file at
// $XDG_CONFIG_HOME/weave/config.yaml holds named storage locations, the
// default storage, a cache directory override and named registry profiles.
// Home resolution order is WEAVE_HOME, then the settings default storage,
// then ~/.weave. "config get|storage|cache|registry|network" manages it;
// "config registry list|add|remove|default" maintains the profiles (e.g.
// "config registry add cua --organization trycua") consumed by the pull,
// clone, push and images commands (config/settings.go, commands_config.go).
//
// HTTP API server: "serve [--port 7777]" exposes the VM lifecycle as a REST
// API under /weave/* — list/get/create/update/delete/clone/run/stop/setup,
// IPSW lookup, synchronous and asynchronous pulls (202 + job polling), push,
// prune, remote image listing, config and storage-location CRUD, log
// retrieval and host status. The run endpoint spawns a detached run process
// because run owns the main thread and AppKit run loop (server_http.go,
// server_requests.go, server_service.go).
//
// MCP server: "serve --mcp" speaks the Model Context Protocol over stdio so
// AI agents can drive VMs as tools: weave_list_vms, weave_get_vm, weave_run_vm,
// weave_stop_vm, weave_clone_vm, weave_delete_vm, weave_create_vm and weave_exec
// (SSH-based), plus a usage-guide resource and sandbox prompts
// (server_mcp.go).
//
// Unattended setup: "setup <name>" automates the macOS Setup Assistant.
// Preset mode replays a YAML boot-command script (embedded presets: sequoia,
// tahoe; packer-style DSL — <wait 'text'>, <click 'text', xoffset=N>,
// <type 'hello'>, <cmd+space>, <delay 2>) against the VM's screen using a
// hand-rolled RFB 3.8 client and Vision-framework OCR, then optionally runs
// SSH/HTTP health checks and post-setup SSH commands. Agent mode hands the
// screen to Claude's computer-use tool (raw Anthropic API client, JPEG
// screenshots downscaled to 1024x768, bounded conversation history) and
// executes the model's click/type/key actions over VNC
// (unattended_*.go, vncclient.go, vnckeysyms.go, ocrservice.go,
// anthropicclient.go, agentsetuprunner.go, commands_setup.go).
//
// "setup --show-screen" opens a view-only viewer (a local MJPEG stream of
// the frames the automation captures, served over HTTP and opened in a
// browser) so an operator can watch each step — and correlate it with the
// per-step log — when tuning a preset to a new macOS build. It is
// deliberately view-only: the VM runs headless, so no native window forwards
// the operator's mouse or keyboard into the guest to fight the automation.
// Apple's _VZVNCServer modifier mapping is non-obvious — Command is delivered
// as the Alt_L X11 keysym and Option as Meta_L — which vnckeysyms.go encodes
// so Cmd-shortcut-driven steps (Spotlight, enabling Remote Login) work.
//
// # Features ported from tart
//
// VM creation: "create" builds a macOS VM by downloading and installing a
// restore image — "--from-ipsw" accepts a URL, a local file path or
// "latest" (which fetches the latest VZMacOSRestoreImage and caches it
// under $WEAVE_HOME/cache/IPSWs) — or an empty Linux VM with "--linux".
// "--disk-size" sets the disk in GB (default 50) and "--disk-format"
// chooses raw or ASIF (commands_create.go, ipswcache.go, diskimageformat.go).
//
// Running VMs: "run" boots a VM and, by default, opens an AppKit window
// hosting a VZVirtualMachineView; "--no-graphics" runs headless,
// "--graphics" forces the window even alongside VNC, and "--recovery" boots
// into macOS recovery. The window close button maps to a graceful guest
// shutdown or, when "--suspendable" is set, to a suspend (commands_run.go,
// vm.go, vm_recovery.go).
//
// Stopping and suspending: "stop" requests a graceful guest shutdown and
// force-kills after "--timeout" seconds (default 30); "suspend" snapshots a
// running VM's state to state.vzvmsave (macOS 14+), and a subsequent "run"
// restores and resumes from it (commands_stop.go, commands_suspend.go).
//
// Cloning: "clone" copies a local VM or a pulled OCI image into a new local
// VM using APFS clonefile so disks are copy-on-write; "--deduplicate"
// deduplicates identical layers and "--concurrency"/"--prune-limit" tune the
// copy (commands_clone.go).
//
// Configuration: each VM has a config.json (CPU count, memory size, display
// width/height with point or pixel units and an optional refit flag, MAC
// address, OS, architecture, and the macOS ECID and hardware model, or the
// disk format). "set" mutates a stopped VM — "--cpu", "--memory",
// "--display", "--display-refit"/"--no-display-refit", "--disk-size" (grow
// only), "--random-mac" and "--random-serial" — and "get" prints the
// configuration as text or JSON ("--format"). "list" enumerates local and/or
// OCI VMs ("--source") as text or JSON, with "--quiet" for names only
// (vmconfig.go, commands_set.go, commands_get.go, commands_list.go).
//
// Storage layout: local VMs live under $WEAVE_HOME/vms, OCI images are cached
// under $WEAVE_HOME/cache/OCIs with per-layer deduplication, and in-progress
// work is staged under $WEAVE_HOME/tmp with flock-based cleanup. VMs are
// locked with fcntl on config.json so two processes cannot run the same VM,
// and a PID lock distinguishes a missing VM from a running one
// (vmstoragelocal.go, vmstorageoci.go, vmdirectory.go, filelock.go,
// pidlock.go).
//
// Pruning and garbage collection: "prune" reclaims space from cached images
// ("--entries caches") or stale VMs by age ("--older-than") or a size budget
// ("--space-budget"), and "--gc" sweeps abandoned tmp entries. Access dates
// are tracked per entry so least-recently-used images are pruned first
// (commands_prune.go, prunable.go, url_accessdate.go, url_prunable.go).
//
// Import and export: "export" writes a VM to a .tvm archive and "import"
// restores one, preserving disk, NVRAM and config (commands_export.go,
// commands_import.go, vmdirectory_archive.go).
//
// OCI distribution: "pull" and "push" move VM images to and from any OCI
// registry. Pushes upload disks as chunked layers ("--chunk-size",
// "--concurrency", custom "--label"s, "--populate-cache") in v1 or v2 disk
// layouts; pulls resume partial layer downloads. "--insecure" allows plain
// HTTP. "fqn" prints the fully-qualified resolved image name
// (commands_pull.go, commands_push.go, commands_fqn.go, oci/, registry/).
//
// Multi-registry and multi-format images: references resolve through named
// registry profiles — "--registry <profile>" selects one explicitly,
// fully-qualified references (host/namespace:tag) work verbatim as always,
// and bare names ("macos-sequoia-weave:latest") resolve against the default
// profile. The image format is detected per manifest, never per registry:
// tart images (cirruslabs media types, LZ4 disk.v2 chunks) and the cua/lume
// formats (sharded raw splits as published by ghcr.io/trycua, the
// chunked-gzip format of newer lume, and the lz4 legacy format) all pull
// into ordinary weave VMs, with lume metadata translated into a weave
// config.json on the way (oci/format.go, oci/codec_*.go,
// vmdirectory_lume.go, registry/resolver.go,
// docs/registries-and-image-formats.md).
//
// Disk-space guard: every download — an OCI pull of any format or an IPSW
// fetch — checks the host volume through the framework
// (NSURLVolumeAvailableCapacityForImportantUsageKey) before the first byte
// transfers, reclaiming prunable cache entries and refusing the download
// with an actionable error when space is still insufficient
// (vmstorage/diskspace.go).
//
// Registry authentication: credentials are resolved from the macOS Keychain,
// ~/.docker/config.json, environment variables or an interactive stdin
// prompt. "login" stores credentials for a host (validated unless
// "--no-validate", password via "--password-stdin") and "logout" removes
// them (commands_login.go, commands_logout.go, credentials_keychain.go,
// credentials_dockerconfig.go, credentials_environment.go,
// credentials_stdin.go, oci_authentication.go, oci_authenticationkeeper.go,
// oci_wwwauthenticate.go).
//
// Networking: a VM's networking is a per-NIC topology — an ordered list of
// NICs, each with its own mode, MAC address and isolation properties — rather
// than the single shared MAC of the original tart port. Three surfaces select
// it, in precedence order:
//
//   - "--net-profile <name>" expands a named scenario onto the per-NIC model:
//     nat (Apple shared/NAT; internet + host + peers), internet-only (softnet
//     filtering: internet but no host or peers), isolated (softnet block-all,
//     air-gapped), vm-lab (a vmnet host-mode segment where VMs interconnect but
//     reach neither host nor internet), and bridged (a peer on the host LAN).
//   - "--net-device <spec>" (repeatable) composes NICs directly, e.g.
//     "nat", "bridged:en0", "softnet,block=10.0.0.0/8,expose=2222:22",
//     "vmnet,mode=host,subnet=192.168.66.1,mask=255.255.255.0,nonat"; each may
//     carry ",mac=<addr>" and ",primary". Multiple --net-device flags give one
//     VM several NICs with distinct modes and MACs.
//   - The legacy "--net-bridged <interface>", "--net-softnet" (with
//     "--net-softnet-allow", "--net-softnet-block", "--net-softnet-expose") and
//     "--net-host" flags remain as aliases onto the same model.
//
// Two isolation engines back the modes. Softnet (the userspace packet-filter
// helper) is the entitlement-free default and powers internet-only/isolated.
// The vmnet-direct engine builds a custom vmnet network (host/shared/bridged,
// optional subnet) and attaches it via VZVmnetNetworkDeviceAttachment; it and
// bridged mode require the com.apple.vm.networking entitlement or root. Custom
// vmnet networks are process-scoped, so cross-process lab segments rely on the
// system-wide vmnet host-mode subnet.
//
// Every profile preserves VM management regardless of guest isolation, because
// the framebuffer VNC server (_VZVNCServer) and the vsock guest agent are both
// network-independent; SSH over the guest IP is additionally available whenever
// the topology has a host-reachable NIC. "--net-profile" is also accepted by
// "create" to persist a default topology into the VM's config.json. The
// resolved NICs are stored under "nics"; configs without it synthesise a single
// primary NAT NIC from the legacy "macAddress" (network_network.go,
// network_shared.go, network_bridged.go, network_softnet.go, network_vmnet.go,
// profile.go, netspec.go, vmconfig/nic.go).
//
// IP discovery: "ip" resolves a running VM's address, waiting up to
// "--wait" seconds, via DHCP leases, the ARP cache or the guest agent
// ("--resolver dhcp|arp|agent") (commands_ip.go, macaddressresolver_*.go).
//
// Guest command execution: "exec" runs a command inside the guest through
// the gRPC guest agent over a vsock control socket, with interactive
// ("-i") and TTY ("-t") modes for shells; remote exit codes pass through to
// the caller's exit code (commands_exec.go, controlsocket.go).
//
// Directory sharing and Rosetta: "--dir name:path[,ro][,tag=]" shares host
// directories into the guest through Virtio file system devices (single or
// multiple named shares, local paths or downloaded http(s) archives), and
// "--rosetta <tag>" exposes a Rosetta share so x86-64 Linux binaries run on
// Apple silicon (commands_run.go).
//
// Storage and serial devices: extra disks attach with repeatable "--disk"
// (disk images, raw block devices, NBD URLs, remote VM names) carrying
// ":ro", ":sync=" and ":caching=" options, and "--root-disk-opts" tunes the
// root disk the same way; "--serial"/"--serial-path" attach a serial console
// (commands_run.go, serial.go, diskutil.go).
//
// Display, input and devices: VNC is available through macOS Screen Sharing
// ("--vnc") or the private _VZVNCServer ("--vnc-experimental"), the latter
// printing a vnc:// URL with a generated passphrase; input devices can be
// toggled with "--no-trackpad", "--no-pointer" and "--no-keyboard",
// "--capture-system-keys" forwards system shortcuts, "--no-audio" disables
// audio, and "--nested" enables nested virtualization where supported
// (vnc_vnc.go, vnc_fullfledged.go, vnc_screensharing.go,
// passphrase_generator.go, vm.go).
//
// View-only screen ("run --show-screen"): runs the VM headless and serves a
// continuously-captured MJPEG stream of its screen over a local HTTP server,
// opened in a browser — handy for peeking at an ephemeral/headless VM without
// the input-forwarding (and so interference) of a native VM window. The
// viewer (ScreenServer in screenviewer.go) is reusable: in operational mode a
// dedicated VNC client streams it (StreamVNCToViewer), while "setup
// --show-screen" feeds the same viewer from the automation's own captures
// because _VZVNCServer permits only one VNC client at a time.
//
// Disk formats: VMs use raw disks by default or the sparse ASIF format
// ("--disk-format asif", macOS 26+); the format is recorded per VM and
// validated for support at run time (diskimageformat.go).
//
// Terminal UI: a braille "Orin" wordmark banner (crowned O, apple-leaf i) is
// shown on no-args and help invocations, and a braille pinwheel spinner with
// an elapsed-time counter animates indeterminate operations such as the
// restore-image lookup (banner.go, spinner.go, terminal_ui.go). Both respect
// NO_COLOR and degrade to plain output when stdout is not a terminal.
//
// Observability: every command runs under an OpenTelemetry span (exported
// over OTLP when a collector is configured), and command lifecycle and
// errors are written both to the span and the file logs (otel.go, main.go).
//
// Shell and platform support: shell completion scripts are generated for the
// installed commands, and platform/OS/architecture detection gates
// macOS-only and Apple-silicon-only behaviour (shellcompletions.go,
// platform_os.go, platform_architecture.go, platform_platform.go,
// deviceinfo.go).
//
// # Commands
//
//	create   Create a VM from an IPSW (macOS) or empty disk (Linux)
//	clone    Clone a local VM or pulled OCI image into a new local VM
//	run      Boot a VM (graphics, headless, VNC, devices, sharing, clipboard; --show-screen for a view-only viewer)
//	set      Modify a stopped VM's configuration
//	get      Show a VM's configuration (text or JSON)
//	list     List local and/or OCI VMs (text or JSON)
//	login    Store credentials for an OCI registry
//	logout   Remove credentials for an OCI registry
//	ip       Resolve a running VM's IP address (dhcp, arp or agent)
//	exec     Execute a command inside a VM via the guest agent
//	ssh      SSH into a VM, or run a one-shot remote command
//	pull     Pull a VM image (tart or cua/lume format) from an OCI registry or profile
//	push     Push a VM image to an OCI registry
//	import   Import a VM from a .tvm archive
//	export   Export a VM to a .tvm archive
//	prune    Reclaim space from caches or stale VMs
//	rename   Rename a local VM
//	stop     Stop a running VM gracefully, then forcefully
//	delete   Delete one or more VMs
//	fqn      Print the fully-qualified OCI name of a VM
//	suspend  Suspend a running VM to disk (macOS 14+)
//	ipsw     Print the latest supported macOS restore image URL
//	images   List the tags of a remote OCI repository or registry profile
//	logs     View the weave log files (info, error or all; tail/follow)
//	config   Manage settings: storage locations, cache dir, registry profiles, network
//	serve    Start the HTTP API server, or the MCP stdio server (--mcp)
//	setup    Automate the macOS Setup Assistant (preset or agent mode; --show-screen for a view-only viewer)
//
// The binary must be code-signed with the com.apple.security.virtualization
// entitlement before any VM-touching command will work:
//
//	go build -o weave ./example/weave/
//	codesign --entitlements example/weave/entitlements.plist --force -s - weave
package main
