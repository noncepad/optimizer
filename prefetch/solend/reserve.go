package solend

import (
	sgo "github.com/gagliardetto/solana-go"
)

// Solend Reserve/LendingMarket account offsets. Unlike the Anchor programs
// elsewhere in this codebase, Solend accounts have no 8-byte discriminator
// -- offsets are absolute from byte 0. Verified empirically against a real
// mainnet obligation and its 3 referenced reserves/lending market pulled
// directly out of a live transaction: lending_market@10 matches the
// market's own pubkey, mint@42 resolves to a real SPL Token mint, and
// supply_vault@75 resolves to a real SPL Token account.
const (
	offLendingMarket = 10
	offMint          = 42
	offSupplyVault   = 75
	minReserveLen    = offSupplyVault + 32 // 107
)

// Reserve is the parsed state of a Solend Reserve account.
type Reserve struct {
	Pubkey        sgo.PublicKey `json:"pubkey"`
	LendingMarket sgo.PublicKey `json:"lending_market"`
	Mint          sgo.PublicKey `json:"mint"`
	SupplyVault   sgo.PublicKey `json:"supply_vault"`
}

func readPubkey(data []byte, off int) sgo.PublicKey {
	return sgo.PublicKeyFromBytes(data[off : off+32])
}

// parseReserve parses a Solend Reserve account. body must be the full,
// untouched account data (no discriminator to strip).
func parseReserve(pubkey sgo.PublicKey, body []byte) *Reserve {
	if len(body) < minReserveLen {
		return nil
	}
	return &Reserve{
		Pubkey:        pubkey,
		LendingMarket: readPubkey(body, offLendingMarket),
		Mint:          readPubkey(body, offMint),
		SupplyVault:   readPubkey(body, offSupplyVault),
	}
}
