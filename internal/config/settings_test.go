//go:build darwin

package config

import (
	"testing"
)

func TestSettingsRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	original := &Settings{
		DefaultStorage:   "fast",
		StorageLocations: map[string]string{"fast": "/tmp/fast", "slow": "/tmp/slow"},
		CacheDir:         "/tmp/cache",
		Registry:         &RegistrySettings{Host: "ghcr.io", Organization: "acme"},
	}
	if err := original.Save(); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DefaultStorage != original.DefaultStorage ||
		loaded.CacheDir != original.CacheDir ||
		loaded.StorageLocations["fast"] != "/tmp/fast" ||
		loaded.StorageLocations["slow"] != "/tmp/slow" ||
		loaded.Registry == nil ||
		loaded.Registry.Host != "ghcr.io" ||
		loaded.Registry.Organization != "acme" {
		t.Fatalf("round trip mismatch: %+v", loaded)
	}

	path, ok := loaded.DefaultStoragePath()
	if !ok || path != "/tmp/fast" {
		t.Fatalf("DefaultStoragePath() = %q, %v", path, ok)
	}
}

func TestSettingsMissingFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	settings, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.DefaultStorage != "" || len(settings.StorageLocations) != 0 {
		t.Fatalf("expected zero-value settings, got %+v", settings)
	}
	if _, ok := settings.DefaultStoragePath(); ok {
		t.Fatal("DefaultStoragePath should not resolve on zero-value settings")
	}
}

func TestResolveStorageLocation(t *testing.T) {
	settings := &Settings{StorageLocations: map[string]string{"fast": "/tmp/fast"}}

	if path, err := settings.ResolveStorageLocation("fast"); err != nil || path != "/tmp/fast" {
		t.Fatalf("named location: got %q, %v", path, err)
	}
	if path, err := settings.ResolveStorageLocation("/abs/path"); err != nil || path != "/abs/path" {
		t.Fatalf("absolute path: got %q, %v", path, err)
	}
	if _, err := settings.ResolveStorageLocation("missing"); err == nil {
		t.Fatal("expected error for unknown location name")
	}
}
