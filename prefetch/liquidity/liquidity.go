// Package liquidity generates router.json for the catscope-rust-bot build.
// It implements prefetch.StaticLoader and writes the configuration consumed by
// build.rs to initialise Router and LiquidityPartitioner at compile time.
package liquidity

import (
	sgo "github.com/gagliardetto/solana-go"
)

// Config holds static parameters for the router and its LiquidityPartitioner.
type Config struct {
	// Lambda is the liquidity-penalty coefficient in the negative-log edge weight.
	Lambda float32 `json:"lambda"`
	// MinClusterLiquidity is the minimum USD pool liquidity for a pool to
	// count toward a token's Tier-2 degree during partitioning.
	MinClusterLiquidity float64 `json:"min_cluster_liquidity"`
	// TokenCount is the initial capacity hint for the router's mint index.
	TokenCount int `json:"token_count"`
	// CoreMints holds the five Tier-1 mint pubkeys in order:
	// USDC, wSOL, JitoSOL, mSOL, USDT.
	CoreMints [5]sgo.PublicKey `json:"core_mints"`
}

// DefaultConfig returns a Config pre-filled with mainnet defaults.
func DefaultConfig() Config {
	return Config{
		Lambda:              0.01,
		MinClusterLiquidity: 50_000.0,
		TokenCount:          5_000,
		CoreMints: [5]sgo.PublicKey{
			sgo.MustPublicKeyFromBase58("EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"), // USDC
			sgo.MustPublicKeyFromBase58("So11111111111111111111111111111111111111112"),     // wSOL
			sgo.MustPublicKeyFromBase58("J1toso1uCk3RLmjorhTtrVwY9HJ7X8V9yYac6Y7kGCPn"), // JitoSOL
			sgo.MustPublicKeyFromBase58("mSoLzYCxHdYgdzU16g5QSh3i5K3z3KZK7ytfqcJm7So"),  // mSOL
			sgo.MustPublicKeyFromBase58("Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB"), // USDT
		},
	}
}

// Liquidity implements prefetch.StaticLoader and writes router.json.
type Liquidity struct {
	cfg Config
}

// Create initialises a Liquidity loader from the supplied Config.
func Create(cfg Config) *Liquidity {
	return &Liquidity{cfg: cfg}
}
