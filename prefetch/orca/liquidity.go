package orca

import (
	"math"
	"math/big"

	sgo "github.com/gagliardetto/solana-go"
)

// LiquidityUsd returns a rough USD estimate of active liquidity using virtual
// reserves derived from the pool's active liquidity L and sqrt price.
//
// virtualA = L / sqrt_price  (raw units of token A)
// virtualB = L * sqrt_price  (raw units of token B)
//
// prices maps each mint to its USD price per human-readable token unit.
// decimals maps each mint to the number of decimal places (e.g. USDC → 6).
func (w *Whirlpool) LiquidityUsd(prices map[sgo.PublicKey]float64, decimals map[sgo.PublicKey]uint8) float64 {
	priceA, okA := prices[w.TokenMintA]
	priceB, okB := prices[w.TokenMintB]
	if !okA || !okB {
		return 0
	}
	liq := liquidityFloat(w.LiquidityLo, w.LiquidityHi)
	if liq == 0 {
		return 0
	}
	sqrt := float64(w.SqrtPriceHi) + float64(w.SqrtPriceLo)/math.Ldexp(1, 64)
	if sqrt == 0 {
		return 0
	}
	scaleA := math.Pow10(int(decimals[w.TokenMintA]))
	scaleB := math.Pow10(int(decimals[w.TokenMintB]))
	usdA := (liq / sqrt) / scaleA * priceA
	usdB := (liq * sqrt) / scaleB * priceB
	return usdA + usdB
}

// liquidityFloat converts a u128 split into lo/hi uint64 parts to float64.
func liquidityFloat(lo, hi uint64) float64 {
	if hi == 0 {
		return float64(lo)
	}
	b := new(big.Int).SetUint64(hi)
	b.Lsh(b, 64)
	b.Or(b, new(big.Int).SetUint64(lo))
	f, _ := new(big.Float).SetInt(b).Float64()
	return f
}
