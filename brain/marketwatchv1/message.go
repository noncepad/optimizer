package marketwatchv1

// message.go defines the stdin/stdout wire protocol between this Go brain
// and the WASM bot (catscope-rust-bot/src/brain/marketwatchv1). Unlike
// every trading bot's message.go, there is no KeyFlagWallet here at all
// -- this bot never signs or sends a transaction, so the Rust side has no
// wallet/signing state to receive. Key numbering is scoped to this bot
// mode only (see that module's message.rs doc comment) -- it does not
// need to match arbv1's or any other bot's numbering.
//
// Key flags (Go->bot via stdin):
//   KeyFlagEchoRequest (1)  -- ping; bot must reply with KeyFlagEchoResponse.
//
// Key flags (bot->Go via stdout):
//   KeyFlagEchoResponse (2) -- pong reply to an echo request.
//   KeyFlagMarketStats (3)  -- fixed-size volatility/correlation snapshot,
//                              see MarketStatsSnapshot.Parse below. Layout
//                              must match catscope-rust-bot's
//                              marketwatchv1::message's own
//                              MARKET_STATS_VALUE_SIZE exactly.

import (
	"fmt"
	"math"

	"github.com/noncepad/catmsg"
)

const (
	KeyFlagEchoRequest  uint8 = 1
	KeyFlagEchoResponse uint8 = 2
	KeyFlagMarketStats  uint8 = 3
)

func DoEchoRequest(payload string) catmsg.FixedPair {
	var x catmsg.FixedPair
	if err := x.From([]byte{KeyFlagEchoRequest}, []byte(payload)); err != nil {
		panic(err)
	}
	return x
}

// TrackedSymbols is the fixed, wire-order set of symbols this dashboard
// tracks -- must match catscope-rust-bot's
// trader::market_stats::TRACKED_MINTS exactly (same order, same count).
//
// Real, major, recognizable Solana-ecosystem tokens, each independently
// confirmed (2026-09-25, against a real prefetch.db snapshot) to have
// hundreds to hundreds-of-thousands of real tracked pool rows -- not the
// original curated 6 (BTC/ETH/XRP/BNB/SUI Portal-wrapped, SOL), whose
// non-SOL entries turned out to have zero tracked pool liquidity at all
// in that same snapshot. See trader::market_stats::TRACKED_MINTS's own
// doc comment for the live-verified finding that prompted the swap.
var TrackedSymbols = [6]string{"SOL", "JUP", "JTO", "BONK", "WIF", "RAY"}

// NSymbols and NPairs mirror trader::market_stats::{N_SYMBOLS,N_PAIRS}
// exactly -- N_PAIRS = N*(N-1)/2, the number of distinct unordered symbol
// pairs.
const (
	NSymbols = len(TrackedSymbols)
	NPairs   = NSymbols * (NSymbols - 1) / 2
)

// PairIndex mirrors trader::market_stats::pair_index exactly: standard
// row-major upper-triangle packing for i<j, 0-indexed into TrackedSymbols.
// Panics if i>=j, same contract as the Rust side.
func PairIndex(i, j int) int {
	if i >= j {
		panic(fmt.Errorf("marketwatchv1: PairIndex: i (%d) must be < j (%d)", i, j))
	}
	idx := 0
	for k := 0; k < i; k++ {
		idx += NSymbols - 1 - k
	}
	return idx + (j - i - 1)
}

// marketStatsValueSize is the fixed wire-value length KeyFlagMarketStats
// always carries: 8-byte slot, 8-byte updates-per-second (float64),
// 8-byte lifetime total update count (uint64), then NSymbols prices,
// NSymbols volatilities, NPairs correlations, all little-endian -- must
// match catscope-rust-bot's marketwatchv1::message::MARKET_STATS_VALUE_SIZE
// exactly.
const marketStatsValueSize = 8 + 8 + 8 + NSymbols*8 + NSymbols*8 + NPairs*8

// MarketStatsSnapshot is the decoded form of one KeyFlagMarketStats
// message -- a live volatility/correlation snapshot across TrackedSymbols,
// as of Slot.
type MarketStatsSnapshot struct {
	Slot uint64
	// UpdatesPerSec is this bot's own low-latency account-update
	// throughput, sampled over the real wall-clock time since the
	// previous push (see catscope-rust-bot's
	// trader::market_stats::updates_per_second doc comment) -- proof the
	// live data pipeline itself is fast, independent of any one symbol's
	// price. 0.0 for the bot's very first push (no previous sample to
	// diff against yet).
	UpdatesPerSec float64
	// TotalUpdates is the lifetime count of low-latency account updates
	// this bot has processed since it started.
	TotalUpdates uint64
	// Prices[i] is TrackedSymbols[i]'s last live USD quote, 0.0 if none
	// observed yet.
	Prices [NSymbols]float64
	// Volatilities[i] is TrackedSymbols[i]'s EWMA log-return standard
	// deviation, 0.0 until at least one real return has been observed.
	Volatilities [NSymbols]float64
	// Correlations[PairIndex(i,j)] is the EWMA-implied correlation
	// between TrackedSymbols[i] and TrackedSymbols[j] (i<j), 0.0 until
	// both symbols have real volatility.
	Correlations [NPairs]float64
}

// Correlation returns the correlation between TrackedSymbols[i] and
// TrackedSymbols[j] regardless of argument order (unlike PairIndex, which
// requires i<j) -- 0.0 (not a panic) for i==j, since a symbol's
// correlation with itself isn't tracked data, it's a trivial 1.0 the
// caller should special-case if it wants that convention instead.
func (s *MarketStatsSnapshot) Correlation(i, j int) float64 {
	if i == j {
		return 0.0
	}
	if i > j {
		i, j = j, i
	}
	return s.Correlations[PairIndex(i, j)]
}

// Parse decodes a KeyFlagMarketStats value -- mirrors
// LatencyReportV1.Parse's own shape in optimizer/brain/arbv1's
// message.go. Returns the number of bytes consumed (always
// marketStatsValueSize on success) and an error if value is short.
func (s *MarketStatsSnapshot) Parse(value []byte) (int, error) {
	if len(value) < marketStatsValueSize {
		return 0, fmt.Errorf("market stats value too short: %d < %d", len(value), marketStatsValueSize)
	}
	u64 := func(b []byte) uint64 {
		return uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 |
			uint64(b[4])<<32 | uint64(b[5])<<40 | uint64(b[6])<<48 | uint64(b[7])<<56
	}
	f64 := func(b []byte) float64 {
		return math.Float64frombits(u64(b))
	}
	i := 0
	s.Slot = u64(value[i : i+8])
	i += 8
	s.UpdatesPerSec = f64(value[i : i+8])
	i += 8
	s.TotalUpdates = u64(value[i : i+8])
	i += 8
	for k := 0; k < NSymbols; k++ {
		s.Prices[k] = f64(value[i : i+8])
		i += 8
	}
	for k := 0; k < NSymbols; k++ {
		s.Volatilities[k] = f64(value[i : i+8])
		i += 8
	}
	for k := 0; k < NPairs; k++ {
		s.Correlations[k] = f64(value[i : i+8])
		i += 8
	}
	return i, nil
}
