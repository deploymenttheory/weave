//go:build darwin

package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deploymenttheory/weave/internal/vmconfig"

	"github.com/deploymenttheory/weave/internal/diskimage"
	"github.com/deploymenttheory/weave/internal/fetcher"
	weavelock "github.com/deploymenttheory/weave/internal/lock"
	"github.com/deploymenttheory/weave/internal/objcutil"
	weaveplatform "github.com/deploymenttheory/weave/internal/platform"
	"github.com/deploymenttheory/weave/internal/prune"
	"github.com/deploymenttheory/weave/internal/vmdirectory"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	virtualization "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/virtualization"
)

func tempFileURL(t *testing.T, contents string) *foundation.NSURL {
	t.Helper()
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return foundation.NSURLFileURLWithPath(objcutil.NSStr(path))
}

func TestResolveBinaryPath(t *testing.T) {
	if url := objcutil.ResolveBinaryPath("diskutil"); url == nil {
		t.Fatal("diskutil not found in PATH")
	} else if got := objcutil.GoStr(url.Path()); !strings.HasSuffix(got, "/diskutil") {
		t.Fatalf("unexpected path %q", got)
	}
	if url := objcutil.ResolveBinaryPath("definitely-not-a-binary-name"); url != nil {
		t.Fatalf("expected nil, got %q", objcutil.GoStr(url.Path()))
	}
}

func TestPrunableURLSizesAndAccessDate(t *testing.T) {
	url := tempFileURL(t, "hello world")
	prunable := prune.NewPrunableURL(url)

	size, err := prunable.SizeBytes()
	if err != nil || size != 11 {
		t.Fatalf("SizeBytes = %d, %v", size, err)
	}
	if allocated, err := prunable.AllocatedSizeBytes(); err != nil || allocated < 0 {
		t.Fatalf("AllocatedSizeBytes = %d, %v", allocated, err)
	}
	if accessDate, err := prunable.AccessDate(); err != nil || accessDate.IsZero() {
		t.Fatalf("AccessDate = %v, %v", accessDate, err)
	}
	if err := prune.URLUpdateAccessDate(url, time.Now()); err != nil {
		t.Fatalf("URLUpdateAccessDate: %v", err)
	}
}

func TestDeduplicatedBytesXattrRoundtrip(t *testing.T) {
	prunable := prune.NewPrunableURL(tempFileURL(t, "x"))

	if got := prunable.DeduplicatedBytes(); got != 0 {
		t.Fatalf("expected 0 before set, got %d", got)
	}
	prunable.SetDeduplicatedBytes(123456789)
	if got := prunable.DeduplicatedBytes(); got != 123456789 {
		t.Fatalf("expected 123456789, got %d", got)
	}
}

func TestFileLockAndPIDLock(t *testing.T) {
	url := tempFileURL(t, "lockme")

	fileLock, err := weavelock.NewFileLock(url)
	if err != nil {
		t.Fatal(err)
	}
	defer fileLock.Close()
	if acquired, err := fileLock.Trylock(); err != nil || !acquired {
		t.Fatalf("Trylock = %v, %v", acquired, err)
	}
	if err := fileLock.Unlock(); err != nil {
		t.Fatal(err)
	}

	pidLock, err := weavelock.NewPIDLock(url)
	if err != nil {
		t.Fatal(err)
	}
	defer pidLock.Close()
	if acquired, err := pidLock.Trylock(); err != nil || !acquired {
		t.Fatalf("Trylock = %v, %v", acquired, err)
	}
	if err := pidLock.Unlock(); err != nil {
		t.Fatal(err)
	}

	if _, err := weavelock.NewPIDLock(foundation.NSURLFileURLWithPath(objcutil.NSStr("/nonexistent/lock"))); err == nil {
		t.Fatal("expected PIDLockMissing error")
	}
}

func TestDiskutilRunReportsFailure(t *testing.T) {
	_, _, err := diskimage.DiskutilRun([]string{"image", "info", "--plist", "/nonexistent/disk.img"})
	if err == nil {
		t.Fatal("expected error for nonexistent image")
	}
	if !strings.Contains(err.Error(), "failed with exit code") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFetcherFetch(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}

	request := foundation.NSURLRequestRequestWithURL(
		foundation.NSURLURLWithString(objcutil.NSStr("https://example.com/")))

	chunks, response, err := fetcher.FetcherFetch(t.Context(), request, false)
	if err != nil {
		t.Fatal(err)
	}
	if code := response.StatusCode(); code != 200 {
		t.Fatalf("status = %d", code)
	}

	var total int
	for chunk := range chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		total += len(chunk.Data)
	}
	if total == 0 {
		t.Fatal("empty body")
	}
	t.Logf("fetched %d bytes", total)
}

func TestVMConfigJSONRoundtrip(t *testing.T) {
	ecid := virtualization.VZMacMachineIdentifierFromID(objcutil.AllocClass("VZMacMachineIdentifier")).Init()
	if ecid == nil {
		t.Fatal("could not create VZMacMachineIdentifier")
	}

	config := vmconfig.NewVMConfig(&vmconfig.LinuxPlatform{}, 2, 2*1024*1024*1024, nil, diskimage.DiskImageFormatRaw)
	originalMAC := objcutil.GoStr(config.MACAddress.String())

	data, err := config.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("json: %s", data)

	decoded, err := vmconfig.NewVMConfigFromJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := objcutil.GoStr(decoded.MACAddress.String()); got != originalMAC {
		t.Fatalf("mac roundtrip: %q != %q", got, originalMAC)
	}
	if decoded.OS != weaveplatform.OSLinux || decoded.CPUCount != 2 || decoded.MemorySize != 2*1024*1024*1024 {
		t.Fatalf("roundtrip mismatch: %+v", decoded)
	}
	if err := decoded.SetCPU(1); err == nil {
		t.Log("SetCPU(1) allowed (minimumAllowedCPUCount <= 1)")
	}
	if err := decoded.SetMemory(1); err == nil {
		t.Fatal("expected LessThanMinimalResourcesError for 1 byte of memory")
	}
}

func TestVMDirectoryLifecycle(t *testing.T) {
	t.Setenv("WEAVE_HOME", filepath.Join(t.TempDir(), "weavehome"))

	dir, err := vmdirectory.VMDirectoryTemporary()
	if err != nil {
		t.Fatal(err)
	}
	if err := dir.Initialize(false); err != nil {
		t.Fatal(err)
	}

	// Create a 1 GB sparse raw disk and the remaining VM files.
	if err := dir.ResizeDisk(1, diskimage.DiskImageFormatRaw); err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{objcutil.GoStr(dir.ConfigURL().Path()), objcutil.GoStr(dir.NvramURL().Path())} {
		if err := os.WriteFile(url, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if !dir.Initialized() {
		t.Fatal("expected initialized")
	}
	state, err := dir.State()
	if err != nil || state != vmdirectory.VMDirectoryStateStopped {
		t.Fatalf("state = %v, %v", state, err)
	}
	size, err := dir.SizeBytes()
	if err != nil || size < 1000*1000*1000 {
		t.Fatalf("SizeBytes = %d, %v", size, err)
	}
	allocated, err := dir.AllocatedSizeBytes()
	if err != nil || allocated > 100*1000*1000 {
		t.Fatalf("AllocatedSizeBytes = %d (expected sparse), %v", allocated, err)
	}

	if err := dir.Delete(); err != nil {
		t.Fatal(err)
	}
	if dir.Initialized() {
		t.Fatal("expected deleted")
	}
}

// TestParseSharedDirectoryShare pins lume's --shared-dir parsing semantics.
func TestParseSharedDirectoryShare(t *testing.T) {
	cases := []struct {
		input    string
		path     string
		readOnly bool
		wantErr  bool
	}{
		{"/tmp/share", "/tmp/share", false, false},
		{"/tmp/share:ro", "/tmp/share", true, false},
		{"/tmp/share:rw", "/tmp/share", false, false},
		{"/tmp/share:bogus", "", false, true},
		{":ro", "", false, true},
	}
	for _, c := range cases {
		share, err := parseSharedDirectoryShare(c.input)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: expected error, got %+v", c.input, share)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error: %v", c.input, err)
			continue
		}
		if share.path != c.path || share.readOnly != c.readOnly {
			t.Errorf("%q: got path=%q readOnly=%v, want path=%q readOnly=%v",
				c.input, share.path, share.readOnly, c.path, c.readOnly)
		}
		if share.name != "" {
			t.Errorf("%q: shared-dir shares must be unnamed, got %q", c.input, share.name)
		}
	}
}
