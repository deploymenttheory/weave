// Port of tart's VMDirectory+OCI.swift: pulling and pushing a VM directory
// to an OCI registry.
//go:build darwin

package vmdirectory

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/deploymenttheory/weave/internal/vmconfig"

	"github.com/deploymenttheory/weave/internal/logging"
	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/oci"
	"github.com/deploymenttheory/weave/internal/prune"
)

// PullFromRegistry ports VMDirectory.pullFromRegistry(registry:manifest:
// concurrency:localLayerCache:deduplicate:), extended with image-format
// dispatch: the manifest's layer media types select the codec (tart or one
// of the lume variants), and codecs that cannot write a weave config.json
// themselves return a VMDescription which is translated here.
func (d *VMDirectory) PullFromRegistry(ctx context.Context, source oci.BlobSource, manifest oci.OCIManifest,
	concurrency uint, localLayerCache *oci.LocalLayerCache, deduplicate bool) error {
	format := oci.DetectImageFormat(manifest)
	codec, err := oci.CodecFor(format, manifest)
	if err != nil {
		return err
	}
	if format != oci.ImageFormatTart {
		logging.DefaultLogger().AppendNewLine(fmt.Sprintf("detected %s image format", format))
	}

	destination := oci.PullDestination{
		ConfigPath: objcutil.GoStr(d.ConfigURL().Path()),
		DiskPath:   objcutil.GoStr(d.DiskURL().Path()),
		NvramPath:  objcutil.GoStr(d.NvramURL().Path()),
	}

	description, err := codec.Pull(ctx, source, manifest, destination, concurrency, localLayerCache, deduplicate)
	if err != nil {
		return err
	}

	// Lume formats carry VM metadata instead of a weave config.json.
	if description != nil {
		if err := d.writeLumeConfig(description); err != nil {
			return err
		}
	}

	if deduplicate && localLayerCache != nil {
		// Set a custom attribute to remember the deduplicated bytes.
		prune.NewPrunableURL(d.DiskURL()).SetDeduplicatedBytes(localLayerCache.DeduplicatedBytes)
	}

	// Serialize VM's manifest to enable better deduplication on subsequent
	// "tart pull"'s.
	manifestJSON, err := manifest.ToJSON()
	if err != nil {
		return err
	}
	return os.WriteFile(objcutil.GoStr(d.ManifestURL().Path()), manifestJSON, 0o644)
}

// PushToRegistry ports VMDirectory.pushToRegistry(registry:references:
// chunkSizeMb:concurrency:labels:).
func (d *VMDirectory) PushToRegistry(ctx context.Context, registry *oci.Registry, references []string,
	chunkSizeMb int, concurrency uint, labels map[string]string) (oci.RemoteName, error) {
	var layers []oci.OCIManifestLayer

	// Read VM's config and push it as a blob.
	config, err := vmconfig.NewVMConfigFromURL(d.ConfigURL())
	if err != nil {
		return oci.RemoteName{}, err
	}

	// Add the disk format label automatically.
	if labels == nil {
		labels = map[string]string{}
	}
	labels[oci.DiskFormatLabel] = string(config.DiskFormat)

	configJSON, err := config.ToJSON()
	if err != nil {
		return oci.RemoteName{}, err
	}
	logging.DefaultLogger().AppendNewLine("pushing config...")
	configDigest, err := registry.PushBlob(ctx, configJSON, chunkSizeMb, "")
	if err != nil {
		return oci.RemoteName{}, err
	}
	layers = append(layers, oci.NewOCIManifestLayer(oci.ConfigMediaType, len(configJSON), configDigest, 0, ""))

	// Compress the disk file as multiple chunks and push them as layers.
	diskInfo, err := os.Stat(objcutil.GoStr(d.DiskURL().Path()))
	if err != nil {
		return oci.RemoteName{}, err
	}
	diskSize := diskInfo.Size()

	logging.DefaultLogger().AppendNewLine("pushing disk... this will take a while...")
	progress := logging.NewDownloadProgress(diskSize)
	logging.NewProgressObserver(progress).Log(logging.DefaultLogger())

	diskLayers, err := (oci.DiskV2{}).Push(ctx, d.DiskURL(), registry, chunkSizeMb, concurrency, progress)
	if err != nil {
		return oci.RemoteName{}, err
	}
	layers = append(layers, diskLayers...)

	// Read VM's NVRAM and push it as a blob.
	logging.DefaultLogger().AppendNewLine("pushing NVRAM...")

	nvram, err := os.ReadFile(objcutil.GoStr(d.NvramURL().Path()))
	if err != nil {
		return oci.RemoteName{}, err
	}
	nvramDigest, err := registry.PushBlob(ctx, nvram, chunkSizeMb, "")
	if err != nil {
		return oci.RemoteName{}, err
	}
	layers = append(layers, oci.NewOCIManifestLayer(oci.NvramMediaType, len(nvram), nvramDigest, 0, ""))

	// Craft a stub OCI config for Docker Hub compatibility.
	ociConfigJSON, err := oci.OCIConfig{
		Architecture: config.Arch,
		OS:           config.OS,
		Config:       &oci.OCIConfigContainer{Labels: labels},
	}.ToJSON()
	if err != nil {
		return oci.RemoteName{}, err
	}
	ociConfigDigest, err := registry.PushBlob(ctx, ociConfigJSON, chunkSizeMb, "")
	if err != nil {
		return oci.RemoteName{}, err
	}
	manifest := oci.NewOCIManifest(
		oci.OCIManifestConfig{Digest: ociConfigDigest, MediaType: oci.OCIConfigMediaType, Size: len(ociConfigJSON)},
		layers, uint64(diskSize), time.Now())

	// Manifest.
	for _, reference := range references {
		logging.DefaultLogger().AppendNewLine(fmt.Sprintf("pushing manifest for %s...", reference))

		if _, err := registry.PushManifest(ctx, reference, manifest); err != nil {
			return oci.RemoteName{}, err
		}
	}

	manifestDigest, err := manifest.ManifestDigest()
	if err != nil {
		return oci.RemoteName{}, err
	}
	return oci.RemoteName{
		Host:      registry.Host(),
		Namespace: registry.Namespace,
		Reference: oci.NewDigestReference(manifestDigest),
	}, nil
}
