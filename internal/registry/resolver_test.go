//go:build darwin

package registry

import (
	"strings"
	"testing"

	weaveconfig "github.com/deploymenttheory/weave/internal/config"
)

func testSettings() *weaveconfig.Settings {
	return &weaveconfig.Settings{Registries: []weaveconfig.RegistryProfile{
		{Name: "weave", Host: "ghcr.io", Organization: "deploymenttheory", IsDefault: true},
		{Name: "cua", Host: "ghcr.io", Organization: "trycua"},
		{Name: "corp", Host: "registry.internal.example:5000", Organization: "vm-images", IsInsecure: true},
	}}
}

// TestResolveName pins the backward-compatibility matrix from
// docs/registries-and-image-formats.md.
func TestResolveName(t *testing.T) {
	settings := testSettings()

	cases := []struct {
		reference    string
		profile      string
		wantName     string
		wantInsecure bool
	}{
		// Rule 2: fully-qualified references are used verbatim.
		{"ghcr.io/cirruslabs/macos-sonoma:latest", "", "ghcr.io/cirruslabs/macos-sonoma:latest", false},
		{"ghcr.io/trycua/macos-sequoia-cua:latest", "", "ghcr.io/trycua/macos-sequoia-cua:latest", false},
		// Rule 2 + per-host insecure inheritance from a matching profile.
		{"registry.internal.example:5000/vm-images/base:1", "", "registry.internal.example:5000/vm-images/base:1", true},
		// Rule 3: bare names resolve against the default profile.
		{"macos-sequoia-weave:latest", "", "ghcr.io/deploymenttheory/macos-sequoia-weave:latest", false},
		{"macos-sequoia-weave", "", "ghcr.io/deploymenttheory/macos-sequoia-weave:latest", false},
		// Rule 1: --registry selects a named profile.
		{"macos-sequoia-cua:latest", "cua", "ghcr.io/trycua/macos-sequoia-cua:latest", false},
		{"base:1", "corp", "registry.internal.example:5000/vm-images/base:1", true},
	}
	for _, c := range cases {
		remoteName, insecure, err := ResolveName(c.reference, c.profile, false, settings)
		if err != nil {
			t.Errorf("%q (--registry %q): %v", c.reference, c.profile, err)
			continue
		}
		if remoteName.String() != c.wantName {
			t.Errorf("%q (--registry %q): resolved %q, want %q", c.reference, c.profile, remoteName, c.wantName)
		}
		if insecure != c.wantInsecure {
			t.Errorf("%q (--registry %q): insecure = %v, want %v", c.reference, c.profile, insecure, c.wantInsecure)
		}
	}
}

func TestResolveNameErrors(t *testing.T) {
	// Unknown profile.
	if _, _, err := ResolveName("x:y", "nope", false, testSettings()); err == nil ||
		!strings.Contains(err.Error(), "unknown registry profile") {
		t.Errorf("unknown profile: %v", err)
	}

	// Bare name without any default profile.
	noDefault := &weaveconfig.Settings{}
	_, _, err := ResolveName("macos:latest", "", false, noDefault)
	if err == nil || !strings.Contains(err.Error(), "no default registry profile") {
		t.Errorf("missing default: %v", err)
	}
}

// TestResolveNameLegacyAlias checks the deprecated registry: block still
// provides a default profile.
func TestResolveNameLegacyAlias(t *testing.T) {
	settings := &weaveconfig.Settings{
		Registry: &weaveconfig.RegistrySettings{Host: "ghcr.io", Organization: "deploymenttheory"},
	}
	remoteName, _, err := ResolveName("base:2", "", false, settings)
	if err != nil {
		t.Fatal(err)
	}
	if remoteName.String() != "ghcr.io/deploymenttheory/base:2" {
		t.Fatalf("resolved %q", remoteName)
	}
}

func TestValidateRegistryProfiles(t *testing.T) {
	bad := &weaveconfig.Settings{Registries: []weaveconfig.RegistryProfile{
		{Name: "a", Host: "ghcr.io", Organization: "x", IsDefault: true},
		{Name: "b", Host: "ghcr.io", Organization: "y", IsDefault: true},
	}}
	if err := bad.ValidateRegistryProfiles(); err == nil {
		t.Error("two defaults must fail validation")
	}

	missingHost := &weaveconfig.Settings{Registries: []weaveconfig.RegistryProfile{
		{Name: "a", Organization: "x"},
	}}
	if err := missingHost.ValidateRegistryProfiles(); err == nil {
		t.Error("missing host must fail validation")
	}

	unsupportedType := &weaveconfig.Settings{Registries: []weaveconfig.RegistryProfile{
		{Name: "a", Type: "ftp", Host: "h", Organization: "x"},
	}}
	if err := unsupportedType.ValidateRegistryProfiles(); err == nil {
		t.Error("unsupported type must fail validation")
	}
}
