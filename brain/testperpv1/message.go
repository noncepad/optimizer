package testperpv1

// message.go defines the stdin/stdout wire protocol between the Go brain and
// the WASM bot. Messages are framed as catmsg.FixedPair: a 1-byte key followed
// by a variable-length value.
//
// Key flags (Go→bot via stdin):
//   KeyFlagEchoRequest (1) — ping; bot must reply with KeyFlagEchoResponse.
//   KeyFlagWallet (3)      — 64-byte ed25519 child keypair, sent for
//                            harness-lifecycle consistency only; this mode
//                            never signs or sends a transaction with it.
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
// perpfundingv1's Rust side (src/brain/perpfundingv1/message.rs) has no use
// for TxLatency/LatencyReportV1/AddressLookupTable, so unlike arbv1's
// message.go those types and key flags are omitted here entirely rather
// than copied unused.
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
//                            float64.
//
//   KeyFlagLstApy (5)      — real annualized SOL-per-LST staking yield
//                            (fraction, e.g. 0.073 for 7.3%/yr) for one
//                            liquid-staking-token symbol (e.g.
//                            "jitoSOL"), pushed periodically from
//                            optimizer/prefetch/lst-yield.EstimateAPY --
//                            see that package's doc comment and
//                            catscope-rust-bot's
//                            src/brain/leveraged_yield_farming_plan.md's
//                            "Phase 0". Same 24-byte wire shape as
//                            KeyFlagTargetAllocation.
//
//   KeyFlagTriggerTestAstralane (6) — one-shot, independent of any
//                            strategy state: proves the generic
//                            transactionprocessor::batch host import
//                            routed to Astralane specifically -- a real
//                            tip payment to one of Astralane's own tip
//                            wallets (bundler.astralane.Tip()), paired
//                            with a second, deliberately inert
//                            self-transfer, tagged with Astralane's own
//                            bundler code (bundler.astralane.Code()).
//                            Empty value. See catscope-rust-bot's
//                            Wallet::test_send_astralane_tip_batch doc
//                            comment.

import (
	"encoding/binary"
	"fmt"
	"math"

	sgo "github.com/gagliardetto/solana-go"
	"github.com/noncepad/catmsg"
)

const (
	KeyFlagEchoRequest          uint8 = 1
	KeyFlagEchoResponse         uint8 = 2
	KeyFlagWallet               uint8 = 3
	KeyFlagTargetAllocation     uint8 = 4
	KeyFlagLstApy               uint8 = 5
	KeyFlagTriggerTestAstralane uint8 = 6
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

// DoLstApy builds the wire message for a real annualized LST staking-yield
// update (fraction, e.g. 0.073 for 7.3%/yr) for one liquid-staking-token
// symbol. symbol must fit in 16 bytes (jitoSOL/bSOL/mSOL all do).
func DoLstApy(symbol string, stakingApy float64) catmsg.FixedPair {
	if len(symbol) > 16 {
		panic(fmt.Errorf("symbol %q longer than 16 bytes", symbol))
	}
	var buf [24]byte
	copy(buf[:16], symbol)
	binary.LittleEndian.PutUint64(buf[16:], math.Float64bits(stakingApy))
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagLstApy}, buf[:])
	if err != nil {
		panic(err)
	}
	return x
}

// DoTriggerTestAstralane builds the wire message requesting the bot send a
// real tip payment to one of Astralane's tip wallets plus a second,
// deliberately inert self-transfer, tagged for Astralane -- proves the
// generic transactionprocessor::batch host import end-to-end without
// risking a real trading operation on it.
func DoTriggerTestAstralane() catmsg.FixedPair {
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagTriggerTestAstralane}, []byte{})
	if err != nil {
		panic(err)
	}
	return x
}
