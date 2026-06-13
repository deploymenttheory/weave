//go:build darwin

package vmconfig

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/deploymenttheory/weave/internal/objcutil"
)

func TestDeriveMACAddressDeterministic(t *testing.T) {
	primary := "02:00:00:00:00:01"

	first := DeriveMACAddress(primary, 1)
	again := DeriveMACAddress(primary, 1)
	if first != again {
		t.Errorf("DeriveMACAddress not deterministic: %q vs %q", first, again)
	}

	other := DeriveMACAddress(primary, 2)
	if first == other {
		t.Errorf("DeriveMACAddress collided across indices: %q", first)
	}

	// Validate it parses as a real MAC and is locally-administered + unicast.
	octets := strings.Split(first, ":")
	if len(octets) != 6 {
		t.Fatalf("derived MAC %q is not 6 octets", first)
	}
	firstOctet, err := strconv.ParseInt(octets[0], 16, 0)
	if err != nil {
		t.Fatalf("parsing first octet of %q: %v", first, err)
	}
	if firstOctet&0x02 == 0 {
		t.Errorf("derived MAC %q is not locally-administered", first)
	}
	if firstOctet&0x01 != 0 {
		t.Errorf("derived MAC %q is not unicast", first)
	}
}

const minimalLinuxConfig = `{
  "version": 1,
  "os": "linux",
  "arch": "arm64",
  "cpuCountMin": 4,
  "cpuCount": 4,
  "memorySizeMin": 4294967296,
  "memorySize": 4294967296,
  "macAddress": "02:00:00:00:00:01",
  "diskFormat": "raw"
}`

func TestEnsureNICsLegacySynthesis(t *testing.T) {
	config, err := NewVMConfigFromJSON([]byte(minimalLinuxConfig))
	if err != nil {
		t.Fatalf("decode legacy config: %v", err)
	}
	if len(config.NICs) != 0 {
		t.Fatalf("legacy config should have no explicit NICs, got %d", len(config.NICs))
	}

	nics := config.EnsureNICs()
	if len(nics) != 1 {
		t.Fatalf("EnsureNICs should synthesise exactly one NIC, got %d", len(nics))
	}
	if nics[0].Mode != NICModeNAT {
		t.Errorf("synthesised NIC mode = %q, want nat", nics[0].Mode)
	}
	if !nics[0].IsPrimary {
		t.Errorf("synthesised NIC should be primary")
	}
	if got, want := nics[0].MACAddress, objcutil.GoStr(config.MACAddress.String()); got != want {
		t.Errorf("synthesised NIC MAC = %q, want %q", got, want)
	}
}

func TestNICsRoundTrip(t *testing.T) {
	source := `{
  "version": 1,
  "os": "linux",
  "arch": "arm64",
  "cpuCountMin": 4,
  "cpuCount": 4,
  "memorySizeMin": 4294967296,
  "memorySize": 4294967296,
  "macAddress": "02:00:00:00:00:09",
  "diskFormat": "raw",
  "nics": [
    {"mode": "nat", "macAddress": "02:00:00:00:00:01", "isPrimary": true},
    {"mode": "softnet", "macAddress": "02:00:00:00:00:02", "softnetBlock": "0.0.0.0/0"}
  ]
}`

	config, err := NewVMConfigFromJSON([]byte(source))
	if err != nil {
		t.Fatalf("decode config with NICs: %v", err)
	}
	if len(config.NICs) != 2 {
		t.Fatalf("expected 2 NICs, got %d", len(config.NICs))
	}

	// MACAddress mirrors the primary NIC for legacy consumers.
	if got := objcutil.GoStr(config.MACAddress.String()); got != "02:00:00:00:00:01" {
		t.Errorf("config MACAddress = %q, want primary NIC MAC 02:00:00:00:00:01", got)
	}

	// Re-encode and confirm the nics survive.
	encoded, err := config.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("unmarshal encoded: %v", err)
	}
	nics, ok := object["nics"].([]any)
	if !ok || len(nics) != 2 {
		t.Fatalf("encoded config should carry 2 nics, got %v", object["nics"])
	}
}
