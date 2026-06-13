// Port of tart's Passphrase/PassphraseGenerator.swift. The Sequence/Iterator
// pair becomes a function returning n random words (arc4random_uniform →
// crypto/rand).
//go:build darwin

package passphrase

import (
	"crypto/rand"
	"math/big"
)

// GeneratePassphrase returns wordCount random words from the BIP-39 list
// (Swift: Array(PassphraseGenerator().prefix(wordCount))).
func GeneratePassphrase(wordCount int) []string {
	words := make([]string, 0, wordCount)
	for range wordCount {
		index, err := rand.Int(rand.Reader, big.NewInt(int64(len(passphrases))))
		if err != nil {
			panic(err)
		}
		words = append(words, passphrases[index.Int64()])
	}
	return words
}
