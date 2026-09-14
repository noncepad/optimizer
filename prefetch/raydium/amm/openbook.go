package amm

import (
	"encoding/binary"
	"fmt"

	sgo "github.com/gagliardetto/solana-go"
)

// OpenBook/Serum MarketState account layout: 5-byte header padding ("serum"),
// then packed little-endian fields, then 7-byte footer padding. Offsets below
// are absolute from the start of the raw account data. Confirmed against the
// upstream openbook-dex/program MarketState struct field order/sizes.
const (
	offMarketVaultSignerNonce = 45
	offMarketCoinVault        = 117
	offMarketPcVault          = 165
	offMarketEventQ           = 253
	offMarketBids             = 285
	offMarketAsks             = 317
	minOpenBookMarketLen      = offMarketAsks + 32 // 349; footer padding follows but isn't required to be present
)

// OpenBookMarket is the subset of a Serum/OpenBook MarketState account
// needed to build a Raydium AMM v4 swap instruction.
type OpenBookMarket struct {
	Pubkey           sgo.PublicKey
	VaultSignerNonce uint64
	CoinVault        sgo.PublicKey
	PcVault          sgo.PublicKey
	EventQueue       sgo.PublicKey
	Bids             sgo.PublicKey
	Asks             sgo.PublicKey
}

// ParseOpenBookMarket deserialises a raw OpenBook/Serum market account.
func ParseOpenBookMarket(pubkeyID sgo.PublicKey, data []byte) (*OpenBookMarket, error) {
	if len(data) < minOpenBookMarketLen {
		return nil, fmt.Errorf("openbook market data too short: %d < %d", len(data), minOpenBookMarketLen)
	}
	m := &OpenBookMarket{
		Pubkey:           pubkeyID,
		VaultSignerNonce: binary.LittleEndian.Uint64(data[offMarketVaultSignerNonce : offMarketVaultSignerNonce+8]),
		CoinVault:        pubkey(data, offMarketCoinVault),
		PcVault:          pubkey(data, offMarketPcVault),
		EventQueue:       pubkey(data, offMarketEventQ),
		Bids:             pubkey(data, offMarketBids),
		Asks:             pubkey(data, offMarketAsks),
	}
	return m, nil
}

// DeriveVaultSigner recomputes the market's vault-signer PDA. The nonce is
// stored on the market account itself (VaultSignerNonce), so this is a direct
// CreateProgramAddress call rather than a bump search.
func DeriveVaultSigner(market sgo.PublicKey, nonce uint64, marketProgramID sgo.PublicKey) (sgo.PublicKey, error) {
	nonceLE := make([]byte, 8)
	binary.LittleEndian.PutUint64(nonceLE, nonce)
	return sgo.CreateProgramAddress([][]byte{market[:], nonceLE}, marketProgramID)
}
