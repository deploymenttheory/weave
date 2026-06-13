//go:build darwin

package oci

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRemoteNameParsing(t *testing.T) {
	name, err := NewRemoteName("ghcr.io/cirruslabs/macos-sonoma-base:latest")
	if err != nil {
		t.Fatal(err)
	}
	if name.Host != "ghcr.io" || name.Namespace != "cirruslabs/macos-sonoma-base" ||
		name.Reference != NewTagReference("latest") {
		t.Fatalf("unexpected: %+v", name)
	}

	name, err = NewRemoteName("127.0.0.1:8080/foo/bar@sha256:deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if name.Host != "127.0.0.1:8080" || name.Reference.Type != ReferenceTypeDigest ||
		name.Reference.Value != "sha256:deadbeef" {
		t.Fatalf("unexpected: %+v", name)
	}

	name, err = NewRemoteName("example.com/vm")
	if err != nil || name.Reference != NewTagReference("latest") {
		t.Fatalf("default tag: %+v, %v", name, err)
	}
	if name.String() != "example.com/vm:latest" {
		t.Fatalf("description: %s", name)
	}

	for _, invalid := range []string{"", "novmname", "host/", "/ns", "host:notaport/ns", "host/ns@md5:x", "host/ns:bad..tag", "host/ns:"} {
		if _, err := NewRemoteName(invalid); err == nil {
			t.Fatalf("expected error for %q", invalid)
		}
	}
}

func TestWWWAuthenticateParsing(t *testing.T) {
	header, err := NewWWWAuthenticate(`Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:user/image:pull"`)
	if err != nil {
		t.Fatal(err)
	}
	if header.Scheme != "Bearer" || header.KVs["realm"] != "https://ghcr.io/token" ||
		header.KVs["service"] != "ghcr.io" || header.KVs["scope"] != "repository:user/image:pull" {
		t.Fatalf("unexpected: %+v", header)
	}

	if _, err := NewWWWAuthenticate("Bearer"); err == nil {
		t.Fatal("expected error for missing directives")
	}
}

func TestOCIManifestJSON(t *testing.T) {
	manifest := NewOCIManifest(
		OCIManifestConfig{Digest: "sha256:cfg", MediaType: OCIConfigMediaType, Size: 2},
		[]OCIManifestLayer{NewOCIManifestLayer(DiskV2MediaType, 123, "sha256:layer", 456, "sha256:content")},
		789, time.Time{})

	data, err := manifest.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), `{"annotations":{"org.cirruslabs.tart.uncompressed-disk-size":"789"},"config":`) {
		t.Fatalf("keys not sorted: %s", data)
	}

	decoded, err := NewOCIManifestFromJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if size, ok := decoded.Layers[0].UncompressedSize(); !ok || size != 456 {
		t.Fatalf("uncompressedSize: %d %v", size, ok)
	}
	if digest, ok := decoded.UncompressedDiskSize(); !ok || digest != 789 {
		t.Fatalf("uncompressedDiskSize: %d %v", digest, ok)
	}
}

func TestLZ4RoundtripAndZeroSkippingWrite(t *testing.T) {
	payload := bytes.Repeat([]byte("weave-disk-layer"), 100000)
	compressed, err := compressLZ4(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(compressed) >= len(payload) {
		t.Fatalf("no compression: %d >= %d", len(compressed), len(payload))
	}
	decompressed, err := decompressLZ4(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, decompressed) {
		t.Fatal("roundtrip mismatch")
	}

	// zeroSkippingWrite should leave zero chunks unwritten (sparse).
	diskPath := filepath.Join(t.TempDir(), "disk.img")
	disk, err := os.Create(diskPath)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	if err := os.Truncate(diskPath, 3*diskV2HoleGranularityBytes); err != nil {
		t.Fatal(err)
	}

	data := make([]byte, 3*diskV2HoleGranularityBytes)
	copy(data[2*diskV2HoleGranularityBytes:], bytes.Repeat([]byte{0xAB}, diskV2HoleGranularityBytes))
	if _, err := zeroSkippingWrite(disk, nil, 4096, 0, data); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(diskPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("content mismatch")
	}
}
