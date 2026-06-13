// Port of tart's Prunable.swift protocols.
//go:build darwin

package prune

import (
	"time"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
)

// PrunableStorage ports tart's PrunableStorage protocol.
type PrunableStorage interface {
	Prunables() ([]Prunable, error)
}

// Prunable ports tart's Prunable protocol: a resource that the prune command
// can garbage-collect, ordered by access date.
type Prunable interface {
	URL() *foundation.NSURL
	Delete() error
	AccessDate() (time.Time, error)
	// SizeBytes is the size on disk as seen in Finder, including empty blocks.
	SizeBytes() (int, error)
	// AllocatedSizeBytes is the actual size on disk without empty blocks.
	AllocatedSizeBytes() (int, error)
}
