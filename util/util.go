// Package util contains generic functions.
package util

import (
	"fmt"

	sgo "git.noncepad.com/pkg/solana-go"
)

func ParsePubkey(data []byte) (sgo.PublicKey, error) {
	var pubkey sgo.PublicKey
	if len(data) != len(pubkey[:]) {
		return pubkey, fmt.Errorf("bad data length %d", len(data))
	}
	copy(pubkey[:], data[:])
	return pubkey, nil
}
