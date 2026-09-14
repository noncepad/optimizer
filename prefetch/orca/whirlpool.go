package orca

import (
	"encoding/binary"
	"fmt"
	"math"

	sgo "github.com/gagliardetto/solana-go"
)

// Account layout offsets (from start of account data, including 8-byte discriminator).
const (
	offWhirlpoolsConfig = 8
	offTickSpacing      = 41
	offFeeRate          = 45
	offLiquidity    = 49 // u128 active liquidity
	offSqrtPrice    = 65
	offTickCurrent  = 81
	offMintA        = 101
	offVaultA       = 133
	offMintB        = 181
	offVaultB       = 213
	minWhirlpoolLen = offVaultB + 32 // 245
)

// WhirlpoolConfig offsets (discriminator included, Anchor-serialised).
const (
	offCfgFeeAuthority                 = 8
	offCfgCollectProtocolFeesAuthority = 40
	offCfgRewardEmissionsSuperAuthority = 72
	offCfgDefaultProtocolFeeRate       = 104
	minWhirlpoolConfigLen              = offCfgDefaultProtocolFeeRate + 2 // 106
)

// WhirlpoolConfig is the parsed state of an Orca WhirlpoolsConfig account.
type WhirlpoolConfig struct {
	Pubkey                       sgo.PublicKey
	FeeAuthority                 sgo.PublicKey
	CollectProtocolFeesAuthority sgo.PublicKey
	RewardEmissionsSuperAuthority sgo.PublicKey
	DefaultProtocolFeeRate       uint16
}

// Whirlpool is the parsed state of an Orca Whirlpool concentrated-liquidity pool.
type Whirlpool struct {
	Pubkey           sgo.PublicKey
	WhirlpoolsConfig sgo.PublicKey
	TokenMintA       sgo.PublicKey
	TokenMintB       sgo.PublicKey
	VaultA           sgo.PublicKey
	VaultB           sgo.PublicKey
	SqrtPriceHi      uint64 // high 64 bits of Q64.64 sqrt price
	SqrtPriceLo      uint64 // low 64 bits of Q64.64 sqrt price
	LiquidityLo      uint64 // low 64 bits of active liquidity u128
	LiquidityHi      uint64 // high 64 bits of active liquidity u128
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

func parseWhirlpoolConfig(pubkey sgo.PublicKey, data []byte) (*WhirlpoolConfig, error) {
	if len(data) < minWhirlpoolConfigLen {
		return nil, fmt.Errorf("whirlpool config data too short: %d < %d", len(data), minWhirlpoolConfigLen)
	}
	return &WhirlpoolConfig{
		Pubkey:                        pubkey,
		FeeAuthority:                  sgo.PublicKeyFromBytes(data[offCfgFeeAuthority : offCfgFeeAuthority+32]),
		CollectProtocolFeesAuthority:  sgo.PublicKeyFromBytes(data[offCfgCollectProtocolFeesAuthority : offCfgCollectProtocolFeesAuthority+32]),
		RewardEmissionsSuperAuthority: sgo.PublicKeyFromBytes(data[offCfgRewardEmissionsSuperAuthority : offCfgRewardEmissionsSuperAuthority+32]),
		DefaultProtocolFeeRate:        binary.LittleEndian.Uint16(data[offCfgDefaultProtocolFeeRate : offCfgDefaultProtocolFeeRate+2]),
	}, nil
}

func parseWhirlpool(pubkey sgo.PublicKey, data []byte) (*Whirlpool, error) {
	if len(data) < minWhirlpoolLen {
		return nil, fmt.Errorf("whirlpool data too short: %d < %d", len(data), minWhirlpoolLen)
	}
	sqrtLo := binary.LittleEndian.Uint64(data[offSqrtPrice : offSqrtPrice+8])
	sqrtHi := binary.LittleEndian.Uint64(data[offSqrtPrice+8 : offSqrtPrice+16])
	liqLo := binary.LittleEndian.Uint64(data[offLiquidity : offLiquidity+8])
	liqHi := binary.LittleEndian.Uint64(data[offLiquidity+8 : offLiquidity+16])
	return &Whirlpool{
		Pubkey:           pubkey,
		WhirlpoolsConfig: sgo.PublicKeyFromBytes(data[offWhirlpoolsConfig : offWhirlpoolsConfig+32]),
		TokenMintA:       sgo.PublicKeyFromBytes(data[offMintA : offMintA+32]),
		TokenMintB:       sgo.PublicKeyFromBytes(data[offMintB : offMintB+32]),
		VaultA:           sgo.PublicKeyFromBytes(data[offVaultA : offVaultA+32]),
		VaultB:           sgo.PublicKeyFromBytes(data[offVaultB : offVaultB+32]),
		SqrtPriceHi:      sqrtHi,
		SqrtPriceLo:      sqrtLo,
		LiquidityLo:      liqLo,
		LiquidityHi:      liqHi,
		TickCurrentIndex: int32(binary.LittleEndian.Uint32(data[offTickCurrent : offTickCurrent+4])),
		TickSpacing:      binary.LittleEndian.Uint16(data[offTickSpacing : offTickSpacing+2]),
		FeeRate:          binary.LittleEndian.Uint16(data[offFeeRate : offFeeRate+2]),
	}, nil
}
