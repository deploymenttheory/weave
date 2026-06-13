// Port of tart's IPSWCache.swift: the ~/.weave/cache/IPSWs storage.
//go:build darwin

package ipsw

import (
	"strings"

	weaveconfig "github.com/deploymenttheory/weave/internal/config"
	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/prune"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
)

// IPSWCache ports tart's IPSWCache class.
type IPSWCache struct {
	BaseURL *foundation.NSURL
}

var _ prune.PrunableStorage = (*IPSWCache)(nil)

// NewIPSWCache ports IPSWCache.init().
func NewIPSWCache() (*IPSWCache, error) {
	config, err := weaveconfig.NewConfig()
	if err != nil {
		return nil, err
	}
	baseURL := config.WeaveCacheDir.URLByAppendingPathComponentIsDirectory(objcutil.NSStr("IPSWs"), true)
	if _, err := foundation.NSFileManagerDefaultManager().
		CreateDirectoryAtURLWithIntermediateDirectoriesAttributesError(baseURL, true, nil); err != nil {
		return nil, err
	}
	return &IPSWCache{BaseURL: baseURL}, nil
}

// LocationFor ports IPSWCache.locationFor(fileName:).
func (c *IPSWCache) LocationFor(fileName string) *foundation.NSURL {
	return c.BaseURL.URLByAppendingPathComponentIsDirectory(objcutil.NSStr(fileName), false)
}

// Prunables ports IPSWCache.prunables(): every *.ipsw file in the cache.
func (c *IPSWCache) Prunables() ([]prune.Prunable, error) {
	entries, err := foundation.NSFileManagerDefaultManager().
		ContentsOfDirectoryAtURLIncludingPropertiesForKeysOptionsError(
			c.BaseURL, objcutil.EmptyNSArray[*foundation.NSString](), 0)
	if err != nil {
		return nil, err
	}

	var prunables []prune.Prunable
	for _, url := range objcutil.NSArrayURLs(entries) {
		if strings.HasSuffix(objcutil.GoStr(url.LastPathComponent()), ".ipsw") {
			prunables = append(prunables, prune.NewPrunableURL(url))
		}
	}
	return prunables, nil
}
