package drift

import (
	sgo "github.com/gagliardetto/solana-go"
)

// Drift v2 SpotMarket account offsets (Anchor, zero_copy, 8-byte
// discriminator prepended). Offsets are absolute from the start of the
// account, verified empirically against a live mainnet account: pubkey@8
// (the struct's own self-referential `pubkey` field) was checked to equal
// the account's own address, and mint@72/vault@104 were checked to resolve
// to a real SPL Token mint and token account respectively.
const (
	offMint      = 72
	offVault     = 104
	minMarketLen = offVault + 32 // 136
)

// SpotMarket is the parsed state of a Drift v2 SpotMarket account.
type SpotMarket struct {
	Pubkey sgo.PublicKey `json:"pubkey"`
	Mint   sgo.PublicKey `json:"mint"`
	Vault  sgo.PublicKey `json:"vault"`
}

func readPubkey(data []byte, off int) sgo.PublicKey {
	return sgo.PublicKeyFromBytes(data[off : off+32])
}

// parseSpotMarket parses a Drift v2 SpotMarket account. body must be the
// full account data including the 8-byte Anchor discriminator -- the
// offset constants above are absolute from the start of the account.
func parseSpotMarket(pubkey sgo.PublicKey, body []byte) *SpotMarket {
	if len(body) < minMarketLen {
		return nil
	}
	return &SpotMarket{
		Pubkey: pubkey,
		Mint:   readPubkey(body, offMint),
		Vault:  readPubkey(body, offVault),
	}
}
