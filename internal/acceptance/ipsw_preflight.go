// IPSW pre-flight: the cache-or-download logic the provision suite runs
// before creating a macOS VM. Cached restore images live at
// ~/.weave/cache/IPSWs/sha256:<digest>.ipsw — the file name is the content
// digest, so a cached image is "valid" iff its SHA-256 matches its name.
// A valid cached image is reused; a corrupt one is deleted; if none remain,
// "latest" is returned so create downloads a fresh image.
//go:build darwin

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ipswPreflight validates the IPSW cache and returns either the path of a
// valid cached restore image or the literal "latest" (meaning: let create
// download one). It also returns a human-readable summary and removes any
// corrupt cache entries it finds.
func ipswPreflight(logf func(string, ...any)) (source string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	cacheDir := filepath.Join(home, ".weave", "cache", "IPSWs")
	matches, _ := filepath.Glob(filepath.Join(cacheDir, "*.ipsw"))

	if len(matches) == 0 {
		logf("no cached IPSW found; create will download the latest restore image")
		return "latest", nil
	}

	for _, path := range matches {
		expected, ok := digestFromName(filepath.Base(path))
		if !ok {
			logf("skipping %s (not a sha256-named cache entry)", filepath.Base(path))
			continue
		}
		logf("validating cached IPSW %s (%.1f GB)...", filepath.Base(path), sizeGB(path))
		actual, err := fileSHA256(path)
		if err != nil {
			logf("could not hash %s: %v", filepath.Base(path), err)
			continue
		}
		if actual == expected {
			logf("cache hit: SHA-256 matches; using %s", path)
			return path, nil
		}
		logf("cache entry %s is corrupt (sha256 %s != %s); removing", filepath.Base(path), actual, expected)
		_ = os.Remove(path)
	}

	logf("no valid cached IPSW; create will download the latest restore image")
	return "latest", nil
}

// digestFromName extracts the hex digest from a "sha256:<hex>.ipsw" name.
func digestFromName(name string) (string, bool) {
	name = strings.TrimSuffix(name, ".ipsw")
	const prefix = "sha256:"
	if !strings.HasPrefix(name, prefix) {
		return "", false
	}
	digest := strings.TrimPrefix(name, prefix)
	if len(digest) != 64 {
		return "", false
	}
	return digest, true
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func sizeGB(path string) float64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return float64(info.Size()) / 1e9
}

// presetForIPSW picks the unattended preset matching the restore image's
// macOS version: macOS 26.x → tahoe, macOS 15.x → sequoia. When the version
// cannot be read (e.g. "latest"), it falls back to the host's major version.
func presetForIPSW(source string) string {
	major := 0
	if source != "" && source != "latest" {
		major = ipswMajorVersion(source)
	}
	if major == 0 {
		major = hostMajorVersion()
	}
	switch {
	case major >= 26:
		// tahoe_test is the flow verified end-to-end (fresh install → desktop
		// → Remote Login → ssh) on macOS 26.5.1; the older "tahoe" preset has
		// stale coordinates/recipes and is not used by the provision suite.
		return "tahoe_test"
	case major == 15:
		return "sequoia"
	default:
		return "tahoe_test" // newest verified preset is the safest default
	}
}

// ipswMajorVersion reads ProductVersion from the IPSW's BuildManifest.plist.
func ipswMajorVersion(path string) int {
	output, err := exec.Command("sh", "-c",
		fmt.Sprintf("unzip -p %q BuildManifest.plist | plutil -extract ProductVersion raw -", path)).Output()
	if err != nil {
		return 0
	}
	return majorOf(strings.TrimSpace(string(output)))
}

func hostMajorVersion() int {
	output, err := exec.Command("sw_vers", "-productVersion").Output()
	if err != nil {
		return 0
	}
	return majorOf(strings.TrimSpace(string(output)))
}

func majorOf(version string) int {
	major := 0
	for _, char := range version {
		if char < '0' || char > '9' {
			break
		}
		major = major*10 + int(char-'0')
	}
	return major
}
