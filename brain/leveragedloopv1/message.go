package leveragedloopv1

// message.go defines the stdin/stdout wire protocol between the Go brain and
// the WASM bot. Messages are framed as catmsg.FixedPair: a 1-byte key followed
// by a variable-length value.
//
// Key flags (Go→bot via stdin):
//   KeyFlagEchoRequest (1)  — ping; bot must reply with KeyFlagEchoResponse.
//   KeyFlagWallet (3)       — 64-byte ed25519 child keypair; this mode's
//                             Rust side signs real Kamino transactions
//                             with it.
//   KeyFlagTriggerOpen (4)  — starting notional in USD (8-byte little-
//                             endian float64) for the single loop step.
//                             Ignored bot-side unless the state machine
//                             is currently idle/closed -- see
//                             catscope-rust-bot's
//                             src/brain/leveragedloopv1/message.rs.
//   KeyFlagTriggerOpenAuto (11) — same starting notional as
//                             KeyFlagTriggerOpen, but the LST candidate
//                             is chosen automatically by the bot's own
//                             Time-Expanded DAG instead of always
//                             jitoSOL -- declines (opens nothing) if the
//                             DAG says "do nothing" beats every real
//                             candidate right now. 8-byte little-endian
//                             float64 value. Same idle/closed-only
//                             gating as KeyFlagTriggerOpen. Added
//                             2026-08-28 -- see catscope-rust-bot's
//                             src/brain/leveragedloopv1/message.rs.
//   KeyFlagTriggerClose (5) — requests the deleverage/unwind sequence.
//                             Empty value. Ignored bot-side unless a
//                             position is currently open.
//   KeyFlagTriggerRecoverToken (6) — one-shot recovery action, independent
//                             of the loop's phase: swaps the wallet's
//                             entire real balance of the given mint back
//                             to USDC. 32-byte mint pubkey value.
//                             Originally added 2026-08-27 hardcoded to
//                             mSOL (after a real USDC->mSOL hop landed
//                             but the follow-on hop never completed);
//                             genericized the same day after a separate
//                             bug left a real, unrelated meme token in
//                             the wallet instead -- see catscope-rust-bot's
//                             src/brain/leveragedloopv1/message.rs.
//   KeyFlagTriggerRedepositUsdc (7) — one-shot action, independent of
//                             the loop's phase: swaps this USD notional
//                             of USDC to jitoSOL and deposits it as
//                             additional Kamino collateral. 8-byte
//                             little-endian float64 value. Added
//                             2026-08-27 after a real borrow's automatic
//                             redeposit failed and nothing retried it --
//                             see catscope-rust-bot's
//                             src/brain/leveragedloopv1/message.rs.
//   KeyFlagTriggerTestBundler (8) — one-shot, independent of the loop's
//                             phase: proves the real Astralane
//                             dual-transaction (durable-nonce) bundler
//                             pipeline end-to-end with a trivial, inert
//                             instruction rather than a real trading
//                             operation. Empty value. See
//                             catscope-rust-bot's
//                             Wallet::send_bundler_pair doc comment.
//   KeyFlagTriggerTestBatch (9) — one-shot, independent of the loop's
//                             phase: forces the generic
//                             transactionprocessor::batch host import
//                             (not send_bundler_pair's durable-nonce
//                             pair) with two separate, deliberately
//                             inert self-transfer transactions, tagged
//                             for whichever bundler this build was
//                             compiled with (BUNDLER env var). Empty
//                             value. See catscope-rust-bot's
//                             Wallet::test_send_two_system_transfers doc
//                             comment.
//   KeyFlagLstApy (10)      — real annualized SOL-per-LST exchange-rate
//                             growth (e.g. 0.073 for 7.3%/yr), one
//                             message per tracked LST (see instance.go's
//                             pushLstApyEstimates, lstyield.TrackedLSTs --
//                             37 real candidates as of 2026-08-29), pushed
//                             periodically from
//                             optimizer/prefetch/lst-yield.EstimateAPY.
//                             40-byte value: 32-byte mint pubkey + 8-byte
//                             little-endian float64. Mint-keyed, NOT
//                             symbol-keyed -- changed 2026-08-29 (a full
//                             mint doesn't fit in 16 bytes, and most real
//                             candidates don't have a confidently-known
//                             friendly name); testperpv1/message.go's own
//                             KeyFlagLstApy is a separate, still
//                             symbol-keyed variant, unchanged. See
//                             catscope-rust-bot's
//                             src/brain/leveragedloopv1/message.rs.
//   KeyFlagTriggerEnableBasisTrading (12) — one-time opt-in for the real
//                             Phoenix-perp-funding-vs-Kamino-rate basis
//                             trade, a second strategy fully independent
//                             of the jitoSOL leverage loop above (own,
//                             separate id=1 Kamino obligation). Empty
//                             value. Runs autonomously every real funding
//                             epoch once this has fired -- no manual
//                             trigger needed per cycle, since the
//                             strategy is delta-neutral by construction.
//                             Added 2026-08-29 -- see catscope-rust-bot's
//                             src/brain/leveragedloopv1/message.rs and
//                             state.rs's run_basis_cycle doc comment.
//   KeyFlagTriggerCloseAllBasisPositions (13) — one-shot, independent of
//                             the basis-trade cycle's own logic:
//                             force-closes every currently-open basis
//                             position (both legs, every symbol),
//                             regardless of whether the funding rate
//                             still favors it. Empty value. Manual safety
//                             valve, mirroring KeyFlagTriggerClose's role
//                             for the leverage loop. Added 2026-08-29.
//
// Key flags (bot→Go via stdout):
//   KeyFlagEchoResponse (2)         — pong reply to an echo request.
//   KeyFlagCommonAccountUsage (200) — shared, cross-strategy (see
//                             testperpv1/message.go's identical doc
//                             comment) -- must match catscope-rust-bot's
//                             src/message.rs COMMON_KEY_FLAG_ACCOUNT_USAGE.

import (
	"encoding/binary"
	"fmt"
	"math"

	sgo "github.com/gagliardetto/solana-go"
	"github.com/noncepad/catmsg"
)

const (
	KeyFlagEchoRequest                   uint8 = 1
	KeyFlagEchoResponse                  uint8 = 2
	KeyFlagWallet                        uint8 = 3
	KeyFlagTriggerOpen                   uint8 = 4
	KeyFlagTriggerClose                  uint8 = 5
	KeyFlagTriggerRecoverToken           uint8 = 6
	KeyFlagTriggerRedepositUsdc          uint8 = 7
	KeyFlagTriggerTestBundler            uint8 = 8
	KeyFlagTriggerTestBatch              uint8 = 9
	KeyFlagLstApy                        uint8 = 10
	KeyFlagTriggerOpenAuto               uint8 = 11
	KeyFlagTriggerEnableBasisTrading     uint8 = 12
	KeyFlagTriggerCloseAllBasisPositions uint8 = 13
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

// DoTriggerOpen builds the wire message requesting the bot open a real
// leveraged position, sized at notionalUSD.
func DoTriggerOpen(notionalUSD float64) catmsg.FixedPair {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], math.Float64bits(notionalUSD))
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagTriggerOpen}, buf[:])
	if err != nil {
		panic(err)
	}
	return x
}

// DoTriggerOpenAuto builds the wire message for a real TriggerOpenAuto --
// same notional-USD payload as DoTriggerOpen, but the bot picks the LST
// candidate itself via its own Time-Expanded DAG.
func DoTriggerOpenAuto(notionalUSD float64) catmsg.FixedPair {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], math.Float64bits(notionalUSD))
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagTriggerOpenAuto}, buf[:])
	if err != nil {
		panic(err)
	}
	return x
}

// DoTriggerClose builds the wire message requesting the bot deleverage
// and close its current position.
func DoTriggerClose() catmsg.FixedPair {
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagTriggerClose}, []byte{})
	if err != nil {
		panic(err)
	}
	return x
}

// DoTriggerRecoverToken builds the wire message requesting the bot swap its
// entire real balance of mint back to USDC, independent of the loop's phase.
func DoTriggerRecoverToken(mint sgo.PublicKey) catmsg.FixedPair {
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagTriggerRecoverToken}, mint[:])
	if err != nil {
		panic(err)
	}
	return x
}

// DoTriggerRedepositUsdc builds the wire message requesting the bot swap
// notionalUSD of USDC to jitoSOL and deposit it as additional Kamino
// collateral, independent of the loop's phase.
func DoTriggerRedepositUsdc(notionalUSD float64) catmsg.FixedPair {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], math.Float64bits(notionalUSD))
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagTriggerRedepositUsdc}, buf[:])
	if err != nil {
		panic(err)
	}
	return x
}

// DoTriggerTestBundler builds the wire message requesting the bot send a
// trivial, real Astralane dual-transaction bundle -- proves the pipeline
// end-to-end without risking a real trading operation on it.
func DoTriggerTestBundler() catmsg.FixedPair {
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagTriggerTestBundler}, []byte{})
	if err != nil {
		panic(err)
	}
	return x
}

// DoTriggerTestBatch builds the wire message requesting the bot send two
// separate, real, deliberately inert transactions via the generic
// transactionprocessor::batch host import (not send_bundler_pair's
// durable-nonce pair).
func DoTriggerTestBatch() catmsg.FixedPair {
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagTriggerTestBatch}, []byte{})
	if err != nil {
		panic(err)
	}
	return x
}

// DoTriggerEnableBasisTrading builds the wire message opting into the real
// Phoenix-perp-funding-vs-Kamino-rate basis trade -- a one-time enable, see
// KeyFlagTriggerEnableBasisTrading's doc comment above.
func DoTriggerEnableBasisTrading() catmsg.FixedPair {
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagTriggerEnableBasisTrading}, []byte{})
	if err != nil {
		panic(err)
	}
	return x
}

// DoTriggerCloseAllBasisPositions builds the wire message force-closing
// every currently-open basis-trade position, independent of the cycle's own
// logic -- see KeyFlagTriggerCloseAllBasisPositions's doc comment above.
func DoTriggerCloseAllBasisPositions() catmsg.FixedPair {
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagTriggerCloseAllBasisPositions}, []byte{})
	if err != nil {
		panic(err)
	}
	return x
}

// DoLstApy builds the wire message for a real annualized LST staking-yield
// update (fraction, e.g. 0.073 for 7.3%/yr) for one liquid-staking-token,
// keyed by its real mint pubkey rather than a symbol -- see
// KeyFlagLstApy's doc comment above for why.
func DoLstApy(mint sgo.PublicKey, stakingApy float64) catmsg.FixedPair {
	var buf [40]byte
	copy(buf[:32], mint[:])
	binary.LittleEndian.PutUint64(buf[32:], math.Float64bits(stakingApy))
	var x catmsg.FixedPair
	y := &x
	err := y.From([]byte{KeyFlagLstApy}, buf[:])
	if err != nil {
		panic(err)
	}
	return x
}
