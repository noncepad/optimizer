package xstockshealthv1

// message.go defines the stdin wire protocol between the Go brain and the
// WASM bot. Messages are framed as catmsg.FixedPair: a 1-byte key
// followed by a variable-length value. Trimmed to the one key this mode's
// Rust side actually parses (see catscope-rust-bot/src/brain/
// xstockshealthv1/message.rs): everything else testperpv1's message.go
// defines (echo, target allocation, LST APY, Astralane trigger, account
// usage) has no counterpart there and would just fall through to Blank.
//
// Key flags (Go->bot via stdin):
//   KeyFlagWallet (3) -- 64-byte ed25519 child keypair. Was harness-
//                        lifecycle-only when this mode was a pure
//                        watcher; now the real signing key for the
//                        liquidations it fires.
//
// Key flags (bot->Go via stdout):
//   KeyFlagHealthBoard (10) -- the current xStocks obligation health
//                        board, worst (lowest health factor) first. Must
//                        match catscope-rust-bot's src/brain/
//                        xstockshealthv1/message.rs KEY_FLAG_HEALTH_BOARD
//                        exactly. Each entry is a fixed 34 bytes:
//                        account id (u64) + collateral_usd/debt_usd/
//                        health_factor (f64 each) + collateral_ticker_id/
//                        debt_ticker_id (u8 each), all little-endian
//                        where it applies -- see ParseHealthBoard.
//   KeyFlagLiquidationEvent (11) -- one real liquidation attempt's
//                        outcome, pushed right after the Rust side's
//                        Wallet::drain_and_send returns a real
//                        transaction signature. Must match
//                        KEY_FLAG_LIQUIDATION_EVENT in that same
//                        message.rs exactly -- see ParseLiquidationEvent.
//   KeyFlagDexActivity (13) -- recent real DEX price ticks against the
//                        most liquid Orca pool per xStock ticker (see
//                        that repo's xstock_dex_watcher.rs module doc
//                        comment) -- purely observational, a live-
//                        activity signal, not something eligibility/
//                        liquidation logic reads. Each entry is a fixed
//                        18 bytes: ticker_id (u8) + price_usd (f64) + up
//                        (u8, 0/1) + slot (u64), all little-endian where
//                        it applies -- see ParseDexActivity.
//   KeyFlagMarketOverview (14) -- coverage/PnL summary, same cadence as
//                        KeyFlagHealthBoard. 28 bytes: reserves_monitored
//                        (u16) + pools_monitored (u16) + risk_tier_counts
//                        (4 * u64) + arb_detected_usd (f64) +
//                        liquidation_pnl_usd (f64) -- see
//                        ParseMarketOverview. ArbDetectedUSD is a
//                        *detected opportunity size*, not realized
//                        profit -- no trade is executed for it; render it
//                        labeled as such.

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/noncepad/catmsg"
	sgo "github.com/solana-foundation/solana-go/v2"
)

const (
	KeyFlagWallet           uint8 = 3
	KeyFlagHealthBoard      uint8 = 10
	KeyFlagLiquidationEvent uint8 = 11
	KeyFlagDexActivity      uint8 = 13
	KeyFlagMarketOverview   uint8 = 14
)

// healthBoardEntrySize is the wire size of one HealthEntry -- see
// KeyFlagHealthBoard's doc comment above. 8 (account id) + 8*3
// (collateral_usd/debt_usd/health_factor) + 1*2 (collateral_ticker_id/
// debt_ticker_id) = 34.
const healthBoardEntrySize = 34

// tickerTable is the ordered ticker-index lookup HealthEntry's
// CollateralTickerID/DebtTickerID decode against -- must match
// catscope-rust-bot's kamino_xstocks_watcher.rs XSTOCKS_RESERVES +
// XSTOCKS_DEBT_RESERVES chained order exactly (see that file's
// reserve_ticker_id doc comment): 10 xStock collateral reserves, then the
// 3 debt-side reserves.
var tickerTable = [13]string{
	"SPYx", "QQQx", "HOODx", "GOOGLx", "CRCLx", "TSLAx", "NVDAx", "AAPLx", "METAx", "MSTRx",
	"USDC", "cbBTC", "USDG",
}

// tickerName decodes a ticker-index byte off the wire. "?" for the
// 255 "no position found" sentinel or any other out-of-range value --
// same convention catscope-rust-bot's own reserve_ticker uses for an
// unrecognized reserve.
func tickerName(id uint8) string {
	if int(id) >= len(tickerTable) {
		return "?"
	}
	return tickerTable[id]
}

// HealthEntry is one obligation's health snapshot, as pushed by the Rust
// side's KaminoXstocksWatcher::health_board. HealthFactor may be
// +Inf (a real IEEE-754 value on the wire, not a sentinel) for a
// debt-free obligation -- callers must handle that, not assume finite.
// CollateralTicker/DebtTicker are the dominant (largest by USD) deposit/
// borrow reserve's ticker, decoded via tickerName -- not necessarily the
// only asset on that side, see ObligationHealth's own doc comment on the
// Rust side.
type HealthEntry struct {
	AccountID        uint64
	CollateralUSD    float64
	DebtUSD          float64
	HealthFactor     float64
	CollateralTicker string
	DebtTicker       string
}

// ParseHealthBoard decodes a KeyFlagHealthBoard message value into the
// real total tracked count (see that field's Rust-side doc comment on
// message.rs's HealthBoard for the display bug this fixes -- entries
// alone is NOT the tracked count, it's capped at 100) and the worst
// (lowest HealthFactor) entries -- the Rust side already sorts it that
// way, this just decodes in the order received.
func ParseHealthBoard(value []byte) (totalTracked uint64, entries []HealthEntry, err error) {
	if len(value) < 8 {
		return 0, nil, fmt.Errorf("health board value length %d, want at least 8", len(value))
	}
	totalTracked = binary.LittleEndian.Uint64(value[0:8])
	rest := value[8:]
	if len(rest)%healthBoardEntrySize != 0 {
		return 0, nil, fmt.Errorf("health board entries length %d is not a multiple of %d", len(rest), healthBoardEntrySize)
	}
	n := len(rest) / healthBoardEntrySize
	entries = make([]HealthEntry, n)
	for i := 0; i < n; i++ {
		base := i * healthBoardEntrySize
		entries[i] = HealthEntry{
			AccountID:        binary.LittleEndian.Uint64(rest[base : base+8]),
			CollateralUSD:    math.Float64frombits(binary.LittleEndian.Uint64(rest[base+8 : base+16])),
			DebtUSD:          math.Float64frombits(binary.LittleEndian.Uint64(rest[base+16 : base+24])),
			HealthFactor:     math.Float64frombits(binary.LittleEndian.Uint64(rest[base+24 : base+32])),
			CollateralTicker: tickerName(rest[base+32]),
			DebtTicker:       tickerName(rest[base+33]),
		}
	}
	return totalTracked, entries, nil
}

// liquidationEventSize is the wire size of one LiquidationEvent -- see
// KeyFlagLiquidationEvent's doc comment above. Must match Rust's
// LIQUIDATION_EVENT_SIZE exactly: obligation(8) + repay_ticker(8) +
// withdraw_ticker(8) + repay_usd(8) + health_factor_before(8) +
// estimated_bonus_usd(8) + slot(8) + signature(64) + sent_ok(1) = 121.
const liquidationEventSize = 8 + 8 + 8 + 8 + 8 + 8 + 8 + 64 + 1

// LiquidationEvent is one real liquidation attempt's outcome, as pushed
// by the Rust side right after it actually sends the transaction.
// EstimatedBonusUSD is exactly that -- a rough placeholder (see that
// field's Rust-side doc comment on message.rs's LiquidationEvent), not a
// confirmed realized profit; render it labeled as an estimate, not fact.
type LiquidationEvent struct {
	Obligation         uint64
	RepayTicker        string
	WithdrawTicker     string
	RepayUSD           float64
	HealthFactorBefore float64
	EstimatedBonusUSD  float64
	Slot               uint64
	Signature          sgo.Signature
	SentOK             bool
}

// trimTicker strips trailing NUL padding from a fixed 8-byte ticker field.
func trimTicker(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// ParseLiquidationEvent decodes a KeyFlagLiquidationEvent message value.
func ParseLiquidationEvent(value []byte) (LiquidationEvent, error) {
	if len(value) != liquidationEventSize {
		return LiquidationEvent{}, fmt.Errorf("liquidation event value length %d, want %d", len(value), liquidationEventSize)
	}
	var sig sgo.Signature
	copy(sig[:], value[56:120])
	return LiquidationEvent{
		Obligation:         binary.LittleEndian.Uint64(value[0:8]),
		RepayTicker:        trimTicker(value[8:16]),
		WithdrawTicker:     trimTicker(value[16:24]),
		RepayUSD:           math.Float64frombits(binary.LittleEndian.Uint64(value[24:32])),
		HealthFactorBefore: math.Float64frombits(binary.LittleEndian.Uint64(value[32:40])),
		EstimatedBonusUSD:  math.Float64frombits(binary.LittleEndian.Uint64(value[40:48])),
		Slot:               binary.LittleEndian.Uint64(value[48:56]),
		Signature:          sig,
		SentOK:             value[120] == 1,
	}, nil
}

// dexActivityEntrySize is the wire size of one DexTick -- see
// KeyFlagDexActivity's doc comment above.
const dexActivityEntrySize = 1 + 8 + 1 + 8

// DexTick is one real DEX price observation, as pushed by the Rust
// side's XstockDexWatcher. Ticker is decoded via the same tickerName
// table HealthEntry's CollateralTicker/DebtTicker use -- both sides
// index into the identical XSTOCKS_RESERVES+XSTOCKS_DEBT_RESERVES order.
type DexTick struct {
	Ticker   string
	PriceUSD float64
	Up       bool
	Slot     uint64
}

// ParseDexActivity decodes a KeyFlagDexActivity message value into its
// (possibly empty) list of recent ticks, oldest first -- same order the
// Rust side's VecDeque iterates in.
func ParseDexActivity(value []byte) ([]DexTick, error) {
	if len(value)%dexActivityEntrySize != 0 {
		return nil, fmt.Errorf("dex activity value length %d is not a multiple of %d", len(value), dexActivityEntrySize)
	}
	n := len(value) / dexActivityEntrySize
	out := make([]DexTick, n)
	for i := 0; i < n; i++ {
		base := i * dexActivityEntrySize
		out[i] = DexTick{
			Ticker:   tickerName(value[base]),
			PriceUSD: math.Float64frombits(binary.LittleEndian.Uint64(value[base+1 : base+9])),
			Up:       value[base+9] == 1,
			Slot:     binary.LittleEndian.Uint64(value[base+10 : base+18]),
		}
	}
	return out, nil
}

// marketOverviewFixedSize is the fixed-header wire size of
// KeyFlagMarketOverview's value, before the variable ArbByTicker entries
// -- see that key's doc comment above.
const marketOverviewFixedSize = 2 + 2 + 4*8 + 8 + 8 + 8

// marketOverviewTickerEntrySize is the wire size of one ArbByTicker
// entry: ticker_id (u8) + usd (f64).
const marketOverviewTickerEntrySize = 1 + 8

// ArbTickerAmount is one ticker's slice of MarketOverview.ArbDetectedUSD.
type ArbTickerAmount struct {
	Ticker string
	USD    float64
}

// MarketOverview is the coverage/PnL summary pushed alongside HealthBoard
// -- see KeyFlagMarketOverview's doc comment for the honesty split
// between ArbDetectedUSD and LiquidationPnLUSD.
type MarketOverview struct {
	ReservesMonitored uint16
	PoolsMonitored    uint16
	// RiskTierCounts is [eligibleNow, atRisk, watch, safe] -- see the
	// Rust side's KaminoXstocksWatcher::risk_tier_counts doc comment for
	// the exact health-factor boundaries.
	RiskTierCounts    [4]uint64
	ArbDetectedUSD    float64
	LiquidationPnLUSD float64
	// ArbDetectionCount is how many times ArbDetectedUSD has actually
	// been added to -- render "$X across N detected gaps," not a bare
	// dollar figure.
	ArbDetectionCount uint64
	ArbByTicker       []ArbTickerAmount
}

// ParseMarketOverview decodes a KeyFlagMarketOverview message value.
func ParseMarketOverview(value []byte) (MarketOverview, error) {
	if len(value) < marketOverviewFixedSize {
		return MarketOverview{}, fmt.Errorf("market overview value length %d, want at least %d", len(value), marketOverviewFixedSize)
	}
	rest := value[marketOverviewFixedSize:]
	if len(rest)%marketOverviewTickerEntrySize != 0 {
		return MarketOverview{}, fmt.Errorf("market overview ticker entries length %d is not a multiple of %d", len(rest), marketOverviewTickerEntrySize)
	}
	var m MarketOverview
	m.ReservesMonitored = binary.LittleEndian.Uint16(value[0:2])
	m.PoolsMonitored = binary.LittleEndian.Uint16(value[2:4])
	for i := 0; i < 4; i++ {
		base := 4 + i*8
		m.RiskTierCounts[i] = binary.LittleEndian.Uint64(value[base : base+8])
	}
	m.ArbDetectedUSD = math.Float64frombits(binary.LittleEndian.Uint64(value[36:44]))
	m.LiquidationPnLUSD = math.Float64frombits(binary.LittleEndian.Uint64(value[44:52]))
	m.ArbDetectionCount = binary.LittleEndian.Uint64(value[52:60])
	n := len(rest) / marketOverviewTickerEntrySize
	m.ArbByTicker = make([]ArbTickerAmount, n)
	for i := 0; i < n; i++ {
		base := i * marketOverviewTickerEntrySize
		m.ArbByTicker[i] = ArbTickerAmount{
			Ticker: tickerName(rest[base]),
			USD:    math.Float64frombits(binary.LittleEndian.Uint64(rest[base+1 : base+9])),
		}
	}
	return m, nil
}

func DoWallet(key sgo.PrivateKey) catmsg.FixedPair {
	if len(key) != 64 {
		panic(fmt.Errorf("bad key length: %d %d", len(key), 64))
	}
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagWallet}, key[:])
	if err != nil {
		panic(err)
	}
	return x
}
