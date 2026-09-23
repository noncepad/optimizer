package testperplatencyv1

// message.go defines the stdin/stdout wire protocol between the Go brain and
// the WASM bot. Messages are framed as catmsg.FixedPair: a 1-byte key followed
// by a variable-length value.
//
// Key flags (Go→bot via stdin):
//   KeyFlagEchoRequest (1) — ping; bot must reply with KeyFlagEchoResponse.
//   KeyFlagWallet (3)      — 64-byte ed25519 child keypair -- unlike
//                            testperpv1 (whose smoke test only ever
//                            confirmed on-chain state, never actually
//                            sent), testperplatencyv1's Rust side DOES use
//                            this to sign every real deposit/withdraw
//                            transaction its evaluate() cycle loop builds.
//
// Key flags (bot→Go via stdout):
//   KeyFlagEchoResponse (2)         — pong reply to an echo request.
//   KeyFlagCommonAccountUsage (200) — shared, cross-strategy: the bot's
//                            most-referenced accounts (see doc comment
//                            below), NOT a per-strategy key -- must match
//                            catscope-rust-bot's src/message.rs
//                            COMMON_KEY_FLAG_ACCOUNT_USAGE exactly and
//                            never collide with a per-strategy key
//                            (every per-strategy scheme in this codebase
//                            stays below 100).
//
// testperplatencyv1's Rust side (src/brain/testperplatencyv1/message.rs) --
// a direct copy of testperpv1's -- has no use for
// TxLatency/LatencyReportV1/AddressLookupTable either, so unlike arbv1's
// message.go those types and key flags are omitted here entirely rather
// than copied unused. This mode's own latency findings go out as
// log_warn! lines (see report_cycle_stats/test_advance on the Rust side),
// not a wire message this file would need a key flag for.
//
//   KeyFlagTargetAllocation (4) — target portfolio allocation (fraction
//                            of total portfolio value, 0.0-1.0, e.g.
//                            0.30 for "target 30% of the portfolio in
//                            this symbol") for one symbol, pushed at
//                            runtime whenever the optimizer recomputes
//                            it. The remainder is implicitly USD/stable
//                            -- no explicit USD row. Rebalancing toward
//                            this target is what realizes profit/loss.
//                            Value is a fixed 24 bytes: a 16-byte
//                            zero-padded symbol (matching the Rust
//                            side's SymbolMintRaw.symbol convention)
//                            followed by an 8-byte little-endian
//                            float64. Unused by testperplatencyv1's own
//                            cycle logic today -- kept for
//                            harness-lifecycle consistency with
//                            testperpv1/perpfundingv1's message.go, same
//                            reasoning as KeyFlagWallet above.

import (
	"encoding/binary"
	"fmt"
	"math"

	sgo "github.com/gagliardetto/solana-go"
	"github.com/noncepad/catmsg"
)

const (
	KeyFlagEchoRequest      uint8 = 1
	KeyFlagEchoResponse     uint8 = 2
	KeyFlagWallet           uint8 = 3
	KeyFlagTargetAllocation uint8 = 4
	// KeyFlagCommonAccountUsage is shared across every brain/* package
	// (not just this one) -- see the doc comment above.
	KeyFlagCommonAccountUsage uint8 = 200
)

func DoEchoRequest(payload string) catmsg.FixedPair {
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagEchoRequest}, []byte(payload))
	if err != nil {
		panic(err)
	}
	return x
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

// DoTargetAllocation builds the wire message for a target portfolio
// allocation update for symbol (fraction of total portfolio value,
// 0.0-1.0). symbol must fit in 16 bytes (matches every currently
// tracked symbol: SOL/BTC/ETH/XRP/BNB/SUI).
func DoTargetAllocation(symbol string, allocationPct float64) catmsg.FixedPair {
	if len(symbol) > 16 {
		panic(fmt.Errorf("symbol %q longer than 16 bytes", symbol))
	}
	var buf [24]byte
	copy(buf[:16], symbol)
	binary.LittleEndian.PutUint64(buf[16:], math.Float64bits(allocationPct))
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagTargetAllocation}, buf[:])
	if err != nil {
		panic(err)
	}
	return x
}
