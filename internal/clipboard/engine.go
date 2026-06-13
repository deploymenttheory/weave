// Package clipboard is the host side of weave's enterprise clipboard: a
// policy-driven engine that mirrors the host (macOS) and guest clipboards over
// the weave guest agent. It enforces directionality, per-format allow-lists,
// independent file transfer, a per-item size cap, and a bandwidth limit
// expressed as a percentage of declared session bandwidth — preserving rich
// text and images, not just plain text.
//
// The engine reads/writes the host NSPasteboard via the shared macpb package on
// the main thread, and drives the guest's clipboard module through the agent
// client. It supersedes the original lume-style text-only SSH watcher.
//go:build darwin

package clipboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/deploymenttheory/weave/internal/clipboard/macpb"
	"github.com/deploymenttheory/weave/internal/clipboard/wire"
	"github.com/deploymenttheory/weave/internal/clipboardpolicy"
	guestclient "github.com/deploymenttheory/weave/internal/guestagent/client"
	"github.com/deploymenttheory/weave/internal/guestagent/proto"
	"github.com/deploymenttheory/weave/internal/logging"
	"github.com/deploymenttheory/weave/internal/macaddress"
	weavessh "github.com/deploymenttheory/weave/internal/ssh"
	"github.com/deploymenttheory/weave/internal/vmdirectory"

	dispatch "github.com/deploymenttheory/go-bindings-macosplatform/internal/objc"
)

const (
	pollInterval      = 1 * time.Second
	backoffInterval   = 5 * time.Second
	errorLogThreshold = 3
)

// Engine mirrors the clipboard between the host and one VM under a policy.
type Engine struct {
	policy             clipboardpolicy.Policy
	vmName             string
	vmDir              *vmdirectory.VMDirectory
	mac                macaddress.MACAddress
	user, password     string
	guestOS, guestArch string

	allowedSet  map[wire.Canonical]bool
	allowedList []wire.Canonical
	limiter     *limiter
	stageDir    string // host staging dir for files applied to the host pasteboard

	// Loop-prevention state.
	lastHostChangeCount  uint64
	lastGuestChangeCount uint64
	lastHostHash         string
	lastGuestHash        string

	// Agent connection, invalidated on error or IP change.
	client   *guestclient.Client
	cachedIP string

	// Error suppression.
	consecutiveFailures int
	lastLoggedError     string
	disabled            bool // permanently off (e.g. no embedded agent for the guest)
}

// NewEngine builds a clipboard engine for one VM. guestOS/guestArch select the
// agent binary to deploy (e.g. "darwin"/"arm64", "linux"/"amd64").
func NewEngine(policy clipboardpolicy.Policy, vmName string, vmDir *vmdirectory.VMDirectory, mac macaddress.MACAddress, user, password, guestOS, guestArch string) *Engine {
	return &Engine{
		policy:    policy,
		vmName:    vmName,
		vmDir:     vmDir,
		mac:       mac,
		user:      user,
		password:  password,
		guestOS:   guestOS,
		guestArch: guestArch,
	}
}

// Run mirrors the clipboard until ctx is cancelled. Call as a goroutine after
// the VM starts. It returns immediately when the policy is inactive.
func (e *Engine) Run(ctx context.Context) {
	if !e.policy.Active() {
		return
	}

	e.allowedSet = e.policy.AllowedCanonical()
	e.allowedList = sortedCanonicals(e.allowedSet)
	e.limiter = newLimiter(e.policy.BytesPerSec())
	if dir, err := os.MkdirTemp("", "weave-clip-host-"); err == nil {
		e.stageDir = dir
	}
	e.initHostState()

	fmt.Printf("Clipboard policy engine started (direction=%s, files=%v, formats=%v)\n",
		e.policy.Direction, e.policy.FileTransfer, e.allowedList)

	for {
		interval := pollInterval
		if e.consecutiveFailures >= errorLogThreshold {
			interval = backoffInterval
		}
		select {
		case <-ctx.Done():
			if e.client != nil {
				_ = e.client.Close()
			}
			return
		case <-time.After(interval):
		}
		e.sync(ctx)
	}
}

// initHostState seeds the change-count and hash state from the current host
// clipboard so nothing is synced at startup.
func (e *Engine) initHostState() {
	var payload wire.Payload
	dispatch.RunOnMainThread(func() {
		e.lastHostChangeCount = macpb.ChangeCount()
		payload = macpb.Read(e.allowedSet, e.policy.MaxBytes())
	})
	hash := hashPayload(payload)
	e.lastHostHash = hash
	e.lastGuestHash = hash // assume the guest starts matching the host
}

func (e *Engine) sync(ctx context.Context) {
	client := e.agent(ctx)
	if client == nil {
		return
	}

	didHostToGuest := false

	// Host → guest.
	if e.policy.AllowHostToGuest() {
		hostCC := hostChangeCount()
		if hostCC != e.lastHostChangeCount {
			e.lastHostChangeCount = hostCC
			payload := e.captureHost()
			hash := hashPayload(payload)
			if !payload.Empty() && hash != e.lastHostHash && hash != e.lastGuestHash {
				e.lastHostHash = hash
				e.lastGuestHash = hash // assume the guest has it after the push
				if err := e.setGuest(ctx, payload); err != nil {
					e.handleError("sync clipboard to guest", err)
				} else {
					e.resetFailure()
					didHostToGuest = true
				}
			}
		}
	}

	// Guest → host (skip if we just pushed: the guest's change count would race).
	if !didHostToGuest && e.policy.AllowGuestToHost() {
		guestCC, err := e.statGuest()
		if err != nil {
			e.handleError("stat guest clipboard", err)
			return
		}
		if guestCC == e.lastGuestChangeCount {
			e.resetFailure()
			return
		}
		e.lastGuestChangeCount = guestCC

		payload, err := e.getGuest(ctx)
		if err != nil {
			e.handleError("sync clipboard from guest", err)
			return
		}
		hash := hashPayload(payload)
		if !payload.Empty() && hash != e.lastGuestHash && hash != e.lastHostHash {
			e.lastGuestHash = hash
			e.applyHost(payload)
			e.lastHostHash = hash
		}
		e.resetFailure()
	}
}

// ── Host pasteboard (main thread) ────────────────────────────────────────────

func hostChangeCount() uint64 {
	var cc uint64
	dispatch.RunOnMainThread(func() { cc = macpb.ChangeCount() })
	return cc
}

func (e *Engine) captureHost() wire.Payload {
	var payload wire.Payload
	dispatch.RunOnMainThread(func() {
		payload = macpb.Read(e.allowedSet, e.policy.MaxBytes())
	})
	return payload
}

func (e *Engine) applyHost(payload wire.Payload) {
	dispatch.RunOnMainThread(func() {
		_ = macpb.Write(payload, e.stageDir)
		e.lastHostChangeCount = macpb.ChangeCount()
	})
}

// ── Guest agent protocol ─────────────────────────────────────────────────────

func (e *Engine) statGuest() (uint64, error) {
	c := e.client
	c.Lock()
	defer c.Unlock()

	if err := proto.WriteRequest(c.Writer(), proto.Request{Module: wire.Module, Op: wire.OpStat}); err != nil {
		return 0, e.dropClient(err)
	}
	meta, err := e.readMeta(c)
	if err != nil {
		return 0, err
	}
	return meta.ChangeCount, nil
}

func (e *Engine) getGuest(ctx context.Context) (wire.Payload, error) {
	c := e.client
	c.Lock()
	defer c.Unlock()

	raw, _ := json.Marshal(wire.Meta{Allowed: e.allowedList})
	if err := proto.WriteRequest(c.Writer(), proto.Request{Module: wire.Module, Op: wire.OpGet, Meta: raw}); err != nil {
		return wire.Payload{}, e.dropClient(err)
	}
	meta, err := e.readMeta(c)
	if err != nil {
		return wire.Payload{}, err
	}
	payload, err := wire.ReadBody(c.Reader(), meta, e.gate(ctx))
	if err != nil {
		return wire.Payload{}, e.dropClient(err)
	}
	return payload, nil
}

func (e *Engine) setGuest(ctx context.Context, payload wire.Payload) error {
	c := e.client
	c.Lock()
	defer c.Unlock()

	raw, _ := json.Marshal(wire.MetaFor(payload))
	if err := proto.WriteRequest(c.Writer(), proto.Request{Module: wire.Module, Op: wire.OpSet, Meta: raw}); err != nil {
		return e.dropClient(err)
	}
	if err := wire.WriteBody(c.Writer(), payload, e.gate(ctx)); err != nil {
		return e.dropClient(err)
	}
	if _, err := e.readMeta(c); err != nil {
		return err
	}
	return nil
}

// readMeta reads a response envelope and decodes its clipboard meta, mapping a
// transport error to a client drop and a module error to a plain error.
func (e *Engine) readMeta(c *guestclient.Client) (wire.Meta, error) {
	resp, err := proto.ReadResponse(c.Reader())
	if err != nil {
		return wire.Meta{}, e.dropClient(err)
	}
	if resp.Err != "" {
		return wire.Meta{}, fmt.Errorf("guest clipboard: %s", resp.Err)
	}
	var meta wire.Meta
	if len(resp.Meta) > 0 {
		if err := json.Unmarshal(resp.Meta, &meta); err != nil {
			return wire.Meta{}, err
		}
	}
	return meta, nil
}

// dropClient invalidates the agent connection so the next cycle redials, and
// returns the triggering error.
func (e *Engine) dropClient(err error) error {
	if e.client != nil {
		_ = e.client.Close()
		e.client = nil
		e.cachedIP = ""
	}
	return err
}

func (e *Engine) gate(ctx context.Context) wire.Gate {
	if e.limiter == nil {
		return nil
	}
	return func(n int) error { return e.limiter.waitN(ctx, n) }
}

// ── Agent connection ─────────────────────────────────────────────────────────

// agent returns a connected guest agent client for the VM's current IP, or nil
// when the VM is not running, has no resolvable IP yet, or the agent cannot be
// deployed (silent skip — the VM may still be booting).
func (e *Engine) agent(ctx context.Context) *guestclient.Client {
	if e.disabled {
		return nil
	}
	if running, err := e.vmDir.Running(); err != nil || !running {
		return nil
	}

	ip, found, err := macaddress.ResolveIP(ctx, e.mac, macaddress.IPResolutionStrategyDHCP, 0, e.vmDir.ControlSocketURL())
	if err != nil || !found {
		return nil
	}

	if e.client != nil && e.cachedIP == ip.String() {
		return e.client
	}
	if e.client != nil {
		_ = e.client.Close()
		e.client = nil
	}

	ssh := weavessh.NewSSHClient(ip.String(), 22, e.user, e.password)
	client, err := guestclient.Dial(ctx, ssh, guestclient.Options{GOOS: e.guestOS, GOARCH: e.guestArch})
	if err != nil {
		if strings.Contains(err.Error(), "no embedded agent") {
			e.disabled = true
			logging.DefaultLogger().AppendNewLine("Clipboard disabled: " + err.Error())
			return nil
		}
		e.handleError("connect guest agent", err)
		return nil
	}
	e.client = client
	e.cachedIP = ip.String()
	e.resetFailure()
	return client
}

// ── Error suppression ────────────────────────────────────────────────────────

func (e *Engine) handleError(message string, err error) {
	e.consecutiveFailures++
	desc := err.Error()
	if desc != e.lastLoggedError {
		logging.DefaultLogger().AppendNewLine(fmt.Sprintf("Failed to %s: %s", message, desc))
		e.lastLoggedError = desc
	} else if e.consecutiveFailures == errorLogThreshold {
		logging.DefaultLogger().AppendNewLine("Clipboard sync errors repeating, suppressing further logs until resolved")
	}
}

func (e *Engine) resetFailure() {
	if e.consecutiveFailures >= errorLogThreshold {
		logging.DefaultLogger().AppendNewLine("Clipboard sync recovered")
	}
	e.consecutiveFailures = 0
	e.lastLoggedError = ""
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func hashPayload(p wire.Payload) string {
	h := sha256.New()
	for _, item := range p.Items {
		h.Write([]byte(item.Format))
		h.Write([]byte{0})
		h.Write(item.Data)
		h.Write([]byte{0})
	}
	for _, file := range p.Files {
		h.Write([]byte(file.Name))
		h.Write([]byte{0})
		h.Write(file.Data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sortedCanonicals(set map[wire.Canonical]bool) []wire.Canonical {
	list := make([]wire.Canonical, 0, len(set))
	for c := range set {
		list = append(list, c)
	}
	slices.Sort(list)
	return list
}
