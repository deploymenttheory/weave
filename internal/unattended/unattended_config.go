// Port of lume's Unattended/UnattendedConfig.swift: the YAML configuration
// for unattended Setup Assistant automation, with built-in presets embedded
// from unattended-presets/ (copied from lume's resource bundle).
//go:build darwin

package unattended

import (
	"embed"
	"fmt"
	"os"
	"sort"
	"strings"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/objcutil"

	"gopkg.in/yaml.v3"
)

//go:embed unattended-presets/*.yml
var unattendedPresets embed.FS

// UnattendedHealthCheck mirrors lume's HealthCheck.
type UnattendedHealthCheck struct {
	Type       string `yaml:"type"` // "ssh" or "http"
	User       string `yaml:"user,omitempty"`
	Password   string `yaml:"password,omitempty"`
	Timeout    int    `yaml:"timeout,omitempty"`     // seconds, default 30
	Retries    int    `yaml:"retries,omitempty"`     // default 3
	RetryDelay int    `yaml:"retry_delay,omitempty"` // seconds, default 5
}

// UnattendedConfig mirrors lume's UnattendedConfig.
type UnattendedConfig struct {
	BootWait        int                    `yaml:"boot_wait"` // seconds before automation starts
	BootCommands    []string               `yaml:"boot_commands"`
	HealthCheck     *UnattendedHealthCheck `yaml:"health_check,omitempty"`
	PostSSHCommands []string               `yaml:"post_ssh_commands,omitempty"`
}

// LoadUnattendedConfig loads a preset by name (e.g. "sequoia", "tahoe") or
// a YAML file by path.
func LoadUnattendedConfig(pathOrPreset string) (*UnattendedConfig, error) {
	if data, err := unattendedPresets.ReadFile("unattended-presets/" + pathOrPreset + ".yml"); err == nil {
		return parseUnattendedConfig(data)
	}

	data, err := os.ReadFile(objcutil.ExpandTilde(pathOrPreset))
	if err != nil {
		if os.IsNotExist(err) {
			available := AvailableUnattendedPresets()
			return nil, weaveerrors.ErrConfigLoadFailed(fmt.Sprintf(
				"%q is neither a preset (available: %s) nor a readable file",
				pathOrPreset, strings.Join(available, ", ")))
		}
		return nil, weaveerrors.ErrConfigLoadFailed(err.Error())
	}
	return parseUnattendedConfig(data)
}

// AvailableUnattendedPresets lists the embedded preset names.
func AvailableUnattendedPresets() []string {
	entries, err := unattendedPresets.ReadDir("unattended-presets")
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		names = append(names, strings.TrimSuffix(entry.Name(), ".yml"))
	}
	sort.Strings(names)
	return names
}

func parseUnattendedConfig(data []byte) (*UnattendedConfig, error) {
	config := &UnattendedConfig{BootWait: 60}
	if err := yaml.Unmarshal(data, config); err != nil {
		return nil, weaveerrors.ErrConfigLoadFailed(err.Error())
	}
	if len(config.BootCommands) == 0 {
		return nil, weaveerrors.ErrConfigLoadFailed("missing required field: boot_commands")
	}
	// Validate the commands eagerly so failures surface before boot.
	if _, err := ParseBootCommands(config.BootCommands); err != nil {
		return nil, err
	}
	return config, nil
}
