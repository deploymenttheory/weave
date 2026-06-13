//go:build linux

package clipguest

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/deploymenttheory/weave/internal/clipboard/wire"
)

// linuxBackend drives the guest clipboard through the standard CLI tools:
// wl-clipboard (wl-copy/wl-paste) under Wayland, falling back to xclip under
// X11. These tools expose only a single representation per copy (each
// invocation replaces the selection), so Write sets the single richest
// representation available (files > image > html > rtf > plain). Read can pull
// every advertised target the policy allows.
type linuxBackend struct {
	stageDir    string
	listTargets func() ([]string, error)
	paste       func(target string) ([]byte, error)
	copy        func(target string, data []byte) error
}

func newBackend() (backend, error) {
	dir, err := os.MkdirTemp("", "weave-clipboard-")
	if err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}

	if os.Getenv("WAYLAND_DISPLAY") != "" && haveAll("wl-paste", "wl-copy") {
		return &linuxBackend{stageDir: dir, listTargets: wlListTargets, paste: wlPaste, copy: wlCopy}, nil
	}
	if haveAll("xclip") {
		return &linuxBackend{stageDir: dir, listTargets: xclipListTargets, paste: xclipPaste, copy: xclipCopy}, nil
	}
	return nil, fmt.Errorf("no clipboard tool found (need wl-clipboard or xclip)")
}

func (b *linuxBackend) Stat() (uint64, error) {
	h := fnv.New64a()
	targets, _ := b.listTargets()
	for _, t := range targets {
		h.Write([]byte(t))
		h.Write([]byte{0})
	}
	for _, target := range []string{"text/plain;charset=utf-8", "text/uri-list"} {
		if data, err := b.paste(target); err == nil {
			h.Write(data)
		}
	}
	return h.Sum64(), nil
}

func (b *linuxBackend) Read(allowed map[wire.Canonical]bool) (wire.Payload, error) {
	var payload wire.Payload

	targets, err := b.listTargets()
	if err != nil {
		return payload, err
	}

	seen := map[wire.Canonical]bool{}
	for _, canon := range wire.AllCanonical() {
		if !allowed[canon] || canon == wire.CanonFiles || seen[canon] {
			continue
		}
		mime, ok := wire.LinuxMIMEForCanonical(canon)
		if !ok || !hasTarget(targets, mime) {
			continue
		}
		data, err := b.paste(mime)
		if err != nil || len(data) == 0 {
			continue
		}
		payload.Items = append(payload.Items, wire.DataItem{Format: canon, Data: data})
		seen[canon] = true
	}

	if allowed[wire.CanonFiles] && hasTarget(targets, "text/uri-list") {
		if data, err := b.paste("text/uri-list"); err == nil {
			for _, path := range parseURIList(data) {
				contents, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				payload.Files = append(payload.Files, wire.DataFile{Name: filepath.Base(path), Data: contents})
			}
		}
	}

	return payload, nil
}

func (b *linuxBackend) Write(p wire.Payload) error {
	if len(p.Files) > 0 {
		var list bytes.Buffer
		for _, file := range p.Files {
			path := filepath.Join(b.stageDir, file.Name)
			if err := os.WriteFile(path, file.Data, 0o600); err != nil {
				return fmt.Errorf("stage file %q: %w", file.Name, err)
			}
			list.WriteString((&url.URL{Scheme: "file", Path: path}).String())
			list.WriteString("\r\n")
		}
		return b.copy("text/uri-list", list.Bytes())
	}

	if best, ok := richestItem(p.Items); ok {
		mime, _ := wire.LinuxMIMEForCanonical(best.Format)
		return b.copy(mime, best.Data)
	}
	return nil
}

// richestItem picks the highest-fidelity item: image first, then html, rtf,
// plain. Returns false when there are no items.
func richestItem(items []wire.DataItem) (wire.DataItem, bool) {
	priority := map[wire.Canonical]int{
		wire.CanonPNG: 5, wire.CanonTIFF: 5, wire.CanonPDF: 5,
		wire.CanonHTML: 4, wire.CanonRTF: 3, wire.CanonPlainText: 1,
	}
	best := wire.DataItem{}
	bestScore := -1
	for _, item := range items {
		if priority[item.Format] > bestScore {
			best, bestScore = item, priority[item.Format]
		}
	}
	return best, bestScore >= 0
}

// hasTarget reports whether the clipboard advertises a MIME target matching
// mime, comparing the part before any ";charset=" parameter and accepting the
// X11 plain-text aliases.
func hasTarget(targets []string, mime string) bool {
	want := baseMIME(mime)
	for _, t := range targets {
		if baseMIME(t) == want {
			return true
		}
		if want == "text/plain" && (t == "UTF8_STRING" || t == "STRING" || t == "TEXT") {
			return true
		}
	}
	return false
}

func baseMIME(m string) string {
	base, _, _ := strings.Cut(m, ";")
	return strings.TrimSpace(base)
}

func parseURIList(data []byte) []string {
	var paths []string
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if parsed, err := url.Parse(line); err == nil && parsed.Scheme == "file" {
			paths = append(paths, parsed.Path)
		}
	}
	return paths
}

func haveAll(tools ...string) bool {
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			return false
		}
	}
	return true
}

// ── Wayland (wl-clipboard) ───────────────────────────────────────────────────

func wlListTargets() ([]string, error) {
	out, err := exec.Command("wl-paste", "--list-types").Output()
	if err != nil {
		return nil, nil // empty clipboard exits non-zero; treat as no targets
	}
	return splitLines(out), nil
}

func wlPaste(target string) ([]byte, error) {
	return exec.Command("wl-paste", "--no-newline", "--type", target).Output()
}

func wlCopy(target string, data []byte) error {
	cmd := exec.Command("wl-copy", "--type", target)
	cmd.Stdin = bytes.NewReader(data)
	return cmd.Run()
}

// ── X11 (xclip) ──────────────────────────────────────────────────────────────

func xclipListTargets() ([]string, error) {
	out, err := exec.Command("xclip", "-selection", "clipboard", "-t", "TARGETS", "-o").Output()
	if err != nil {
		return nil, nil
	}
	return splitLines(out), nil
}

func xclipPaste(target string) ([]byte, error) {
	return exec.Command("xclip", "-selection", "clipboard", "-t", target, "-o").Output()
}

func xclipCopy(target string, data []byte) error {
	cmd := exec.Command("xclip", "-selection", "clipboard", "-t", target, "-i")
	cmd.Stdin = bytes.NewReader(data)
	return cmd.Run()
}

func splitLines(b []byte) []string {
	var lines []string
	for line := range strings.SplitSeq(string(b), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
