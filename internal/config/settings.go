// Port of lume's FileSystem/Settings.swift: a YAML settings file holding
// named storage locations, the default storage selection, a cache directory
// override and registry defaults. Stored at
// $XDG_CONFIG_HOME/weave/config.yaml (default ~/.config/weave/config.yaml).
//
// Resolution order for the VM home directory (see NewConfig in config.go):
// WEAVE_HOME environment variable wins, then Settings.DefaultStorage, then
// ~/.weave. A broken settings file is non-fatal everywhere except in the
// config command itself: commands warn and fall back to ~/.weave.
//go:build darwin

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/deploymenttheory/weave/internal/clipboardpolicy"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/objcutil"

	"gopkg.in/yaml.v3"
)

// RegistrySettings holds default registry coordinates used by commands that
// accept a repository (e.g. images).
type RegistrySettings struct {
	Host         string `yaml:"host,omitempty" json:"host,omitempty"`
	Organization string `yaml:"organization,omitempty" json:"organization,omitempty"`
}

// Settings is the persisted configuration.
type Settings struct {
	DefaultStorage   string            `yaml:"defaultStorage,omitempty" json:"defaultStorage,omitempty"` // name in StorageLocations or an absolute path
	StorageLocations map[string]string `yaml:"storageLocations,omitempty" json:"storageLocations,omitempty"`
	CacheDir         string            `yaml:"cacheDir,omitempty" json:"cacheDir,omitempty"`
	Registry         *RegistrySettings `yaml:"registry,omitempty" json:"registry,omitempty"`
	Registries       []RegistryProfile `yaml:"registries,omitempty" json:"registries,omitempty"`
	// DefaultClipboardPolicy is the enterprise clipboard policy applied to VMs
	// that do not carry their own, overridable per-run by --clipboard-* flags.
	DefaultClipboardPolicy *clipboardpolicy.Policy `yaml:"defaultClipboardPolicy,omitempty" json:"defaultClipboardPolicy,omitempty"`
}

var StorageLocationNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// settingsPath returns the settings file location ($XDG_CONFIG_HOME aware).
func settingsPath() (string, error) {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", weaveerrors.ErrInvalidHomeDirectory()
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "weave", "config.yaml"), nil
}

// LoadSettings reads the settings file; a missing file yields zero-value
// settings.
func LoadSettings() (*Settings, error) {
	path, err := settingsPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Settings{}, nil
		}
		return nil, err
	}

	settings := &Settings{}
	if err := yaml.Unmarshal(data, settings); err != nil {
		return nil, fmt.Errorf("failed to parse settings file %s: %w", path, err)
	}
	return settings, nil
}

// loadSettingsOnce caches the settings for the lifetime of the process so
// every NewConfig() call doesn't re-read the file.
var loadSettingsOnce = sync.OnceValues(LoadSettings)

// settingsOrWarn returns the cached settings, warning once (and degrading to
// defaults) when the file is unreadable.
var settingsWarnOnce sync.Once

func settingsOrWarn() *Settings {
	settings, err := loadSettingsOnce()
	if err != nil {
		settingsWarnOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "warning: ignoring settings file: %v\n", err)
		})
		return &Settings{}
	}
	return settings
}

// Save atomically writes the settings file with 0600 permissions.
func (s *Settings) Save() error {
	path, err := settingsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return weaveerrors.ErrDirectoryCreationFailed(filepath.Dir(path))
	}

	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// ResolveStorageLocation resolves a storage location name (or absolute
// path) to a path.
func (s *Settings) ResolveStorageLocation(nameOrPath string) (string, error) {
	if path, ok := s.StorageLocations[nameOrPath]; ok {
		return path, nil
	}
	if filepath.IsAbs(objcutil.ExpandTilde(nameOrPath)) {
		return objcutil.ExpandTilde(nameOrPath), nil
	}
	return "", weaveerrors.ErrStorageLocationNotFound(nameOrPath)
}

// DefaultStoragePath resolves Settings.DefaultStorage, when set.
func (s *Settings) DefaultStoragePath() (string, bool) {
	if s.DefaultStorage == "" {
		return "", false
	}
	path, err := s.ResolveStorageLocation(s.DefaultStorage)
	if err != nil {
		return "", false
	}
	return path, true
}

// ValidateStorageLocation checks a location is usable: the directory is
// created when missing, must be a directory and must be writable.
func ValidateStorageLocation(path string) error {
	info, err := os.Stat(path)
	switch {
	case os.IsNotExist(err):
		if err := os.MkdirAll(path, 0o755); err != nil {
			return weaveerrors.ErrDirectoryCreationFailed(path)
		}
	case err != nil:
		return weaveerrors.ErrDirectoryAccessDenied(path)
	case !info.IsDir():
		return weaveerrors.ErrStorageLocationNotADirectory(path)
	}

	probe, err := os.CreateTemp(path, ".weave-write-probe-*")
	if err != nil {
		return weaveerrors.ErrStorageLocationNotWritable(path)
	}
	probe.Close()
	_ = os.Remove(probe.Name())
	return nil
}

// RegistryProfile is one named registry the resolver can pull from or push
// to. Type "" or "oci" selects the generic OCI-distribution transport.
type RegistryProfile struct {
	Name         string `yaml:"name" json:"name"`
	Type         string `yaml:"type,omitempty" json:"type,omitempty"`
	Host         string `yaml:"host,omitempty" json:"host,omitempty"`
	Organization string `yaml:"organization,omitempty" json:"organization,omitempty"`
	IsInsecure   bool   `yaml:"insecure,omitempty" json:"insecure,omitempty"`
	IsDefault    bool   `yaml:"default,omitempty" json:"default,omitempty"`
}

// RegistryProfiles returns the configured profiles. When none are configured
// but the legacy registry: block (host/organization) is, a synthetic default
// profile named "default" is derived from it so old settings keep working.
func (s *Settings) RegistryProfiles() []RegistryProfile {
	if len(s.Registries) > 0 {
		return s.Registries
	}
	if s.Registry != nil && s.Registry.Host != "" && s.Registry.Organization != "" {
		return []RegistryProfile{{
			Name:         "default",
			Host:         s.Registry.Host,
			Organization: s.Registry.Organization,
			IsDefault:    true,
		}}
	}
	return nil
}

// FindRegistryProfile looks a profile up by name.
func (s *Settings) FindRegistryProfile(name string) (RegistryProfile, bool) {
	for _, profile := range s.RegistryProfiles() {
		if profile.Name == name {
			return profile, true
		}
	}
	return RegistryProfile{}, false
}

// DefaultRegistryProfile returns the profile marked default, if any.
func (s *Settings) DefaultRegistryProfile() (RegistryProfile, bool) {
	for _, profile := range s.RegistryProfiles() {
		if profile.IsDefault {
			return profile, true
		}
	}
	return RegistryProfile{}, false
}

// RegistryProfileForHost returns the first profile whose host matches; used
// to inherit per-host defaults (e.g. insecure) for fully-qualified
// references.
func (s *Settings) RegistryProfileForHost(host string) (RegistryProfile, bool) {
	for _, profile := range s.RegistryProfiles() {
		if profile.Host == host {
			return profile, true
		}
	}
	return RegistryProfile{}, false
}

// ValidateRegistryProfiles enforces the profile invariants: valid names,
// hosts present, at most one default.
func (s *Settings) ValidateRegistryProfiles() error {
	defaults := 0
	seen := map[string]bool{}
	for _, profile := range s.Registries {
		if !StorageLocationNamePattern.MatchString(profile.Name) {
			return weaveerrors.ErrInvalidStorageLocation(profile.Name)
		}
		if seen[profile.Name] {
			return weaveerrors.ErrInvalidStorageLocation(profile.Name + " (duplicate)")
		}
		seen[profile.Name] = true
		switch profile.Type {
		case "", "oci":
			if profile.Host == "" {
				return weaveerrors.ErrInvalidStorageLocation(profile.Name + " (missing host)")
			}
		default:
			return weaveerrors.ErrInvalidStorageLocation(profile.Name + " (unsupported type " + profile.Type + ")")
		}
		if profile.IsDefault {
			defaults++
		}
	}
	if defaults > 1 {
		return weaveerrors.ErrInvalidStorageLocation("more than one default registry profile")
	}
	return nil
}
