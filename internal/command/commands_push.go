// Port of tart's Commands/Push.swift.
//go:build darwin

package command

import (
	"context"
	"fmt"
	"strings"

	weaveregistry "github.com/deploymenttheory/weave/internal/registry"

	weaveconfig "github.com/deploymenttheory/weave/internal/config"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/logging"
	"github.com/deploymenttheory/weave/internal/oci"
	"github.com/deploymenttheory/weave/internal/vmstorage"
)

// PushCommand ports the Push command.
type PushCommand struct {
	Registry      string // registry profile name for bare remote names
	LocalName     string
	RemoteNames   []string
	Insecure      bool
	Concurrency   uint
	ChunkSize     int
	Labels        []string
	PopulateCache bool
}

func (c *PushCommand) Run(ctx context.Context) error {
	ociStorage, err := vmstorage.NewVMStorageOCI()
	if err != nil {
		return err
	}
	localVMDir, err := vmstorage.VMStorageHelperOpen(c.LocalName)
	if err != nil {
		return err
	}
	lock, err := localVMDir.Lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	acquired, err := lock.Trylock()
	if err != nil {
		return err
	}
	if !acquired {
		return weaveerrors.ErrVMIsRunning(c.LocalName)
	}

	// Parse remote names supplied by the user and group them by registry.
	type registryIdentifier struct {
		host      string
		namespace string
	}
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		return err
	}
	registryGroups := map[registryIdentifier][]oci.RemoteName{}
	registryInsecure := map[registryIdentifier]bool{}
	for _, raw := range c.RemoteNames {
		remoteName, insecure, err := weaveregistry.ResolveName(raw, c.Registry, c.Insecure, settings)
		if err != nil {
			return err
		}
		id := registryIdentifier{host: remoteName.Host, namespace: remoteName.Namespace}
		registryGroups[id] = append(registryGroups[id], remoteName)
		registryInsecure[id] = registryInsecure[id] || insecure
	}

	// Push VM.
	for id, remoteNamesForRegistry := range registryGroups {
		registry, err := oci.NewRegistry(id.host, id.namespace, registryInsecure[id], nil)
		if err != nil {
			return err
		}

		logging.DefaultLogger().AppendNewLine(fmt.Sprintf("pushing %s to %s/%s%s...",
			c.LocalName, id.host, id.namespace, referenceNames(remoteNamesForRegistry)))

		references := make([]string, 0, len(remoteNamesForRegistry))
		for _, remoteName := range remoteNamesForRegistry {
			references = append(references, remoteName.Reference.Value)
		}

		var pushedRemoteName oci.RemoteName
		// If we're pushing a local OCI VM, check if it points to an already
		// existing registry manifest, and if so, only upload manifests
		// (without config, disk and NVRAM) to the user-specified references.
		if remoteName, err := oci.NewRemoteName(c.LocalName); err == nil {
			pushedRemoteName, err = c.lightweightPushToRegistry(ctx, registry, remoteName, references)
			if err != nil {
				return err
			}
		} else {
			pushedRemoteName, err = localVMDir.PushToRegistry(ctx, registry, references,
				c.ChunkSize, c.Concurrency, c.parseLabels())
			if err != nil {
				return err
			}
			// Populate the local cache (if requested).
			if c.PopulateCache {
				expectedPushedVMDir, err := ociStorage.Create(pushedRemoteName, false)
				if err != nil {
					return err
				}
				if err := localVMDir.Clone(expectedPushedVMDir, false); err != nil {
					return err
				}
			}
		}

		// Link the rest of the remote names.
		if c.PopulateCache {
			for _, remoteName := range remoteNamesForRegistry {
				if err := ociStorage.Link(remoteName, pushedRemoteName); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// lightweightPushToRegistry ports Push.lightweightPushToRegistry(registry:
// remoteName:references:).
func (c *PushCommand) lightweightPushToRegistry(ctx context.Context, registry *oci.Registry,
	remoteName oci.RemoteName, references []string) (oci.RemoteName, error) {
	// Is the local OCI VM already present in the registry?
	ociStorage, err := vmstorage.NewVMStorageOCI()
	if err != nil {
		return oci.RemoteName{}, err
	}
	digest, err := ociStorage.Digest(remoteName)
	if err != nil {
		return oci.RemoteName{}, err
	}

	remoteManifest, _, err := registry.PullManifest(ctx, digest)
	if err != nil {
		return oci.RemoteName{}, err
	}

	// Overwrite the registry's references with the retrieved manifest.
	for _, reference := range references {
		logging.DefaultLogger().AppendNewLine(fmt.Sprintf("pushing manifest for %s...", reference))

		if _, err := registry.PushManifest(ctx, reference, remoteManifest); err != nil {
			return oci.RemoteName{}, err
		}
	}

	return oci.RemoteName{Host: registry.Host(), Namespace: registry.Namespace,
		Reference: oci.NewDigestReference(digest)}, nil
}

// parseLabels ports Push.parseLabels(): key=value pairs to a map; empty
// values are allowed, empty keys are not.
func (c *PushCommand) parseLabels() map[string]string {
	result := map[string]string{}
	for _, label := range c.Labels {
		key, value, _ := strings.Cut(strings.TrimSpace(label), "=")
		if key == "" {
			continue
		}
		result[key] = value
	}
	return result
}

// referenceNames ports the Collection<RemoteName>.referenceNames() extension.
func referenceNames(remoteNames []oci.RemoteName) string {
	references := make([]string, 0, len(remoteNames))
	for _, remoteName := range remoteNames {
		references = append(references, remoteName.Reference.FullyQualified())
	}

	switch len(references) {
	case 0:
		return "∅"
	case 1:
		return references[0]
	default:
		return "{" + strings.Join(references, ",") + "}"
	}
}
