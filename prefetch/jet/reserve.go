package jet

import (
	sgo "github.com/gagliardetto/solana-go"
)

// Jet Protocol V1 Reserve account offsets. No public IDL exists for this
// exact deployment, so these were reverse-engineered from a real
// refresh_reserve transaction pulled off mainnet: the bytes at these
// offsets were checked to exactly equal that transaction's own
// market/mint/vault account pubkeys.
const (
	offMarket     = 16
	offMint       = 144
	offVault      = 240
	minReserveLen = offVault + 32 // 272
)

// Reserve is the parsed state of a Jet Protocol V1 Reserve account.
type Reserve struct {
	Pubkey sgo.PublicKey `json:"pubkey"`
	Market sgo.PublicKey `json:"market"`
	Mint   sgo.PublicKey `json:"mint"`
	Vault  sgo.PublicKey `json:"vault"`
}

func readPubkey(data []byte, off int) sgo.PublicKey {
	return sgo.PublicKeyFromBytes(data[off : off+32])
}

// parseReserve parses a Jet Protocol V1 Reserve account. body must be the
// full, untouched account data (no discriminator to strip).
func parseReserve(pubkey sgo.PublicKey, body []byte) *Reserve {
	if len(body) < minReserveLen {
		return nil
	}
	return &Reserve{
		Pubkey: pubkey,
		Market: readPubkey(body, offMarket),
		Mint:   readPubkey(body, offMint),
		Vault:  readPubkey(body, offVault),
	}
}
