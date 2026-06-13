# Registries and image formats

weave pulls and pushes VM images through standard OCI registries, and it
understands more than one on-the-wire image encoding. This document explains
how references resolve, which formats weave can pull, what happens to an
image on the way to disk, and how downloads are guarded. The short version:

- **Any OCI registry works** — ghcr.io, GitLab/Harbor/Artifactory, a
  self-hosted `registry:2` — with per-host credentials via `weave login`.
- **Both tart and cua/lume images pull into ordinary weave VMs.** The format
  is detected from each image's manifest; you never configure it.
- **Registry profiles** give registries short names and make bare image
  names work: `weave pull --registry cua macos-sequoia-vanilla:latest`.
- **Every download is checked against free disk space before the first byte
  transfers.**

## 1. Two independent concepts

weave deliberately separates two ideas that often get conflated:

| Concept | Question it answers | Where it's decided |
|---|---|---|
| **Registry (transport)** | Where do manifests and blobs live, and how does weave authenticate? | per registry profile / reference |
| **Image format (encoding)** | How is a VM packed into manifest layers? | per image, detected from the manifest |

A single registry can host images in several formats —
`ghcr.io/yourorg` could serve weave-pushed (tart-format) images next to
re-published cua images — so format is never a property of a registry.
Format only ever becomes a *choice* when pushing (weave pushes tart format).

## 2. Registries

### 2.1 Fully-qualified references (zero configuration)

Any `host/namespace/name[:tag|@digest]` reference works directly, exactly as
it always has:

```sh
weave pull ghcr.io/cirruslabs/macos-sonoma-vanilla:latest
weave pull ghcr.io/trycua/macos-sequoia-vanilla:latest
weave images ghcr.io/trycua/macos-sequoia-cua
```

### 2.2 Registry profiles

Profiles give registries short names, defaults, and per-registry settings.
They live in the weave settings file
(`$XDG_CONFIG_HOME/weave/config.yaml`) and are managed by the CLI:

```sh
weave config registry add cua   --organization trycua
weave config registry add weave --organization deploymenttheory --default
weave config registry add corp  --host registry.internal.example:5000 \
                                --organization vm-images --insecure
weave config registry list
weave config registry default weave
weave config registry remove corp
```

which produces:

```yaml
registries:
  - name: cua
    host: ghcr.io
    organization: trycua
  - name: weave
    host: ghcr.io
    organization: deploymenttheory
    default: true
  - name: corp
    host: registry.internal.example:5000
    organization: vm-images
    insecure: true
```

### 2.3 How a reference resolves

`pull`, `clone`, `push` and `images` all resolve references the same way, in
this order:

1. **`--registry <profile>`** — the reference is `name[:tag]` relative to
   that profile's `host/organization`:
   `weave pull --registry cua macos-sequoia-vanilla:latest`
   → `ghcr.io/trycua/macos-sequoia-vanilla:latest`.
2. **Fully-qualified reference** — used verbatim. If a profile matches the
   host, its `insecure` setting is inherited (a CLI `--insecure` always
   wins).
3. **Bare name** — resolved against the *default* profile:
   `weave pull macos-sequoia-weave:latest`
   → `ghcr.io/deploymenttheory/macos-sequoia-weave:latest`.
   Without a default profile this is an error that tells you how to add one.

Note that profile names never appear *inside* a reference —
`cua/foo:latest` parses as host `cua` — always use `--registry` to select a
profile explicitly.

### 2.4 Authentication and private registries

Credentials are resolved per host, in order: macOS Keychain (stored by
`weave login <host>`), `~/.docker/config.json`, environment variables, then
an interactive prompt. Nothing about this changes with profiles — a private
registry is just a profile (or fully-qualified reference) plus a
`weave login registry.internal.example:5000` beforehand. Registries on plain
HTTP or self-signed TLS need `--insecure` on the command or `insecure: true`
on the profile.

## 3. Image formats

weave detects the encoding of every image from its manifest's layer media
types and dispatches to a matching codec:

| Format | Published by | How weave recognises it |
|---|---|---|
| **tart** | tart and weave (`weave push`) | `application/vnd.cirruslabs.tart.*` media types; LZ4-compressed `disk.v2` chunks |
| **lume sharded** | the public `ghcr.io/trycua` images | raw 500 MiB `disk.img` splits whose media type embeds the order (`application/vnd.oci.image.layer.v1.tar;part.number=N;part.total=M`); a `config.json` layer; an `nvram.bin` layer |
| **lume chunked** | newer lume releases (`lume push`) | `application/vnd.trycua.lume.*` media types; gzip chunks placed at explicit byte offsets |
| **lume lz4** (legacy, best effort) | old lume releases | sequential `application/octet-stream+lz4` parts |

Anything else fails with an error that lists the manifest's media types, so
unrecognised variants are easy to report.

Wire details that matter in practice:

- Sharded images are **uncompressed** — the manifest's layer sizes are the
  real transfer size (≈41 GB for `macos-sequoia-vanilla`, ≈82 GB for
  `macos-sequoia-cua`). weave prints the total before starting.
- Disk reassembly is **sparse**: all-zero runs are skipped, so the resulting
  `disk.img` occupies far less space than its logical size, and every part's
  sha256 digest is verified while streaming.
- The dubious bits of the ecosystem are handled: the `.tar` label on sharded
  parts is a misnomer (content is raw disk bytes), and some repos label
  parts `+lzfse` although they are not LZFSE — weave treats both as raw,
  keyed on the `part.number` parameter, matching what the images actually
  contain.

### 3.1 What pulling a lume image produces

A pulled image — regardless of source format — is an **ordinary weave VM**:
`config.json` (weave schema), `disk.img`, `nvram.bin`. For lume images,
weave translates the image's metadata at pull time:

| weave config field | Taken from | Default if absent |
|---|---|---|
| `os` | image config (`macOS` → `darwin`) | — (error) |
| `ecid` / `hardwareModel` | image's `machineIdentifier` / `hardwareModel` — the same Virtualization.framework payloads both ecosystems store, passed through and validated | — (error for macOS guests) |
| `cpuCount` / `memorySize` | image config or manifest annotations | 4 CPUs / 4 GiB |
| `macAddress` | image config (kept; `clone` regenerates on collision) | random |
| `display` | image config | 1024x768 |
| disk size | image config `diskSize` | sum of the parts |
| `arch` / `diskFormat` | — | `arm64` / `raw` |

lume's `networkMode` is ignored (weave configures networking per `run`), as
are extra `storage[]` items (a warning is printed; only the primary disk is
pulled).

After that, everything downstream is uniform: `run`, `clone`, `set`,
`suspend`, `delete`, pruning and `list` neither know nor care where the VM
came from. The guest account on public trycua images is `lume`/`lume`.

## 4. Pulling, caching and the disk-space guard

- **Cache**: pulled images land in `$WEAVE_HOME/cache/OCIs`, keyed by
  manifest digest. Re-pulling a tag that resolves to a cached digest is a
  no-op; `clone` from a cached image is an APFS copy-on-write (seconds).
- **Deduplication**: tart-format pulls reuse layers from previously cached
  images (`--deduplicate`); lume formats currently always fetch in full.
- **Progress**: every transfer with a known size — OCI pulls of any format,
  pushes, IPSW downloads, remote `--dir` archives — renders a percentage
  line that also reaches the file logs (`weave logs info -f`). The braille
  spinner is reserved for waits with no knowable total (e.g. the restore
  image lookup); see `terminal/spinner.go` for the rule.
- **Disk-space guard**: before the first byte of any download, weave asks
  the filesystem — through Foundation's volume-capacity API, which sees
  purgeable space that `statfs` cannot — whether the volume can hold the
  download. It reclaims least-recently-used cache entries first and
  otherwise refuses up front:

  ```
  not enough disk space for this download: 43.08 GB required, 2.14 GB available …
  (free up space or prune cached images: weave prune --entries caches)
  ```

  Set `WEAVE_NO_AUTO_PRUNE` to disable the automatic reclaim (the hard check
  still applies).

## 5. Cookbook

```sh
# Pull a cua image and run it with the native window
weave pull ghcr.io/trycua/macos-sequoia-vanilla:latest
weave clone ghcr.io/trycua/macos-sequoia-vanilla:latest sequoia
weave run sequoia
weave ssh --user lume --password lume sequoia

# Make your own org the default and use bare names
weave config registry add weave --organization deploymenttheory --default
weave push my-vm macos-sequoia-weave:latest
weave pull macos-sequoia-weave:latest

# Private registry
weave login registry.internal.example:5000
weave config registry add corp --host registry.internal.example:5000 \
    --organization vm-images --insecure
weave pull --registry corp base-image:1
```

## 6. For developers

- `registry/` — the transport interfaces (`ImageSource`, `ImageSink`,
  `TagLister`, `Client`) and the reference resolver. The generic OCI
  distribution client (`oci.Registry`) satisfies all of them; a new backend
  (e.g. a signed-URL object store) only needs to implement `ImageSource` to
  make pulls work.
- `oci/format.go` — format detection and the `Codec` interface;
  `oci/codec_*.go` — one codec per format. Codecs write `disk.img`/`nvram`
  and either write the weave `config.json` themselves (tart) or return an
  `oci.VMDescription` that `vmdirectory/vmdirectory_lume.go` translates
  through the canonical `vmconfig.VMConfig` type.
- `vmstorage/diskspace.go` — `EnsureDiskSpace`, the pre-download guard.
- `oci/testdata/` — manifests and a config blob captured live from ghcr.
  These fixtures are **normative**: when lume's source and the published
  images disagree (they have), weave follows the registry. The unit tests in
  `oci/format_test.go` and `oci/codec_lume_test.go` run entirely against
  them, no network needed.

### Diagnosing an unrecognised image

When a pull fails with "unsupported VM image format", inspect the manifest
directly and compare it with the table in §3:

```sh
TOKEN=$(curl -s "https://ghcr.io/token?scope=repository:<org>/<repo>:pull" \
        | python3 -c "import sys,json;print(json.load(sys.stdin)['token'])")
curl -s -H "Authorization: Bearer $TOKEN" \
     -H "Accept: application/vnd.oci.image.manifest.v1+json" \
     "https://ghcr.io/v2/<org>/<repo>/manifests/<tag>" | python3 -m json.tool
```

Check the config descriptor's media type, every distinct layer media type
(including `;parameter` suffixes), where annotations live (manifest vs
layer), and whether layer sizes are uniform (raw splits) or varied
(compressed chunks). A new variant means a new fixture in `oci/testdata/`
and either an extension to an existing codec or a new one behind
`DetectImageFormat`.
