//go:build darwin

package oci

import (
	"os"
	"strings"
	"testing"
)

func loadFixtureManifest(t *testing.T, name string) OCIManifest {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := NewOCIManifestFromJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

// TestDetectImageFormatFixtures pins detection against live-captured
// manifests (see docs/registries-and-image-formats.md).
func TestDetectImageFormatFixtures(t *testing.T) {
	cases := []struct {
		fixture string
		want    ImageFormat
	}{
		{"tart-sonoma-manifest.json", ImageFormatTart},
		{"lume-sharded-vanilla-manifest.json", ImageFormatLumeSharded},
		{"lume-sharded-cua-manifest.json", ImageFormatLumeSharded},
	}
	for _, c := range cases {
		if got := DetectImageFormat(loadFixtureManifest(t, c.fixture)); got != c.want {
			t.Errorf("%s: detected %s, want %s", c.fixture, got, c.want)
		}
	}
}

func TestDetectImageFormatSynthetic(t *testing.T) {
	manifestWithLayers := func(config string, mediaTypes ...string) OCIManifest {
		manifest := OCIManifest{Config: OCIManifestConfig{MediaType: config}}
		for _, mediaType := range mediaTypes {
			manifest.Layers = append(manifest.Layers, OCIManifestLayer{MediaType: mediaType})
		}
		return manifest
	}

	cases := []struct {
		name     string
		manifest OCIManifest
		want     ImageFormat
	}{
		{"empty", OCIManifest{}, ImageFormatUnknown},
		{"chunked by disk layer", manifestWithLayers("", LumeDiskMediaType, LumeNvramMediaType), ImageFormatLumeChunked},
		{"chunked by config descriptor", manifestWithLayers(LumeConfigMediaType), ImageFormatLumeChunked},
		{"lz4 legacy", manifestWithLayers("",
			"application/octet-stream+lz4", "application/octet-stream+lz4",
			OCIConfigMediaType, octetStreamMediaType), ImageFormatLumeLZ4},
		{"sharded lzfse variant", manifestWithLayers("application/vnd.oci.empty.v1+json",
			"application/octet-stream+lzfse;part.number=1;part.total=2",
			"application/octet-stream+lzfse;part.number=2;part.total=2",
			OCIConfigMediaType, octetStreamMediaType), ImageFormatLumeSharded},
		{"sharded single tar", manifestWithLayers("application/vnd.oci.empty.v1+json",
			lumeShardedLayerMediaType, OCIConfigMediaType, octetStreamMediaType), ImageFormatLumeSharded},
		{"tart wins over anything", manifestWithLayers("",
			DiskV2MediaType, ConfigMediaType, NvramMediaType), ImageFormatTart},
		{"plain docker image", manifestWithLayers("application/vnd.docker.container.image.v1+json",
			"application/vnd.docker.image.rootfs.diff.tar.gzip"), ImageFormatUnknown},
	}
	for _, c := range cases {
		if got := DetectImageFormat(c.manifest); got != c.want {
			t.Errorf("%s: detected %s, want %s", c.name, got, c.want)
		}
	}
}

func TestCodecForUnknownListsMediaTypes(t *testing.T) {
	manifest := OCIManifest{Layers: []OCIManifestLayer{
		{MediaType: "application/x-mystery"},
	}}
	_, err := CodecFor(DetectImageFormat(manifest), manifest)
	if err == nil {
		t.Fatal("expected an error for an unknown format")
	}
	if got := err.Error(); !strings.Contains(got, "application/x-mystery") {
		t.Fatalf("error does not list the offending media type: %s", got)
	}
}

func TestParseLayerMediaType(t *testing.T) {
	base, params := parseLayerMediaType("application/vnd.oci.image.layer.v1.tar;part.number=7;part.total=164")
	if base != "application/vnd.oci.image.layer.v1.tar" {
		t.Fatalf("base = %q", base)
	}
	if params["part.number"] != "7" || params["part.total"] != "164" {
		t.Fatalf("params = %v", params)
	}

	base, params = parseLayerMediaType("application/octet-stream")
	if base != "application/octet-stream" || len(params) != 0 {
		t.Fatalf("plain type mangled: %q %v", base, params)
	}
}
