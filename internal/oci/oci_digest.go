// Port of tart's OCI/Digest.swift: SHA-256 content digests in OCI
// "sha256:<hex>" form. CryptoKit becomes crypto/sha256.
//go:build darwin

package oci

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"os"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	"github.com/deploymenttheory/weave/internal/objcutil"
)

var (
	ErrDigestInvalidOffset = errors.New("invalid offset")
	ErrDigestInvalidSize   = errors.New("invalid size")
)

// Digest ports tart's Digest class: an incremental SHA-256 hasher.
type Digest struct {
	hash hash.Hash
}

func NewDigest() *Digest {
	return &Digest{hash: sha256.New()}
}

func (d *Digest) Update(data []byte) {
	_, _ = d.hash.Write(data)
}

func (d *Digest) Finalize() string {
	return "sha256:" + hex.EncodeToString(d.hash.Sum(nil))
}

// DigestHash ports Digest.hash(_ data:).
func DigestHash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// DigestHashURL ports Digest.hash(_ url:).
func DigestHashURL(url *foundation.NSURL) (string, error) {
	file, err := os.Open(objcutil.GoStr(url.Path()))
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}

// DigestHashURLChunk ports Digest.hash(_ url:offset:size:): the digest of a
// size-byte chunk at offset.
func DigestHashURLChunk(url *foundation.NSURL, offset uint64, size uint64) (string, error) {
	file, err := os.Open(objcutil.GoStr(url.Path()))
	if err != nil {
		return "", err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	fileSize := uint64(info.Size())

	if offset > fileSize {
		return "", ErrDigestInvalidOffset
	}
	if offset+size > fileSize {
		return "", ErrDigestInvalidSize
	}

	data := make([]byte, size)
	if _, err := file.ReadAt(data, int64(offset)); err != nil {
		return "", err
	}

	return DigestHash(data), nil
}
