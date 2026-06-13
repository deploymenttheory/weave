// Port of tart's OCI/Manifest.swift. Struct fields are declared in
// alphabetical JSON-key order so encoding/json output matches Swift's
// .sortedKeys encoder, keeping manifest digests stable.
//go:build darwin

package oci

import (
	"encoding/json"
	"strconv"
	"time"

	weaveplatform "github.com/deploymenttheory/weave/internal/platform"
)

// OCI manifest and OCI config media types.
const (
	ociManifestMediaType = "application/vnd.oci.image.manifest.v1+json"
	OCIConfigMediaType   = "application/vnd.oci.image.config.v1+json"
)

// Layer media types. The cirruslabs.tart identifiers are a wire contract:
// they make weave-pushed images interchangeable with tart-pushed ones (and
// let weave pull tart images), so they must not be renamed to weave.
const (
	ConfigMediaType = "application/vnd.cirruslabs.tart.config.v1"
	DiskV2MediaType = "application/vnd.cirruslabs.tart.disk.v2"
	NvramMediaType  = "application/vnd.cirruslabs.tart.nvram.v1"
)

// Manifest annotations.
const (
	uncompressedDiskSizeAnnotation = "org.cirruslabs.tart.uncompressed-disk-size"
	uploadTimeAnnotation           = "org.cirruslabs.tart.upload-time"
)

// Manifest labels.
const DiskFormatLabel = "org.cirruslabs.tart.disk.format"

// Layer annotations.
const (
	uncompressedSizeAnnotation          = "org.cirruslabs.tart.uncompressed-size"
	uncompressedContentDigestAnnotation = "org.cirruslabs.tart.uncompressed-content-digest"
)

// OCIManifest ports tart's OCIManifest struct.
type OCIManifest struct {
	Annotations   map[string]string  `json:"annotations,omitempty"`
	Config        OCIManifestConfig  `json:"config"`
	Layers        []OCIManifestLayer `json:"layers"`
	MediaType     string             `json:"mediaType"`
	SchemaVersion int                `json:"schemaVersion"`
}

// NewOCIManifest ports OCIManifest.init(config:layers:uncompressedDiskSize:
// uploadDate:); zero values mean "absent" for the optional parameters.
func NewOCIManifest(config OCIManifestConfig, layers []OCIManifestLayer, uncompressedDiskSize uint64, uploadDate time.Time) OCIManifest {
	annotations := map[string]string{}
	if uncompressedDiskSize != 0 {
		annotations[uncompressedDiskSizeAnnotation] = strconv.FormatUint(uncompressedDiskSize, 10)
	}
	if !uploadDate.IsZero() {
		annotations[uploadTimeAnnotation] = uploadDate.UTC().Format(time.RFC3339)
	}

	return OCIManifest{
		Annotations:   annotations,
		Config:        config,
		Layers:        layers,
		MediaType:     ociManifestMediaType,
		SchemaVersion: 2,
	}
}

// NewOCIManifestFromJSON ports OCIManifest.init(fromJSON:).
func NewOCIManifestFromJSON(data []byte) (OCIManifest, error) {
	var manifest OCIManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return OCIManifest{}, err
	}
	return manifest, nil
}

// ToJSON ports OCIManifest.toJSON().
func (m OCIManifest) ToJSON() ([]byte, error) {
	return json.Marshal(m)
}

// ManifestDigest ports OCIManifest.digest().
func (m OCIManifest) ManifestDigest() (string, error) {
	data, err := m.ToJSON()
	if err != nil {
		return "", err
	}
	return DigestHash(data), nil
}

// UncompressedDiskSize ports OCIManifest.uncompressedDiskSize().
func (m OCIManifest) UncompressedDiskSize() (uint64, bool) {
	value, ok := m.Annotations[uncompressedDiskSizeAnnotation]
	if !ok {
		return 0, false
	}
	size, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, false
	}
	return size, true
}

// OCIConfig ports tart's OCIConfig struct.
type OCIConfig struct {
	Architecture weaveplatform.Architecture `json:"architecture"`
	Config       *OCIConfigContainer        `json:"config,omitempty"`
	OS           weaveplatform.OS           `json:"os"`
}

// OCIConfigContainer ports OCIConfig.ConfigContainer.
type OCIConfigContainer struct {
	Labels map[string]string `json:"Labels,omitempty"`
}

// ToJSON ports OCIConfig.toJSON().
func (c OCIConfig) ToJSON() ([]byte, error) {
	return json.Marshal(c)
}

// OCIManifestConfig ports tart's OCIManifestConfig struct.
type OCIManifestConfig struct {
	Digest    string `json:"digest"`
	MediaType string `json:"mediaType"`
	Size      int    `json:"size"`
}

// OCIManifestLayer ports tart's OCIManifestLayer struct; equality and
// hashing are by digest, like the Swift original.
type OCIManifestLayer struct {
	Annotations map[string]string `json:"annotations,omitempty"`
	Digest      string            `json:"digest"`
	MediaType   string            `json:"mediaType"`
	Size        int               `json:"size"`
}

// NewOCIManifestLayer ports OCIManifestLayer.init(mediaType:size:digest:
// uncompressedSize:uncompressedContentDigest:); zero values mean "absent".
func NewOCIManifestLayer(mediaType string, size int, digest string, uncompressedSize uint64, uncompressedContentDigest string) OCIManifestLayer {
	annotations := map[string]string{}
	if uncompressedSize != 0 {
		annotations[uncompressedSizeAnnotation] = strconv.FormatUint(uncompressedSize, 10)
	}
	if uncompressedContentDigest != "" {
		annotations[uncompressedContentDigestAnnotation] = uncompressedContentDigest
	}

	return OCIManifestLayer{
		Annotations: annotations,
		Digest:      digest,
		MediaType:   mediaType,
		Size:        size,
	}
}

// UncompressedSize ports OCIManifestLayer.uncompressedSize().
func (l OCIManifestLayer) UncompressedSize() (uint64, bool) {
	value, ok := l.Annotations[uncompressedSizeAnnotation]
	if !ok {
		return 0, false
	}
	size, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, false
	}
	return size, true
}

// UncompressedContentDigest ports OCIManifestLayer.uncompressedContentDigest().
func (l OCIManifestLayer) UncompressedContentDigest() (string, bool) {
	digest, ok := l.Annotations[uncompressedContentDigestAnnotation]
	return digest, ok
}

// Descriptor ports tart's Descriptor struct.
type Descriptor struct {
	Size   int
	Digest string
}
