package bundler

// message.go defines the shared, cross-strategy wire message that pushes a
// bundler's live tip state down to whichever bot mode is currently running,
// over the same catmsg.FixedPair/CustomStdin pipe every brain package's own
// per-strategy messages already use (see e.g.
// optimizer/brain/perpfundingv1/message.go's DoTargetAllocation) -- defined
// once here, not duplicated per brain package, since the wire shape and the
// broadcaster loop below are identical regardless of which strategy
// receives it. Mirrors KeyFlagCommonAccountUsage's reserved-namespace
// convention (every brain package's own per-strategy keys stay below 100),
// just for the opposite direction: that one is bot->optimizer, this one is
// optimizer->bot.

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"log/slog"
	"time"

	"git.noncepad.com/pkg/solpipe-util/graph"
	sgo "github.com/gagliardetto/solana-go"
	"github.com/noncepad/catmsg"
)

// KeyFlagBundlerTipUpdate is the shared, cross-strategy stdin key flag
// (Go->bot) carrying a TipUpdate -- must match catscope-rust-bot's
// src/bundler_message.rs COMMON_KEY_FLAG_BUNDLER_TIP_UPDATE exactly, and
// must never collide with any per-strategy key (every per-strategy scheme
// in this codebase stays below 100).
const KeyFlagBundlerTipUpdate uint8 = 201

// TipUpdate is the payload broadcast to a running bot via CustomStdin
// whenever a registered Bundler's tip state changes (see
// RunTipBroadcaster).
type TipUpdate struct {
	// Bundler is the same code Bundler.Code() returns (see
	// BundlerAstralane/BundlerJito).
	Bundler BundlerCode
	// Up is false whenever Tip() (or Distribution()) returned an error --
	// Addresses/Distribution are meaningless in that case and the bot
	// should treat this bundler as unusable until the next Up==true
	// update.
	Up           bool
	Addresses    []sgo.PublicKey
	Distribution [5]graph.Lamports
}

// DoBundlerTipUpdate builds the wire message for a TipUpdate: [bundler
// u8][up u8][n_addrs u16][addr0 32B]...[addrN-1 32B][p25 u64][p50 u64][p75
// u64][p95 u64][p99 u64], all little-endian. Mirrors
// catscope-rust-bot's bundler_message::BundlerTipUpdate::parse exactly.
func DoBundlerTipUpdate(u TipUpdate) catmsg.FixedPair {
	value := make([]byte, 1+1+2+len(u.Addresses)*32+5*8)
	i := 0
	value[i] = u.Bundler
	i++
	if u.Up {
		value[i] = 1
	}
	i++
	binary.LittleEndian.PutUint16(value[i:], uint16(len(u.Addresses)))
	i += 2
	for _, addr := range u.Addresses {
		copy(value[i:i+32], addr[:])
		i += 32
	}
	for _, lamports := range u.Distribution {
		binary.LittleEndian.PutUint64(value[i:], lamports)
		i += 8
	}
	var x catmsg.FixedPair
	y := &x
	if err := y.From([]byte{KeyFlagBundlerTipUpdate}, value); err != nil {
		panic(err)
	}
	return x
}

// tipBroadcastInterval is how often RunTipBroadcaster re-polls every
// registered Bundler. Tip distribution moves with real network congestion,
// so this needs to stay fairly fresh; the tip address list itself rarely
// changes (Wallet::apply_bundler_tip_update on the Rust side already skips
// re-subscribing addresses it already knows), so there's no real cost to
// polling both together on the same short cadence.
const tipBroadcastInterval = 15 * time.Second

// RunTipBroadcaster polls each of bundlers' Tip()/Distribution() every
// tipBroadcastInterval and pushes a TipUpdate to the running bot via send.
// Every successful (Up: true) poll is also persisted to db via
// RecordTipUpdate, so a freshly-started bot doesn't have to wait through
// however long it takes a bundler's own live feed (e.g. Astralane's tip
// websocket) to deliver a first real snapshot: whenever a poll fails
// (Tip() or Distribution() returns an error), the last db-persisted
// TipUpdate for that bundler is sent instead of an immediate Up: false --
// only bundlers that have NEVER produced a real update are marked down.
// db may be nil (persistence/fallback disabled, e.g. in tests).
// Blocks until ctx is cancelled; callers should run it in its own
// goroutine. send failures (e.g. the bot hasn't connected yet) are logged
// and retried on the next tick rather than treated as fatal.
func RunTipBroadcaster(ctx context.Context, entry *slog.Logger, db *sql.DB, bundlers []Bundler, send func(TipUpdate) error) {
	doneC := ctx.Done()
	fallback := func(code BundlerCode) (TipUpdate, bool) {
		if db == nil {
			return TipUpdate{}, false
		}
		update, ok, err := LoadTipUpdate(db, code)
		if err != nil {
			entry.Warn(fmt.Sprintf("bundler %d: failed to load persisted tip update: %s", code, err))
			return TipUpdate{}, false
		}
		return update, ok
	}
	poll := func() {
		for _, b := range bundlers {
			code := b.Code()
			addrs, err := b.Tip()
			if err != nil {
				entry.Warn(fmt.Sprintf("bundler %d: Tip() failed: %s", code, err))
				update, ok := fallback(code)
				if !ok {
					update = TipUpdate{Bundler: code}
					entry.Warn(fmt.Sprintf("bundler %d: no persisted tip update either, marking down", code))
				} else {
					entry.Warn(fmt.Sprintf("bundler %d: falling back to last persisted tip update", code))
				}
				if sErr := send(update); sErr != nil {
					entry.Warn(fmt.Sprintf("bundler %d: failed to send fallback/down update: %s", code, sErr))
				}
				continue
			}
			dist, err := b.Distribution()
			if err != nil {
				entry.Warn(fmt.Sprintf("bundler %d: Distribution() failed: %s", code, err))
				update, ok := fallback(code)
				if !ok {
					update = TipUpdate{Bundler: code}
					entry.Warn(fmt.Sprintf("bundler %d: no persisted tip update either, marking down", code))
				} else {
					entry.Warn(fmt.Sprintf("bundler %d: falling back to last persisted tip update", code))
				}
				if sErr := send(update); sErr != nil {
					entry.Warn(fmt.Sprintf("bundler %d: failed to send fallback/down update: %s", code, sErr))
				}
				continue
			}
			update := TipUpdate{Bundler: code, Up: true, Addresses: addrs, Distribution: dist}
			if db != nil {
				if rErr := RecordTipUpdate(db, update); rErr != nil {
					entry.Warn(fmt.Sprintf("bundler %d: failed to persist tip update: %s", code, rErr))
				}
			}
			if sErr := send(update); sErr != nil {
				entry.Warn(fmt.Sprintf("bundler %d: failed to send tip update: %s", code, sErr))
			}
		}
	}
	poll()
	ticker := time.NewTicker(tipBroadcastInterval)
	defer ticker.Stop()
	for {
		select {
		case <-doneC:
			return
		case <-ticker.C:
			poll()
		}
	}
}
