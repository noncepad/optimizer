package pumpswap

import (
	"fmt"

	sgo "github.com/gagliardetto/solana-go"
)

// Pool account layout, verified against pump.fun's own published IDL
// (github.com/pump-fun/pump-public-docs, idl/pump_amm.json) -- see
// edge-generator/src/pumpswap.rs for the same offsets, independently
// cross-checked there too (that file predates this package and was built
// first).
const (
	poolMinLen         = 261
	offPoolBaseMint    = 43
	offPoolQuoteMint   = 75
	offPoolBaseVault   = 139
	offPoolQuoteVault  = 171
	offPoolCoinCreator = 211
)

// sha256("account:Pool")[..8] -- verified against pump_amm.json's
// accounts[].discriminator, matches edge-generator/src/pumpswap.rs's
// pool_discriminator() exactly.
var poolDiscriminator = [8]byte{241, 154, 109, 4, 17, 177, 109, 188}

// PDAs derived from the PumpSwap program.
var globalConfigPDA sgo.PublicKey

func init() {
	var err error
	globalConfigPDA, _, err = sgo.FindProgramAddress([][]byte{[]byte("global_config")}, ProgramID)
	if err != nil {
		panic(fmt.Sprintf("failed to derive PumpSwap global_config PDA: %s", err))
	}
}

// Pool holds one PumpSwap pool's parsed state.
type Pool struct {
	Pool         sgo.PublicKey `json:"pool"`
	BaseMint     sgo.PublicKey `json:"base_mint"`
	QuoteMint    sgo.PublicKey `json:"quote_mint"`
	BaseVault    sgo.PublicKey `json:"base_vault"`
	QuoteVault   sgo.PublicKey `json:"quote_vault"`
	CoinCreator  sgo.PublicKey `json:"coin_creator"`
	BaseBalance  uint64        `json:"base_balance"`
	QuoteBalance uint64        `json:"quote_balance"`
}

func readPubkey(data []byte, off int) sgo.PublicKey {
	return sgo.PublicKeyFromBytes(data[off : off+32])
}

// parsePool parses a Pool account's full body (including its 8-byte
// discriminator). Unlike Pump.fun's BondingCurve, both mints and both
// vault pubkeys are stored inline -- no separate discovery step needed.
func parsePool(pool sgo.PublicKey, data []byte) (*Pool, error) {
	if len(data) < poolMinLen {
		return nil, fmt.Errorf("pool account too short: %d bytes", len(data))
	}
	return &Pool{
		Pool:        pool,
		CoinCreator: readPubkey(data, offPoolCoinCreator),
		BaseMint:    readPubkey(data, offPoolBaseMint),
		QuoteMint:   readPubkey(data, offPoolQuoteMint),
		BaseVault:   readPubkey(data, offPoolBaseVault),
		QuoteVault:  readPubkey(data, offPoolQuoteVault),
	}, nil
}
