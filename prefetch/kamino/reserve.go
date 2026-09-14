package kamino

import (
	sgo "github.com/gagliardetto/solana-go"
)

// Kamino Lending reserve account offsets (Anchor, 8-byte discriminator prepended).
const (
	offLendingMarket = 32               // Pubkey
	offMint          = 128              // Pubkey - liquidity mint
	offSupplyVault   = 160              // Pubkey
	offFeeVault      = 192              // Pubkey
	minReserveLen    = offFeeVault + 32 // 224
)

// Reserve is the parsed state of a Kamino Lending reserve account.
type Reserve struct {
	Pubkey        sgo.PublicKey `json:"pubkey"`
	LendingMarket sgo.PublicKey `json:"lending_market"`
	Mint          sgo.PublicKey `json:"mint"`
	SupplyVault   sgo.PublicKey `json:"supply_vault"`
	FeeVault      sgo.PublicKey `json:"fee_vault"`
}

func readPubkey(data []byte, off int) sgo.PublicKey {
	return sgo.PublicKeyFromBytes(data[off : off+32])
}

// parseReserve parses a Kamino Lending reserve account. body must be the
// full account data including the 8-byte Anchor discriminator -- the offset
// constants above are absolute from the start of the account.
func parseReserve(pubkey sgo.PublicKey, body []byte) *Reserve {
	if len(body) < minReserveLen {
		return nil
	}
	return &Reserve{
		Pubkey:        pubkey,
		LendingMarket: readPubkey(body, offLendingMarket),
		Mint:          readPubkey(body, offMint),
		SupplyVault:   readPubkey(body, offSupplyVault),
		FeeVault:      readPubkey(body, offFeeVault),
	}
}
