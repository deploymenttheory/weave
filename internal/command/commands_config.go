// Port of lume's Commands/Config.swift: get or set application
// configuration. Subcommands (second-level dispatch in root.go style):
//
//	config get                                  show effective configuration
//	config storage list                         list named storage locations
//	config storage add <name> <path>            add a named storage location
//	config storage remove <name>                remove a named storage location
//	config storage default <name>               set the default storage
//	config cache dir [path]                     show or set the cache dir
//	config registry status                      show registry defaults
//	config registry ghcr [--registry H] [--organization O]
//	config network interfaces                   list bridgeable interfaces
//
// lume's "config telemetry" (OTel covers observability here) and
// "config registry gcs" (no GCS backend) are deliberately not ported.
//go:build darwin

package command

import (
	"context"
	"fmt"
	"os"
	"sort"

	weaveconfig "github.com/deploymenttheory/weave/internal/config"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	weavenetwork "github.com/deploymenttheory/weave/internal/network"
	"github.com/deploymenttheory/weave/internal/objcutil"
)

// ConfigCommand carries the raw subcommand arguments; dispatch happens in
// Run because the sub-verbs share the settings load/save cycle.
type ConfigCommand struct {
	Args []string
}

const configUsage = "usage: weave config <get|storage|cache|registry|network> ..."

func (c *ConfigCommand) Run(ctx context.Context) error {
	if len(c.Args) == 0 {
		return weaveerrors.ErrGeneric(configUsage)
	}

	// The config command must not silently degrade on a broken settings
	// file — it is the tool used to fix it.
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}

	verb, rest := c.Args[0], c.Args[1:]
	switch verb {
	case "get":
		return c.runGet(settings)
	case "storage":
		return c.runStorage(settings, rest)
	case "cache":
		return c.runCache(settings, rest)
	case "registry":
		return c.runRegistry(settings, rest)
	case "network":
		return c.runNetwork(rest)
	default:
		return weaveerrors.ErrGeneric(configUsage)
	}
}

func (c *ConfigCommand) runGet(settings *weaveconfig.Settings) error {
	config, err := weaveconfig.NewConfig()
	if err != nil {
		return err
	}

	defaultStorage := settings.DefaultStorage
	if defaultStorage == "" {
		defaultStorage = "<unset>"
	}
	fmt.Printf("Home directory: %s\n", objcutil.GoStr(config.WeaveHomeDir.Path()))
	fmt.Printf("Default storage: %s\n", defaultStorage)
	fmt.Printf("Cache directory: %s\n", objcutil.GoStr(config.WeaveCacheDir.Path()))

	host, organization := "ghcr.io", "<unset>"
	if settings.Registry != nil {
		if settings.Registry.Host != "" {
			host = settings.Registry.Host
		}
		if settings.Registry.Organization != "" {
			organization = settings.Registry.Organization
		}
	}
	fmt.Printf("Registry: %s\n", host)
	fmt.Printf("Organization: %s\n", organization)

	if len(settings.StorageLocations) > 0 {
		fmt.Println("Storage locations:")
		for _, name := range sortedKeys(settings.StorageLocations) {
			marker := ""
			if name == settings.DefaultStorage {
				marker = " (default)"
			}
			fmt.Printf("  %s: %s%s\n", name, settings.StorageLocations[name], marker)
		}
	}
	return nil
}

func (c *ConfigCommand) runStorage(settings *weaveconfig.Settings, args []string) error {
	if len(args) == 0 {
		return weaveerrors.ErrGeneric("usage: weave config storage <list|add|remove|default> ...")
	}

	switch args[0] {
	case "list":
		if len(settings.StorageLocations) == 0 {
			fmt.Println("No storage locations defined.")
			return nil
		}
		for _, name := range sortedKeys(settings.StorageLocations) {
			marker := ""
			if name == settings.DefaultStorage {
				marker = " (default)"
			}
			fmt.Printf("%s: %s%s\n", name, settings.StorageLocations[name], marker)
		}
		return nil

	case "add":
		if len(args) != 3 {
			return weaveerrors.ErrGeneric("usage: weave config storage add <name> <path>")
		}
		name, path := args[1], objcutil.ExpandTilde(args[2])
		if !weaveconfig.StorageLocationNamePattern.MatchString(name) {
			return weaveerrors.ErrInvalidStorageLocation(name)
		}
		if err := weaveconfig.ValidateStorageLocation(path); err != nil {
			return err
		}
		if settings.StorageLocations == nil {
			settings.StorageLocations = map[string]string{}
		}
		settings.StorageLocations[name] = path
		if err := settings.Save(); err != nil {
			return err
		}
		fmt.Printf("Added storage location %q: %s\n", name, path)
		return nil

	case "remove":
		if len(args) != 2 {
			return weaveerrors.ErrGeneric("usage: weave config storage remove <name>")
		}
		name := args[1]
		if _, ok := settings.StorageLocations[name]; !ok {
			return weaveerrors.ErrStorageLocationNotFound(name)
		}
		delete(settings.StorageLocations, name)
		if settings.DefaultStorage == name {
			settings.DefaultStorage = ""
		}
		if err := settings.Save(); err != nil {
			return err
		}
		fmt.Printf("Removed storage location %q\n", name)
		return nil

	case "default":
		if len(args) != 2 {
			return weaveerrors.ErrGeneric("usage: weave config storage default <name>")
		}
		name := args[1]
		path, err := settings.ResolveStorageLocation(name)
		if err != nil {
			return err
		}
		if err := weaveconfig.ValidateStorageLocation(path); err != nil {
			return err
		}
		settings.DefaultStorage = name
		if err := settings.Save(); err != nil {
			return err
		}
		fmt.Printf("Default storage set to %q (%s)\n", name, path)
		fmt.Println("Note: the WEAVE_HOME environment variable, when set, takes precedence.")
		return nil

	default:
		return weaveerrors.ErrGeneric("usage: weave config storage <list|add|remove|default> ...")
	}
}

func (c *ConfigCommand) runCache(settings *weaveconfig.Settings, args []string) error {
	if len(args) == 0 || args[0] != "dir" {
		return weaveerrors.ErrGeneric("usage: weave config cache dir [path]")
	}

	switch len(args) {
	case 1:
		config, err := weaveconfig.NewConfig()
		if err != nil {
			return err
		}
		fmt.Println(objcutil.GoStr(config.WeaveCacheDir.Path()))
		return nil
	case 2:
		path := objcutil.ExpandTilde(args[1])
		if err := weaveconfig.ValidateStorageLocation(path); err != nil {
			return err
		}
		settings.CacheDir = path
		if err := settings.Save(); err != nil {
			return err
		}
		fmt.Printf("Cache directory set to %s\n", path)
		return nil
	default:
		return weaveerrors.ErrGeneric("usage: weave config cache dir [path]")
	}
}

func (c *ConfigCommand) runRegistry(settings *weaveconfig.Settings, args []string) error {
	if len(args) == 0 {
		return weaveerrors.ErrGeneric("usage: weave config registry <status|ghcr|list|add|remove|default> ...")
	}

	switch args[0] {
	case "list":
		profiles := settings.RegistryProfiles()
		if len(profiles) == 0 {
			fmt.Println("No registry profiles configured.")
			return nil
		}
		for _, profile := range profiles {
			marker := " "
			if profile.IsDefault {
				marker = "*"
			}
			insecure := ""
			if profile.IsInsecure {
				insecure = " (insecure)"
			}
			fmt.Printf("%s %-16s %s/%s%s\n", marker, profile.Name, profile.Host, profile.Organization, insecure)
		}
		return nil

	case "add":
		fs := NewFlagSet("config registry add")
		host := fs.String("host", "ghcr.io", "")
		organization := fs.String("organization", "", "")
		insecure := fs.Bool("insecure", false, "")
		isDefault := fs.Bool("default", false, "")
		positionals, err := ParseInterleaved(fs, args[1:])
		if err != nil {
			return err
		}
		if len(positionals) != 1 || *organization == "" {
			return weaveerrors.ErrGeneric("usage: weave config registry add <name> --organization <org> [--host ghcr.io] [--insecure] [--default]")
		}
		name := positionals[0]

		profile := weaveconfig.RegistryProfile{
			Name: name, Host: *host, Organization: *organization,
			IsInsecure: *insecure, IsDefault: *isDefault,
		}
		profiles := settings.RegistryProfiles()
		replaced := false
		for index := range profiles {
			if *isDefault {
				profiles[index].IsDefault = false
			}
			if profiles[index].Name == name {
				profiles[index] = profile
				replaced = true
			}
		}
		if !replaced {
			profiles = append(profiles, profile)
		}
		settings.Registries = profiles
		if err := settings.ValidateRegistryProfiles(); err != nil {
			return err
		}
		if err := settings.Save(); err != nil {
			return err
		}
		fmt.Printf("Registry profile %q -> %s/%s\n", name, *host, *organization)
		return nil

	case "remove":
		if len(args) != 2 {
			return weaveerrors.ErrGeneric("usage: weave config registry remove <name>")
		}
		profiles := settings.RegistryProfiles()
		kept := profiles[:0]
		removed := false
		for _, profile := range profiles {
			if profile.Name == args[1] {
				removed = true
				continue
			}
			kept = append(kept, profile)
		}
		if !removed {
			return weaveerrors.ErrGeneric("no registry profile named %q", args[1])
		}
		settings.Registries = kept
		if err := settings.Save(); err != nil {
			return err
		}
		fmt.Printf("Removed registry profile %q\n", args[1])
		return nil

	case "default":
		if len(args) != 2 {
			return weaveerrors.ErrGeneric("usage: weave config registry default <name>")
		}
		profiles := settings.RegistryProfiles()
		found := false
		for index := range profiles {
			profiles[index].IsDefault = profiles[index].Name == args[1]
			found = found || profiles[index].IsDefault
		}
		if !found {
			return weaveerrors.ErrGeneric("no registry profile named %q", args[1])
		}
		settings.Registries = profiles
		if err := settings.Save(); err != nil {
			return err
		}
		fmt.Printf("Default registry profile is now %q\n", args[1])
		return nil

	case "status":
		if settings.Registry == nil {
			fmt.Println("Registry: ghcr.io (default)")
			fmt.Println("Organization: <unset>")
			return nil
		}
		host := settings.Registry.Host
		if host == "" {
			host = "ghcr.io"
		}
		organization := settings.Registry.Organization
		if organization == "" {
			organization = "<unset>"
		}
		fmt.Printf("Registry: %s\n", host)
		fmt.Printf("Organization: %s\n", organization)
		return nil

	case "ghcr":
		fs := NewFlagSet("config registry ghcr")
		host := fs.String("registry", "ghcr.io", "")
		organization := fs.String("organization", "", "")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		settings.Registry = &weaveconfig.RegistrySettings{Host: *host, Organization: *organization}
		if err := settings.Save(); err != nil {
			return err
		}
		fmt.Printf("Registry set to %s (organization: %s)\n", *host, *organization)
		return nil

	default:
		return weaveerrors.ErrGeneric("usage: weave config registry <status|ghcr|list|add|remove|default> ...")
	}
}

func (c *ConfigCommand) runNetwork(args []string) error {
	if len(args) != 1 || args[0] != "interfaces" {
		return weaveerrors.ErrGeneric("usage: weave config network interfaces")
	}
	interfaces := weavenetwork.BridgeInterfaces()
	if len(interfaces) == 0 {
		fmt.Fprintln(os.Stderr, "No bridgeable network interfaces found.")
		return nil
	}
	for _, description := range interfaces {
		fmt.Println(description)
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
