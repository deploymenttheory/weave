# weave-guestd binaries

This directory holds the cross-compiled guest agent binaries embedded into
weave by `guestagent/agentbin`. They are produced by `guestagent/build.sh`:

```
weave-guestd-darwin-arm64
weave-guestd-linux-arm64
weave-guestd-linux-amd64
```

This README is a committed placeholder so `//go:embed dist` always compiles even
before the binaries are built. When a target's binary is missing, the host
engine falls back to legacy text-only clipboard sync. The binaries themselves
are build artifacts and are not committed.
