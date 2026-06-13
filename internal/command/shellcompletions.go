// Port of tart's ShellCompletions/ShellCompletions.swift: VM name listers
// used by the CLI completion machinery.
//go:build darwin

package command

import (
	"strings"

	"github.com/deploymenttheory/weave/internal/vmdirectory"
	"github.com/deploymenttheory/weave/internal/vmstorage"
)

// normalizeName escapes colons, which are misinterpreted by Zsh completion.
func normalizeName(name string) string {
	return strings.ReplaceAll(name, ":", "\\:")
}

// CompleteMachines ports completeMachines(_:_:_:).
func CompleteMachines() []string {
	var names []string

	if localStorage, err := vmstorage.NewVMStorageLocal(); err == nil {
		if entries, err := localStorage.List(); err == nil {
			for _, entry := range entries {
				names = append(names, normalizeName(entry.Name))
			}
		}
	}
	if ociStorage, err := vmstorage.NewVMStorageOCI(); err == nil {
		if entries, err := ociStorage.List(); err == nil {
			for _, entry := range entries {
				names = append(names, normalizeName(entry.Name))
			}
		}
	}
	return names
}

// CompleteLocalMachines ports completeLocalMachines(_:_:_:).
func CompleteLocalMachines() []string {
	var names []string
	if localStorage, err := vmstorage.NewVMStorageLocal(); err == nil {
		if entries, err := localStorage.List(); err == nil {
			for _, entry := range entries {
				names = append(names, normalizeName(entry.Name))
			}
		}
	}
	return names
}

// CompleteRunningMachines ports completeRunningMachines(_:_:_:).
func CompleteRunningMachines() []string {
	var names []string
	if localStorage, err := vmstorage.NewVMStorageLocal(); err == nil {
		if entries, err := localStorage.List(); err == nil {
			for _, entry := range entries {
				if state, err := entry.VMDir.State(); err == nil && state == vmdirectory.VMDirectoryStateRunning {
					names = append(names, normalizeName(entry.Name))
				}
			}
		}
	}
	return names
}
