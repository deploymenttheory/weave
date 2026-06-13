//go:build darwin

package network

import (
	"testing"

	"github.com/deploymenttheory/weave/internal/vmconfig"
)

func TestParseNICDevice(t *testing.T) {
	tests := []struct {
		name string
		spec string
		want vmconfig.NICConfig
	}{
		{
			name: "nat",
			spec: "nat",
			want: vmconfig.NICConfig{Mode: vmconfig.NICModeNAT},
		},
		{
			name: "bridged with interface",
			spec: "bridged:en0",
			want: vmconfig.NICConfig{Mode: vmconfig.NICModeBridged, BridgedInterface: "en0"},
		},
		{
			name: "softnet with block and expose",
			spec: "softnet,block=10.0.0.0/8,expose=2222:22",
			want: vmconfig.NICConfig{
				Mode:          vmconfig.NICModeSoftnet,
				SoftnetBlock:  "10.0.0.0/8",
				SoftnetExpose: "2222:22",
			},
		},
		{
			name: "softnet host bare flag",
			spec: "softnet,host",
			want: vmconfig.NICConfig{Mode: vmconfig.NICModeSoftnet, SoftnetHostMode: true},
		},
		{
			name: "vmnet host mode with subnet",
			spec: "vmnet,mode=host,subnet=192.168.66.1,mask=255.255.255.0,nonat",
			want: vmconfig.NICConfig{
				Mode:        vmconfig.NICModeVmnet,
				VmnetMode:   "host",
				VmnetSubnet: "192.168.66.1",
				VmnetMask:   "255.255.255.0",
				VmnetNoNAT:  true,
			},
		},
		{
			name: "mac and primary modifiers",
			spec: "nat,mac=aa:bb:cc:dd:ee:ff,primary",
			want: vmconfig.NICConfig{
				Mode:       vmconfig.NICModeNAT,
				MACAddress: "aa:bb:cc:dd:ee:ff",
				IsPrimary:  true,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseNICDevice(tc.spec)
			if err != nil {
				t.Fatalf("ParseNICDevice(%q) error: %v", tc.spec, err)
			}
			if got != tc.want {
				t.Errorf("ParseNICDevice(%q) = %+v, want %+v", tc.spec, got, tc.want)
			}
		})
	}
}

func TestParseNICDeviceErrors(t *testing.T) {
	for _, spec := range []string{"", "wifi", "softnet,bogus=1", "vmnet,nope"} {
		if _, err := ParseNICDevice(spec); err == nil {
			t.Errorf("ParseNICDevice(%q) expected error, got nil", spec)
		}
	}
}

func TestParseNICDevicesMarksFirstPrimary(t *testing.T) {
	nics, err := ParseNICDevices([]string{"vmnet,mode=host", "nat"})
	if err != nil {
		t.Fatalf("ParseNICDevices error: %v", err)
	}
	if len(nics) != 2 {
		t.Fatalf("expected 2 NICs, got %d", len(nics))
	}
	if !nics[0].IsPrimary {
		t.Errorf("expected first NIC to be primary")
	}
	if nics[1].IsPrimary {
		t.Errorf("expected second NIC not to be primary")
	}
}

func TestParseNICDevicesRespectsExplicitPrimary(t *testing.T) {
	nics, err := ParseNICDevices([]string{"vmnet,mode=host", "nat,primary"})
	if err != nil {
		t.Fatalf("ParseNICDevices error: %v", err)
	}
	if nics[0].IsPrimary {
		t.Errorf("expected first NIC not to be primary when second is tagged")
	}
	if !nics[1].IsPrimary {
		t.Errorf("expected explicitly tagged second NIC to be primary")
	}
}
