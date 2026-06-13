// Port of tart's Config.swift: resolves the tart home tree (honouring
// WEAVE_HOME), creates the cache and tmp directories, and garbage-collects
// stale tmp entries. All file-system work goes through NSFileManager.
//go:build darwin

package config

import (
	"strings"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	weavelock "github.com/deploymenttheory/weave/internal/lock"
	"github.com/deploymenttheory/weave/internal/objcutil"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
)

// Config mirrors tart's Config struct.
type Config struct {
	WeaveHomeDir  *foundation.NSURL
	WeaveCacheDir *foundation.NSURL
	WeaveTmpDir   *foundation.NSURL
}

// NewConfig ports Config.init().
func NewConfig() (*Config, error) {
	var weaveHomeDir *foundation.NSURL

	// Resolution order: WEAVE_HOME env var, then the settings file's default
	// storage location, then ~/.weave.
	if customWeaveHome, ok := objcutil.EnvironmentValue("WEAVE_HOME"); ok {
		weaveHomeDir = foundation.NSURLFileURLWithPathIsDirectory(objcutil.NSStr(customWeaveHome), true)
		if err := validateWeaveHome(weaveHomeDir); err != nil {
			return nil, err
		}
	} else if settingsHome, ok := settingsOrWarn().DefaultStoragePath(); ok {
		weaveHomeDir = foundation.NSURLFileURLWithPathIsDirectory(objcutil.NSStr(settingsHome), true)
		if err := validateWeaveHome(weaveHomeDir); err != nil {
			return nil, err
		}
	} else {
		weaveHomeDir = foundation.NSFileManagerDefaultManager().
			HomeDirectoryForCurrentUser().
			URLByAppendingPathComponentIsDirectory(objcutil.NSStr(".weave"), true)
	}

	fileManager := foundation.NSFileManagerDefaultManager()

	weaveCacheDir := weaveHomeDir.URLByAppendingPathComponentIsDirectory(objcutil.NSStr("cache"), true)
	if cacheDir := settingsOrWarn().CacheDir; cacheDir != "" {
		weaveCacheDir = foundation.NSURLFileURLWithPathIsDirectory(objcutil.NSStr(cacheDir), true)
	}
	if _, err := fileManager.CreateDirectoryAtURLWithIntermediateDirectoriesAttributesError(weaveCacheDir, true, nil); err != nil {
		return nil, err
	}

	weaveTmpDir := weaveHomeDir.URLByAppendingPathComponentIsDirectory(objcutil.NSStr("tmp"), true)
	if _, err := fileManager.CreateDirectoryAtURLWithIntermediateDirectoriesAttributesError(weaveTmpDir, true, nil); err != nil {
		return nil, err
	}

	return &Config{
		WeaveHomeDir:  weaveHomeDir,
		WeaveCacheDir: weaveCacheDir,
		WeaveTmpDir:   weaveTmpDir,
	}, nil
}

// GC ports Config.gc(): removes every tmp-directory entry whose flock can be
// acquired — i.e. whose creating process has finished or crashed.
func (c *Config) GC() error {
	fileManager := foundation.NSFileManagerDefaultManager()

	entries, err := fileManager.ContentsOfDirectoryAtURLIncludingPropertiesForKeysOptionsError(
		c.WeaveTmpDir, objcutil.EmptyNSArray[*foundation.NSString](), 0)
	if err != nil {
		return err
	}

	for _, entry := range objcutil.NSArrayURLs(entries) {
		lock, err := weavelock.NewFileLock(entry)
		if err != nil {
			return err
		}

		acquired, err := lock.Trylock()
		if err != nil {
			_ = lock.Close()
			return err
		}
		if !acquired {
			_ = lock.Close()
			continue
		}

		if _, err := fileManager.RemoveItemAtURLError(entry); err != nil {
			_ = lock.Close()
			return err
		}

		if err := lock.Unlock(); err != nil {
			_ = lock.Close()
			return err
		}
		_ = lock.Close()
	}

	return nil
}

// ConfigJSONWritingOptions mirrors Config.jsonEncoder()'s .sortedKeys setting
// for use with NSJSONSerialization wherever tart serialised via JSONEncoder.
func ConfigJSONWritingOptions() foundation.NSJSONWritingOptions {
	return foundation.NSJSONWritingSortedKeys
}

// validateWeaveHome ports Config.validateWeaveHome: walks every path component
// of url from the root down, creating each missing directory one level at a
// time so a clear error names the exact component that cannot be created.
func validateWeaveHome(url *foundation.NSURL) error {
	fileManager := foundation.NSFileManagerDefaultManager()
	components := objcutil.NSArrayStrings(url.PathComponents())

	for i := range components {
		descendingPath := strings.Join(components[:i+1], "/")
		descendingURL := foundation.NSURLFileURLWithPath(objcutil.NSStr(descendingPath))

		if fileManager.FileExistsAtPath(descendingURL.Path()) {
			continue
		}

		if _, err := fileManager.CreateDirectoryAtURLWithIntermediateDirectoriesAttributesError(descendingURL, false, nil); err != nil {
			return weaveerrors.ErrGeneric("WEAVE_HOME is invalid: %s does not exist, yet we can't create it: %v",
				objcutil.GoStr(descendingURL.Path()), err)
		}
	}

	return nil
}
