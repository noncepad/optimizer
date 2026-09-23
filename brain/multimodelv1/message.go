package multimodelv1

// message.go defines the stdin/stdout wire protocol between the Go brain and
// the WASM bot. Messages are framed as catmsg.FixedPair: a 1-byte key followed
// by a variable-length value.
//
// Key flags (Go→bot via stdin):
//   KeyFlagWallet (3)       — 64-byte ed25519 child keypair; this mode's
//                             Rust side subscribes its own wallet
//                             balances with it. Signs/sends no real
//                             transaction yet (sub-phase 5a is idle-only).
//   KeyFlagTriggerEnableFactorLogging (2) — empty payload; one-time
//                             opt-in for Phase 5 sub-phase 5b's real
//                             factor-graph resync/logging cycle (real
//                             structural factors from live pool
//                             liquidity, logged on a periodic cadence --
//                             opens/closes nothing). See
//                             catscope-rust-bot's
//                             src/brain/multimodelv1/message.rs's
//                             identically-named variant and
//                             state.rs's run_factor_resync.
//   KeyFlagTriggerEnablePairTrading (4) — empty payload; one-time opt-in
//                             for Phase 5 sub-phase 5c's REAL, executing
//                             pure-Kamino pair/stat-arb trade -- unlike
//                             KeyFlagTriggerEnableFactorLogging, this one
//                             sends real transactions (real Kamino
//                             deposits/borrows, real spot swaps). See
//                             catscope-rust-bot's
//                             src/brain/multimodelv1/message.rs's
//                             identically-named variant and
//                             state.rs's run_pair_trade_cycle.
//   bundler.KeyFlagBundlerTipUpdate (201) — shared, cross-strategy, not
//                             defined in this file (see bundler/message.go)
//                             -- multimodelv1.go's Hook.SendBundlerTipUpdate
//                             and cmd/multimodel.go's
//                             startBundlerTipBroadcaster wire it up like
//                             every other real bot mode. Confirmed the
//                             Rust side actually consumes this (added
//                             CustomMessageInbound::CommonBundlerTipUpdate
//                             to catscope-rust-bot's
//                             src/brain/multimodelv1/message.rs +
//                             state.rs's on_message) rather than silently
//                             falling through to Blank the way an
//                             unhandled key flag would have.
//   KeyFlagReplayResidualSnapshot (5) — one mint's worth of the Go
//                             -persisted copy of the bot's own last real
//                             residual/z-score warm-up state (see
//                             KeyFlagResidualSnapshotReport below), sent
//                             once per persisted mint (never batched --
//                             see message value below) and pushed back
//                             at boot (cmd/multimodel.go's
//                             pushResidualSnapshots, called immediately
//                             after connect, before
//                             KeyFlagTriggerEnablePairTrading, so it wins
//                             the race against the first real resync --
//                             see catscope-rust-bot's state.rs's
//                             apply_residual_snapshot doc comment for why
//                             a late replay is deliberately discarded
//                             rather than partially applied) so a
//                             restart doesn't need the same multi-minute
//                             climb back to real z-scores every time.
//                             Payload is opaque to this package -- see
//                             catscope-rust-bot's
//                             src/trader/residual_snapshot.rs.
//
// Note the gap at 1: every other bot mode's own message.go reserves
// KeyFlagEchoRequest(1)/KeyFlagEchoResponse(2) here, sent as a stdin
// liveness ping from loopInstance. KeyFlagEchoResponse's own slot (2) is
// now genuinely used above (KeyFlagTriggerEnableFactorLogging) -- the
// echo pair as a whole is still deliberately not defined in this package
// regardless, since key flag 1 was never free here to begin with: see
// instance.go's doc comment, catscope-rust-bot's
// src/brain/multimodelv1/message.rs already uses key flag 1 for
// CUSTOM_KEY_FLAG_REPLAY_OPEN_INTENT (Phase 4), whose deserializer
// hard-errors on a non-OpenIntent payload instead of the usual silent
// fallback-to-Blank an unmatched key flag gets on every other mode.
//
// Key flags (bot→Go via stdout):
//   KeyFlagCommonAccountUsage (200) — shared, cross-strategy (see every
//                             other mode's identical doc comment) --
//                             must match catscope-rust-bot's
//                             src/message.rs COMMON_KEY_FLAG_ACCOUNT_USAGE.
//   KeyFlagResidualSnapshotReport (2) — sent once per mint with real
//                             residual history, per real factor resync
//                             while pair trading is enabled (never
//                             batched across mints -- see
//                             catscope-rust-bot's state.rs's
//                             send_residual_snapshot for why); received
//                             by instance.go's loopInstance and persisted
//                             per-mint (prefetch/multimodel.
//                             SaveResidualSnapshot -- the residual data
//                             itself stays opaque to Go) for
//                             KeyFlagReplayResidualSnapshot above to send
//                             back at the next boot. This value (2) is a
//                             genuinely separate key-flag *namespace*
//                             from the Go→bot flags above --
//                             KeyFlagTriggerEnableFactorLogging also
//                             happens to be 2 on the Go→bot side, but the
//                             two directions never collide (same
//                             reasoning as the existing 1/1 overlap
//                             between CUSTOM_KEY_FLAG_REPLAY_OPEN_INTENT
//                             and CUSTOM_KEY_FLAG_OPEN_INTENT_REPORT,
//                             noted below).
//
// catscope-rust-bot's src/brain/multimodelv1/message.rs also defines
// CUSTOM_KEY_FLAG_OPEN_INTENT_REPORT (1, outbound) for Phase 4's
// OpenIntentReport -- no Go-side receiver exists yet (Phase 4 point 2,
// still unbuilt; see PLAN-1.md), so nothing here decodes it. Nothing
// sends it yet either (sub-phase 5a has no decision logic that would
// ever produce one).

import (
	"fmt"

	sgo "github.com/gagliardetto/solana-go"
	"github.com/noncepad/catmsg"
)

const (
	KeyFlagTriggerEnableFactorLogging uint8 = 2
	KeyFlagWallet                     uint8 = 3
	KeyFlagTriggerEnablePairTrading   uint8 = 4
	// KeyFlagReplayResidualSnapshot (Go→bot) must match catscope-rust-bot's
	// src/brain/multimodelv1/message.rs CUSTOM_KEY_FLAG_REPLAY_RESIDUAL_SNAPSHOT.
	KeyFlagReplayResidualSnapshot uint8 = 5
	// KeyFlagResidualSnapshotReport (bot→Go) must match catscope-rust-bot's
	// CUSTOM_KEY_FLAG_RESIDUAL_SNAPSHOT_REPORT -- a different namespace
	// than the Go→bot flags above, see the doc comment above.
	KeyFlagResidualSnapshotReport uint8 = 2
	// KeyFlagTriggerEnableDirectionalTrading (Go→bot) must match
	// catscope-rust-bot's CUSTOM_KEY_FLAG_TRIGGER_ENABLE_DIRECTIONAL_TRADING
	// -- trade type 1 (directional factor-neutral). Unlike every other
	// trigger in this file, the value is a real 32-byte mint pubkey, not
	// an empty payload: entry itself is human-specified (see
	// catscope-rust-bot's PLAN-1.md directional-neutral design notes for
	// why), so the human's chosen long target has to travel over the
	// wire.
	KeyFlagTriggerEnableDirectionalTrading uint8 = 6
	// KeyFlagTriggerCloseDirectionalPosition (Go→bot) must match
	// catscope-rust-bot's CUSTOM_KEY_FLAG_TRIGGER_CLOSE_DIRECTIONAL_POSITION
	// -- empty payload; explicit human request to close whatever
	// directional position is currently open (or pending open). Trade
	// type 1's stop-loss/borrow-gate closes are automatic; "I'm
	// satisfied, take profit" isn't, since there's no computed
	// take-profit target for a directional bet.
	KeyFlagTriggerCloseDirectionalPosition uint8 = 7
	// KeyFlagTriggerEnableDispersionTrading (Go→bot) must match
	// catscope-rust-bot's CUSTOM_KEY_FLAG_TRIGGER_ENABLE_DISPERSION_TRADING
	// -- trade type 3 (dispersion). Empty payload, unlike trade type 1's
	// enable trigger: entry itself is automated (a real, computed
	// idiosyncratic-volatility signal), this only arms the automated
	// decision loop.
	KeyFlagTriggerEnableDispersionTrading uint8 = 8
	// KeyFlagTriggerCloseDispersionPosition (Go→bot) must match
	// catscope-rust-bot's CUSTOM_KEY_FLAG_TRIGGER_CLOSE_DISPERSION_POSITION
	// -- empty payload; explicit human request to close whatever
	// dispersion position is currently open. The automated exit signal
	// handles ordinary reversion; this is the human override.
	KeyFlagTriggerCloseDispersionPosition uint8 = 9
	// KeyFlagTriggerSweepMint (Go→bot) must match catscope-rust-bot's
	// CUSTOM_KEY_FLAG_TRIGGER_SWEEP_MINT -- temporary, standalone manual
	// cleanup tool (2026-09-03), not tied to any of the four trade
	// types' own gates. Carries two real 32-byte mint pubkeys (source
	// then destination): the bot sweeps its entire real balance of the
	// source mint to the destination mint via the same real
	// execute_spot_leg path every other real send in this mode uses.
	// Destination isn't hardcoded to mint_usdc -- a real, live-confirmed
	// incident found the router had no route to USDC within the normal
	// hop budget for one stranded mint, while a different destination
	// did. See catscope-rust-bot's src/brain/multimodelv1/message.rs's
	// identically-named variant for the real incident (a hop-chain send
	// stranding a real balance in an unsubscribed pass-through
	// intermediate mint) this exists to clean up.
	KeyFlagTriggerSweepMint uint8 = 10
	// KeyFlagTriggerEnableHawkesTrading (Go→bot) must match
	// catscope-rust-bot's CUSTOM_KEY_FLAG_TRIGGER_ENABLE_HAWKES_TRADING
	// -- trade type 5 (Hawkes-on-eigenfactor momentum, see
	// docs/HAWKES_FACTOR_TRADE_PLAN.md). Empty payload, same shape as
	// KeyFlagTriggerEnableDispersionTrading: entry itself is automated
	// (a real, discrete-time self-exciting intensity crossing its own
	// threshold), this only arms the automated decision loop.
	KeyFlagTriggerEnableHawkesTrading uint8 = 11
	// KeyFlagTriggerCloseHawkesPosition (Go→bot) must match
	// catscope-rust-bot's CUSTOM_KEY_FLAG_TRIGGER_CLOSE_HAWKES_POSITION
	// -- empty payload; explicit human request to close whatever Hawkes
	// momentum basket is currently open. The automated exit signal
	// (intensity decay, max-holding-cycles cap, borrow-gate re-check)
	// handles ordinary reversion; this is the human override.
	KeyFlagTriggerCloseHawkesPosition uint8 = 12
	// KeyFlagCommonAccountUsage is shared across every brain/* package
	// (not just this one) -- see the doc comment above.
	KeyFlagCommonAccountUsage uint8 = 200
)

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

func DoTriggerEnableFactorLogging() catmsg.FixedPair {
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagTriggerEnableFactorLogging}, []byte{})
	if err != nil {
		panic(err)
	}
	return x
}

func DoTriggerEnablePairTrading() catmsg.FixedPair {
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagTriggerEnablePairTrading}, []byte{})
	if err != nil {
		panic(err)
	}
	return x
}

// DoTriggerEnableDirectionalTrading wraps the human-specified long target
// (trade type 1's own entry signal, see the KeyFlagTriggerEnableDirectionalTrading
// doc comment above) as a real 32-byte mint pubkey payload.
func DoTriggerEnableDirectionalTrading(mint sgo.PublicKey) catmsg.FixedPair {
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagTriggerEnableDirectionalTrading}, mint[:])
	if err != nil {
		panic(err)
	}
	return x
}

func DoTriggerCloseDirectionalPosition() catmsg.FixedPair {
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagTriggerCloseDirectionalPosition}, []byte{})
	if err != nil {
		panic(err)
	}
	return x
}

func DoTriggerEnableDispersionTrading() catmsg.FixedPair {
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagTriggerEnableDispersionTrading}, []byte{})
	if err != nil {
		panic(err)
	}
	return x
}

func DoTriggerCloseDispersionPosition() catmsg.FixedPair {
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagTriggerCloseDispersionPosition}, []byte{})
	if err != nil {
		panic(err)
	}
	return x
}

// DoTriggerSweepMint wraps the mint to sweep plus its real destination
// mint (temporary manual-cleanup tool, see the KeyFlagTriggerSweepMint
// doc comment above) as a real 64-byte payload -- not hardcoded to USDC,
// since a real, live-confirmed incident found the router had no route to
// USDC within the normal hop budget for one stranded mint, while a
// different destination did have one.
func DoTriggerSweepMint(mint, destMint sgo.PublicKey) catmsg.FixedPair {
	var x catmsg.FixedPair
	y := &x
	value := make([]byte, 0, 64)
	value = append(value, mint[:]...)
	value = append(value, destMint[:]...)
	err := y.From([]byte{KeyFlagTriggerSweepMint}, value)
	if err != nil {
		panic(err)
	}
	return x
}

func DoTriggerEnableHawkesTrading() catmsg.FixedPair {
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagTriggerEnableHawkesTrading}, []byte{})
	if err != nil {
		panic(err)
	}
	return x
}

func DoTriggerCloseHawkesPosition() catmsg.FixedPair {
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagTriggerCloseHawkesPosition}, []byte{})
	if err != nil {
		panic(err)
	}
	return x
}

// DoReplayResidualSnapshot wraps a persisted residual-snapshot payload
// (prefetch/multimodel.LoadResidualSnapshot's return value) for sending
// back to a freshly connected bot. payload is opaque to this package --
// see catscope-rust-bot's src/trader/residual_snapshot.rs.
func DoReplayResidualSnapshot(payload []byte) catmsg.FixedPair {
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagReplayResidualSnapshot}, payload)
	if err != nil {
		panic(err)
	}
	return x
}
