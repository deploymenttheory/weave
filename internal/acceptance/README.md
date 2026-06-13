# weave acceptance suite

End-to-end acceptance tests that drive the **real, code-signed `weave`
binary** through themed suites against an **isolated `$WEAVE_HOME` and
settings directory**, so a run never touches your real `~/.weave` VMs.

## Running

```sh
# From the repo root. Builds + codesigns weave, then runs the default suites.
go run ./example/weave/acceptance

# Pick suites
go run ./example/weave/acceptance -suites cli,config,lifecycle

# Include network-dependent suites (ipsw → Apple's servers)
go run ./example/weave/acceptance -net

# Include the guest suite against a provisioned, running VM (SSH enabled)
go run ./example/weave/acceptance -guest my-vm -guest-user admin -guest-password admin

# Full macOS provisioning: a headless pass, then a --show-screen pass, each
# creating and unattended-setting-up its own fresh VM (needs a cached IPSW)
go run ./example/weave/acceptance -suites "" -provision

# Keep the isolated home for inspection; print logs for passing cases too
go run ./example/weave/acceptance -keep -v
```

The runner builds `weave` and signs it with
`example/weave/entitlements.plist` (the Virtualization entitlement) into a
temporary directory, then runs every selected suite and prints a pass /
fail / skip summary. The process exits non-zero if any case fails.

## Model: children → parent

- A **Case** is one leaf test (`harness.go`, `Case`).
- A **Suite** is a themed collection of cases with optional setup/teardown
  (`Suite`). Cases run in order and may build on VMs created by earlier
  cases in the same suite.
- **main.go** is the parent runner: it owns the binary, the isolated
  environment, the suite registry, and the summary.

## Suites

| Suite       | Touches      | What it verifies |
|-------------|--------------|------------------|
| `cli`       | nothing      | version, unknown-subcommand, and the exit-code contract (missing VM → 2, usage error → 1) |
| `config`    | settings     | `config get/storage/registry`, YAML round-trip, `WEAVE_HOME` precedence |
| `logs`      | log files    | file logger + `logs info/error/all --lines` |
| `lifecycle` | Linux VM     | create → list/get (JSON) → set → clone → export/import → delete |
| `serve`     | HTTP server  | `/weave/host/status`, `/weave/vms`, 404, config-location CRUD, logs |
| `mcp`       | MCP stdio    | initialize handshake + `tools/list` advertises the `weave_*` tools |
| `vnc`       | Linux VM     | `run --vnc-experimental` serves a framebuffer (full RFB handshake + capture, see `rfb.go`) |
| `ipsw`      | network      | `ipsw` prints a valid restore-image URL (gated by `-net`) |
| `guest`     | running VM   | `ssh`, `exec`, clipboard against a provisioned guest (gated by `-guest`) |
| `netbehavior-linux` | Linux VMs | **behavioural** network proof: boots a real Linux guest under each profile and probes reachability *from inside the guest* — see below |
| `netbehavior-macos` | macOS VM | the same battery inside a provisioned macOS guest, re-run under each profile |
| `provision` / `provision-viewer` | macOS VM | IPSW pre-flight → `create --from-ipsw` → unattended Setup Assistant (`setup --unattended`) → guest checks; the two suites run sequentially, headless then `--show-screen`, each on its own fresh VM (gated by `-provision`) |

### Behavioural network suites (`netbehavior-*`)

The `network` suite proves the *config* round-trip (the persisted NIC topology
survives load→save). The `netbehavior-*` suites prove the *behaviour*: they
boot a real guest under each network profile and run an in-guest reachability
battery (`fixtures/network/netprobe.sh`, delivered over SSH — or the vsock
guest agent when the image ships one), then assert the observed matrix against
each profile's contract:

| Profile | internet | dns | host |
|---------|----------|-----|------|
| `nat` | ✓ | ✓ | ✓ (gateway) |
| `internet-only` | ✓ | ✓ | — |
| `isolated` | ✗ | ✗ | — |
| `vm-lab` | ✗ | — | ✓ (subnet .1) |
| `bridged` | ✓ | — | — |

Each scenario boots **one** VM and asserts the reachability that guest sees.
Notes: the softnet profiles assert egress posture only — under softnet the
guest's default gateway is softnet's own userspace NAT, not the macOS host, so
a gateway ping is not a meaningful host signal there. The `vm-lab` "host" probe
pings the subnet's `.1` (the host's address in vmnet host mode, which provides
no default gateway). VM-to-VM interconnect is intentionally not asserted: vmnet
networks are process-scoped (two separate `weave run` invocations cannot share
one), and guest↔guest / guest↔host isolation under nat is expected behaviour.

Privilege:

- `nat` — no privilege.
- `internet-only`, `isolated` — softnet, which shells out to `sudo`; runs as **root**. The guest stays reachable over `weave ssh` (softnet's userspace NAT resolves and forwards host→guest; its `--block` filters the guest's *egress*, not host→guest ingress).
- `vm-lab` — vmnet host mode; the vmnet path honours **root** in lieu of the entitlement.
- `bridged` — `VZBridgedNetworkDeviceAttachment` is gated by the `com.apple.vm.networking` entitlement and **root does *not* bypass it**. It only runs with a properly entitled (notarised, Path B) binary, opted in via `WEAVE_ACC_BRIDGED=1`; otherwise it skips.

Each gated case **skips** (it does not fail) when its privilege/entitlement is absent. A boot failure surfaces the `weave run` log in the failure message.

```sh
# Prerequisite: cache a bootable Linux guest once (any tart/lume Linux image).
weave pull ghcr.io/cirruslabs/ubuntu:latest

# nat only (no privilege):
go run ./example/weave/acceptance -suites netbehavior-linux

# full matrix (softnet + vmnet; bridged skipped unless WEAVE_ACC_BRIDGED=1):
sudo go run ./example/weave/acceptance -suites netbehavior-linux

# macOS guest (reuses a VM provisioned by -provision; long install otherwise):
sudo go run ./example/weave/acceptance -suites netbehavior-macos
```

### Enabling the `bridged` scenario

`VZBridgedNetworkDeviceAttachment` requires the `com.apple.vm.networking`
entitlement. **Root does not bypass it** — AMFI kills any binary claiming it
without an Apple-authorized provisioning profile. The bridged case skips by
default and is gated by `WEAVE_ACC_BRIDGED=1`.

**One-time Apple setup (per Team ID):**

1. **Request the entitlement** — go to developer.apple.com → Account →
   Contact → submit a Virtualization entitlement request for
   `com.apple.vm.networking` for your Team ID. Apple reviews and grants it.

2. **Register a Mac App ID** — Certificates, Identifiers & Profiles →
   Identifiers → `+` → Mac App. Bundle ID:
   `com.deploymenttheory.weave` (or your own). Enable the
   `com.apple.vm.networking` capability once Apple has granted it.

3. **Create a Mac Provisioning Profile** — Profiles → `+` → Mac App
   Distribution (Developer ID). Associate it with the App ID above. Download
   the `.provisionprofile` file.

4. **Run the bridged case:**

```sh
export WEAVE_SIGNING_IDENTITY="Developer ID Application: D Watkins (5GM6DW5337)"
export WEAVE_PROVISIONING_PROFILE=example/weave/weave_cli.provisionprofile
WEAVE_ACC_BRIDGED=1 sudo go run ./example/weave/acceptance -suites netbehavior-linux
```

The harness (`main.go`) detects `WEAVE_ACC_BRIDGED=1` and calls
`buildAndSignEntitled` which signs with `entitlements-bridged.plist`
(adds `com.apple.vm.networking`) and embeds the provisioning profile. It
fails fast with a clear message if the env vars are missing or mismatched.

Image / credentials are overridable: `WEAVE_ACC_LINUX_IMAGE` (default
`ghcr.io/cirruslabs/ubuntu:latest`), `WEAVE_ACC_LINUX_USER`/`_PASSWORD`
(default `admin`/`admin`, used only for the SSH fallback), and
`WEAVE_ACC_MACOS_VM` (default `acc-macos`). The Linux suite shares the real
`~/.weave` OCI cache into the isolated home so the multi-GB image is not
re-pulled; cloned VMs still live in (and are torn down from) the isolated home.

The `lifecycle` and `vnc` suites use **empty Linux VMs**, which create in
milliseconds and boot far enough to exercise their feature without a
multi-minute macOS install. `rfb.go` is a standalone minimal RFB 3.8 client
(it must advertise the DesktopSize pseudo-encoding, which `_VZVNCServer`
requires) so the suite exercises the VNC **server** end to end rather than
trusting weave's own client.

## Flags

| Flag | Default | Meaning |
|------|---------|---------|
| `-suites`        | `cli,config,logs,lifecycle,serve,mcp,vnc` | comma-separated suites to run |
| `-net`           | off  | include network-dependent suites |
| `-provision`     | off  | include the two macOS provisioning suites (headless, then `--show-screen`) |
| `-guest`         | ""   | provisioned running VM for the guest suite |
| `-guest-user`    | `admin` | guest SSH user |
| `-guest-password`| `admin` | guest SSH password |
| `-keep`          | off  | keep the isolated home (and any VMs) after the run |
| `-home`          | temp | isolated home base directory |
| `-ipsw`          | autodetect | cached restore image path for macOS tests |
| `-v`             | off  | print case logs even for passing cases |

## Debugging a failing provision run

When the unattended automation misbehaves against a particular screen, use
the gated VNC diagnostic in the main package
(`example/weave/vnc_diagnostic_test.go`). It drives the production
automation engine against a live VM, saving a PNG frame dump plus the OCR
text (with coordinates) after every step:

```sh
WEAVE_VNC_ENDPOINT=$(cat <vmdir>/.vnc-endpoint) \
WEAVE_VNC_PROBE_COMMANDS="<wait 'Language', timeout=180> ; <enter> ; <delay 3>" \
go test ./example/weave/ -run TestVNCScreenDiagnostic -v -timeout 600s
```

A stopped mid-setup VM is replayable: Setup Assistant restarts at the
Language screen on the next `run`, so failed provisioning VMs (kept with
`-keep`) make good diagnostic targets.
