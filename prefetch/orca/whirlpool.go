package orca

import (
	"encoding/binary"
	"fmt"
	"math"

	sgo "github.com/gagliardetto/solana-go"
)

// Account layout offsets (from start of account data, including 8-byte discriminator).
const (
	offTickSpacing  = 41
	offFeeRate      = 45
	offSqrtPrice    = 65
	offTickCurrent  = 81
	offMintA        = 101
	offVaultA       = 133
	offMintB        = 181
	offVaultB       = 213
	minWhirlpoolLen = offVaultB + 32 // 245
)

// Whirlpool is the parsed state of an Orca Whirlpool concentrated-liquidity pool.
type Whirlpool struct {
	Pubkey           sgo.PublicKey
	TokenMintA       sgo.PublicKey
	TokenMintB       sgo.PublicKey
	VaultA           sgo.PublicKey
	VaultB           sgo.PublicKey
	SqrtPriceHi      uint64 // high 64 bits of Q64.64 sqrt price
	SqrtPriceLo      uint64 // low 64 bits of Q64.64 sqrt price
	TickCurrentIndex int32
	TickSpacing      uint16
	FeeRate          uint16 // hundredths of a basis point (3000 = 0.3%)
}

// SpotPrice returns token_b raw units per token_a raw unit.
func (w *Whirlpool) SpotPrice() float64 {
	// sqrt_price = SqrtPriceX64 / 2^64 = hi + lo/2^64
	sqrt := float64(w.SqrtPriceHi) + float64(w.SqrtPriceLo)/math.Ldexp(1, 64)
	return sqrt * sqrt
}

// FeeBps returns the fee in basis points.
func (w *Whirlpool) FeeBps() uint16 {
	return w.FeeRate / 100
}

func parseWhirlpool(pubkey sgo.PublicKey, data []byte) (*Whirlpool, error) {
	if len(data) < minWhirlpoolLen {
		return nil, fmt.Errorf("whirlpool data too short: %d < %d", len(data), minWhirlpoolLen)
	}
	lo := binary.LittleEndian.Uint64(data[offSqrtPrice : offSqrtPrice+8])
	hi := binary.LittleEndian.Uint64(data[offSqrtPrice+8 : offSqrtPrice+16])
	return &Whirlpool{
		Pubkey:           pubkey,
		TokenMintA:       sgo.PublicKeyFromBytes(data[offMintA : offMintA+32]),
		TokenMintB:       sgo.PublicKeyFromBytes(data[offMintB : offMintB+32]),
		VaultA:           sgo.PublicKeyFromBytes(data[offVaultA : offVaultA+32]),
		VaultB:           sgo.PublicKeyFromBytes(data[offVaultB : offVaultB+32]),
		SqrtPriceHi:      hi,
		SqrtPriceLo:      lo,
		TickCurrentIndex: int32(binary.LittleEndian.Uint32(data[offTickCurrent : offTickCurrent+4])),
		TickSpacing:      binary.LittleEndian.Uint16(data[offTickSpacing : offTickSpacing+2]),
		FeeRate:          binary.LittleEndian.Uint16(data[offFeeRate : offFeeRate+2]),
	}, nil
}
