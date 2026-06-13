//go:build darwin

package oci

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBlobSource serves blobs from memory, keyed by digest, in fixed-size
// chunks to exercise the streaming paths.
type fakeBlobSource struct {
	blobs map[string][]byte
}

func (s *fakeBlobSource) PullBlob(ctx context.Context, digest string, rangeStart int64,
	handler func([]byte) error) error {
	blob, ok := s.blobs[digest]
	if !ok {
		return fmt.Errorf("fake blob source: no blob %s", digest)
	}
	const chunkSize = 64 * 1024
	for offset := int(rangeStart); offset < len(blob); offset += chunkSize {
		end := min(offset+chunkSize, len(blob))
		if err := handler(blob[offset:end]); err != nil {
			return err
		}
	}
	return nil
}

func newFakeBlobSource() *fakeBlobSource {
	return &fakeBlobSource{blobs: map[string][]byte{}}
}

// add stores a blob and returns its layer descriptor.
func (s *fakeBlobSource) add(mediaType string, data []byte, annotations map[string]string) OCIManifestLayer {
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	s.blobs[digest] = data
	return OCIManifestLayer{MediaType: mediaType, Digest: digest, Size: len(data), Annotations: annotations}
}

// testLumeConfigJSON mirrors the live-captured config blob's shape.
func testLumeConfigJSON(diskSize uint64) []byte {
	data, _ := json.Marshal(LumeConfigJSON{
		OS: "macOS", CPUCount: 4, MemorySize: 4 << 30, DiskSize: diskSize,
		MACAddress: "aa:21:91:02:cf:fb", Display: "1024x768",
		HardwareModel: "aGFyZHdhcmU=", MachineIdentifier: "ZWNpZA==",
	})
	return data
}

// patternedPart builds a deterministic part with leading/trailing zero runs
// so sparseness logic is exercised.
func patternedPart(seed byte, size int) []byte {
	part := make([]byte, size)
	for i := size / 4; i < size/2; i++ {
		part[i] = seed + byte(i%7)
	}
	return part
}

func destinationIn(t *testing.T) PullDestination {
	t.Helper()
	dir := t.TempDir()
	return PullDestination{
		ConfigPath: filepath.Join(dir, "config.json"),
		DiskPath:   filepath.Join(dir, "disk.img"),
		NvramPath:  filepath.Join(dir, "nvram.bin"),
	}
}

func TestLumeShardedAssembly(t *testing.T) {
	source := newFakeBlobSource()
	partA := patternedPart(1, 256*1024)
	partB := patternedPart(2, 256*1024)
	partC := patternedPart(3, 100*1024) // shorter tail part
	nvram := patternedPart(9, 32*1024)
	diskSize := uint64(len(partA) + len(partB) + len(partC))

	manifest := OCIManifest{Layers: []OCIManifestLayer{
		// Deliberately out of order: ordering must come from part.number.
		source.add(lumeShardedLayerMediaType+";part.number=2;part.total=3", partB,
			map[string]string{imageTitleAnnotation: "disk.img.part.ab"}),
		source.add(OCIConfigMediaType, testLumeConfigJSON(diskSize),
			map[string]string{imageTitleAnnotation: "config.json"}),
		source.add(lumeShardedLayerMediaType+";part.number=3;part.total=3", partC,
			map[string]string{imageTitleAnnotation: "disk.img.part.ac"}),
		source.add(octetStreamMediaType, nvram,
			map[string]string{imageTitleAnnotation: "nvram.bin"}),
		source.add(lumeShardedLayerMediaType+";part.number=1;part.total=3", partA,
			map[string]string{imageTitleAnnotation: "disk.img.part.aa"}),
	}}

	if format := DetectImageFormat(manifest); format != ImageFormatLumeSharded {
		t.Fatalf("detected %s", format)
	}

	destination := destinationIn(t)
	description, err := lumeShardedCodec{}.Pull(t.Context(), source, manifest, destination, 4, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	if description == nil || description.OS != "darwin" || description.CPUCount != 4 ||
		description.MACAddress != "aa:21:91:02:cf:fb" || description.DiskSizeBytes != diskSize {
		t.Fatalf("description: %+v", description)
	}

	disk, err := os.ReadFile(destination.DiskPath)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append(append([]byte(nil), partA...), partB...), partC...)
	if !bytes.Equal(disk, want) {
		t.Fatal("disk reassembly mismatch")
	}

	gotNvram, err := os.ReadFile(destination.NvramPath)
	if err != nil || !bytes.Equal(gotNvram, nvram) {
		t.Fatalf("nvram mismatch (%v)", err)
	}
}

func TestLumeShardedRejectsNonContiguousParts(t *testing.T) {
	source := newFakeBlobSource()
	manifest := OCIManifest{Layers: []OCIManifestLayer{
		source.add(OCIConfigMediaType, testLumeConfigJSON(1024), nil),
		source.add(octetStreamMediaType, []byte("nvram"), map[string]string{imageTitleAnnotation: "nvram.bin"}),
		source.add(lumeShardedLayerMediaType+";part.number=1;part.total=3", []byte("a"), nil),
		source.add(lumeShardedLayerMediaType+";part.number=3;part.total=3", []byte("c"), nil),
	}}
	_, err := lumeShardedCodec{}.Pull(t.Context(), source, manifest, destinationIn(t), 2, nil, false)
	if err == nil || !strings.Contains(err.Error(), "not contiguous") {
		t.Fatalf("expected non-contiguous error, got %v", err)
	}
}

func TestLumeShardedRejectsDigestMismatch(t *testing.T) {
	source := newFakeBlobSource()
	part := source.add(lumeShardedLayerMediaType+";part.number=1;part.total=1", []byte("payload"), nil)
	// Corrupt the stored blob after the digest was computed.
	source.blobs[part.Digest] = []byte("tampered")

	manifest := OCIManifest{Layers: []OCIManifestLayer{
		source.add(OCIConfigMediaType, testLumeConfigJSON(8), nil),
		source.add(octetStreamMediaType, []byte("nvram"), map[string]string{imageTitleAnnotation: "nvram.bin"}),
		part,
	}}
	_, err := lumeShardedCodec{}.Pull(t.Context(), source, manifest, destinationIn(t), 1, nil, false)
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected digest mismatch error, got %v", err)
	}
}

func TestLumeChunkedAssembly(t *testing.T) {
	source := newFakeBlobSource()
	chunkA := patternedPart(4, 200*1024)
	chunkB := patternedPart(5, 150*1024)
	diskSize := uint64(len(chunkA) + len(chunkB))

	gzipped := func(data []byte) []byte {
		var buffer bytes.Buffer
		writer := gzip.NewWriter(&buffer)
		_, _ = writer.Write(data)
		_ = writer.Close()
		return buffer.Bytes()
	}

	descriptorJSON, _ := json.Marshal(LumeOCIConfig{
		OS: "macOS", HardwareModel: "aGFyZHdhcmU=", MachineIdentifier: "ZWNpZA==",
	})
	descriptor := source.add(LumeConfigMediaType, descriptorJSON, nil)

	manifest := OCIManifest{
		Config: OCIManifestConfig{MediaType: descriptor.MediaType, Digest: descriptor.Digest, Size: descriptor.Size},
		Annotations: map[string]string{
			lumeAnnotationCPUCount:      "6",
			lumeAnnotationMemorySize:    "8589934592",
			lumeAnnotationDisplay:       "1280x800",
			lumeAnnotationTotalDiskSize: fmt.Sprintf("%d", diskSize),
		},
		Layers: []OCIManifestLayer{
			source.add(LumeDiskMediaType, gzipped(chunkB), map[string]string{
				lumeAnnotationPartNumber:       "2",
				lumeAnnotationPartOffset:       fmt.Sprintf("%d", len(chunkA)),
				lumeAnnotationUncompressedSize: fmt.Sprintf("%d", len(chunkB)),
			}),
			source.add(LumeDiskMediaType, gzipped(chunkA), map[string]string{
				lumeAnnotationPartNumber:       "1",
				lumeAnnotationPartOffset:       "0",
				lumeAnnotationUncompressedSize: fmt.Sprintf("%d", len(chunkA)),
			}),
			source.add(LumeNvramMediaType, []byte("chunked-nvram"), nil),
		},
	}

	if format := DetectImageFormat(manifest); format != ImageFormatLumeChunked {
		t.Fatalf("detected %s", format)
	}

	destination := destinationIn(t)
	description, err := lumeChunkedCodec{}.Pull(t.Context(), source, manifest, destination, 2, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if description.CPUCount != 6 || description.MemorySizeBytes != 8589934592 ||
		description.Display != "1280x800" || description.DiskSizeBytes != diskSize {
		t.Fatalf("description: %+v", description)
	}

	disk, err := os.ReadFile(destination.DiskPath)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]byte(nil), chunkA...), chunkB...)
	if !bytes.Equal(disk, want) {
		t.Fatal("chunked disk reassembly mismatch")
	}
}

func TestLumeLZ4Assembly(t *testing.T) {
	source := newFakeBlobSource()
	partA := patternedPart(6, 180*1024)
	partB := patternedPart(7, 90*1024)
	diskSize := uint64(len(partA) + len(partB))

	lz4 := func(data []byte) []byte {
		compressed, err := compressLZ4(data)
		if err != nil {
			t.Fatal(err)
		}
		return compressed
	}

	manifest := OCIManifest{Layers: []OCIManifestLayer{
		source.add(OCIConfigMediaType, testLumeConfigJSON(diskSize), nil),
		source.add(lumeLZ4LayerMediaType, lz4(partA), nil),
		source.add(lumeLZ4LayerMediaType, lz4(partB), nil),
		source.add(octetStreamMediaType, []byte("lz4-nvram"), map[string]string{imageTitleAnnotation: "nvram.bin"}),
	}}

	if format := DetectImageFormat(manifest); format != ImageFormatLumeLZ4 {
		t.Fatalf("detected %s", format)
	}

	destination := destinationIn(t)
	description, err := lumeLZ4Codec{}.Pull(t.Context(), source, manifest, destination, 1, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if description.DiskSizeBytes != diskSize {
		t.Fatalf("description: %+v", description)
	}

	disk, err := os.ReadFile(destination.DiskPath)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]byte(nil), partA...), partB...)
	if !bytes.Equal(disk, want) {
		t.Fatal("lz4 disk reassembly mismatch")
	}
}

// TestLumeConfigTranslationFixture pins the translation against the
// live-captured config blob.
func TestLumeConfigTranslationFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/lume-sharded-vanilla-config.json")
	if err != nil {
		t.Fatal(err)
	}
	config := &LumeConfigJSON{}
	if err := json.Unmarshal(data, config); err != nil {
		t.Fatal(err)
	}
	description, err := lumeVMDescriptionFromConfigJSON(config)
	if err != nil {
		t.Fatal(err)
	}
	if description.OS != "darwin" {
		t.Errorf("OS = %q", description.OS)
	}
	if description.CPUCount != 4 || description.MemorySizeBytes != 4294967296 {
		t.Errorf("cpu/memory = %d/%d", description.CPUCount, description.MemorySizeBytes)
	}
	if description.DiskSizeBytes != 42949672960 {
		t.Errorf("diskSize = %d", description.DiskSizeBytes)
	}
	if description.MACAddress != "aa:21:91:02:cf:fb" || description.Display != "1024x768" {
		t.Errorf("mac/display = %q/%q", description.MACAddress, description.Display)
	}
	if description.ECIDBase64 == "" || description.HardwareModelBase64 == "" {
		t.Error("identity payloads must pass through")
	}
}

func TestLumeConfigTranslationDefaultsAndErrors(t *testing.T) {
	description, err := lumeVMDescriptionFromConfigJSON(&LumeConfigJSON{OS: "macOS"})
	if err != nil {
		t.Fatal(err)
	}
	if description.CPUCount != lumeDefaultCPUCount ||
		description.MemorySizeBytes != lumeDefaultMemorySizeBytes ||
		description.Display != lumeShardedDefaultDisplay {
		t.Fatalf("defaults not applied: %+v", description)
	}

	if _, err := lumeVMDescriptionFromConfigJSON(&LumeConfigJSON{OS: "plan9"}); err == nil {
		t.Fatal("expected an error for an unknown guest OS")
	}
}
