//go:build darwin

package network

import (
	"testing"

	"github.com/deploymenttheory/weave/internal/vmconfig"
)

func TestExpandProfile(t *testing.T) {
	tests := []struct {
		name      string
		profile   string
		opts      ProfileOptions
		wantMode  vmconfig.NICMode
		wantCheck func(t *testing.T, nic vmconfig.NICConfig)
	}{
		{
			name:     "nat",
			profile:  "nat",
			wantMode: vmconfig.NICModeNAT,
		},
		{
			name:     "internet-only is softnet default",
			profile:  "internet-only",
			wantMode: vmconfig.NICModeSoftnet,
			wantCheck: func(t *testing.T, nic vmconfig.NICConfig) {
				if nic.SoftnetBlock != "" {
					t.Errorf("internet-only should not block by default, got block=%q", nic.SoftnetBlock)
				}
			},
		},
		{
			name:     "isolated blocks all egress",
			profile:  "isolated",
			wantMode: vmconfig.NICModeSoftnet,
			wantCheck: func(t *testing.T, nic vmconfig.NICConfig) {
				if nic.SoftnetBlock != "0.0.0.0/0" {
					t.Errorf("isolated should block 0.0.0.0/0, got %q", nic.SoftnetBlock)
				}
			},
		},
		{
			name:     "vm-lab is vmnet host mode",
			profile:  "vm-lab",
			wantMode: vmconfig.NICModeVmnet,
			wantCheck: func(t *testing.T, nic vmconfig.NICConfig) {
				if nic.VmnetMode != "host" {
					t.Errorf("vm-lab should use vmnet host mode, got %q", nic.VmnetMode)
				}
			},
		},
		{
			name:     "bridged carries interface",
			profile:  "bridged",
			opts:     ProfileOptions{BridgedInterface: "en0"},
			wantMode: vmconfig.NICModeBridged,
			wantCheck: func(t *testing.T, nic vmconfig.NICConfig) {
				if nic.BridgedInterface != "en0" {
					t.Errorf("bridged should carry interface en0, got %q", nic.BridgedInterface)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nics, err := ExpandProfile(tc.profile, tc.opts)
			if err != nil {
				t.Fatalf("ExpandProfile(%q) error: %v", tc.profile, err)
			}
			if len(nics) == 0 {
				t.Fatalf("ExpandProfile(%q) returned no NICs", tc.profile)
			}
			if !nics[0].IsPrimary {
				t.Errorf("ExpandProfile(%q) first NIC should be primary", tc.profile)
			}
			if nics[0].Mode != tc.wantMode {
				t.Errorf("ExpandProfile(%q) mode = %q, want %q", tc.profile, nics[0].Mode, tc.wantMode)
			}
			if tc.wantCheck != nil {
				tc.wantCheck(t, nics[0])
			}
		})
	}
}

func TestExpandProfileUnknown(t *testing.T) {
	if _, err := ExpandProfile("bogus", ProfileOptions{}); err == nil {
		t.Errorf("ExpandProfile(bogus) expected error, got nil")
	}
}
