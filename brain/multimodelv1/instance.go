// loopInstance runs in its own goroutine after Init completes.
// It subscribes to the bot's stdout stream and dispatches incoming
// messages:
//   - KeyFlagCommonAccountUsage: parses and persists the bot's reported
//     account-usage tally into prefetch.db's account_usage table (see
//     optimizer/prefetch/alt), same as every other bot mode.
//   - KeyFlagResidualSnapshotReport: persists one mint's real residual/
//     z-score warm-up state (prefetch/multimodel.SaveResidualSnapshot --
//     the residual data itself is opaque, not parsed here; only the
//     mint is extracted, to key the row) so cmd/multimodel.go's
//     pushResidualSnapshots can replay it back at the next boot. See
//     message.go's doc comment and catscope-rust-bot's
//     src/trader/residual_snapshot.rs.
//
// Deliberately does NOT send a DoEchoRequest liveness ping the way every
// other mode's loopInstance does (leveragedloopv1/testperpv1's own
// instance.go): that ping's key flag (1) collides here with a real,
// load-bearing one -- catscope-rust-bot's
// src/brain/multimodelv1/message.rs maps key flag 1 to
// CUSTOM_KEY_FLAG_REPLAY_OPEN_INTENT (Phase 4's still-unwired intent
// replay), whose deserializer hard-errors on a payload that isn't a real
// OpenIntent record, instead of silently falling through to Blank the
// way an unmatched key flag does on every other mode. Sending the
// template's echo ping here would corrupt that parse path. Not fixed by
// renumbering the Rust side (out of scope for this pass, and Phase 4's
// wire protocol is otherwise already load-bearing); the echo ping itself
// is a diagnostic nicety, not required for the "connects, subscribes,
// idles cleanly" milestone this package targets, so it's dropped instead.
// No LST-yield pushing either (leveragedloopv1/testperpv1's own
// pushLstApyEstimates) -- this mode's Rust side has no use for it yet.
// The loop exits and cancels the context if the bot process closes or any
// framing error occurs.
package multimodelv1

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	mgrbot "git.noncepad.com/pkg/bot/catscope"
	"git.noncepad.com/pkg/optimizer/prefetch/alt"
	"git.noncepad.com/pkg/optimizer/prefetch/multimodel"
	"git.noncepad.com/pkg/solpipe-util/logger"
	"github.com/noncepad/catmsg"
)

func loopInstance(
	ctx context.Context,
	cancel context.CancelCauseFunc,
	handshake mgrbot.Handshake,
	instance mgrbot.Bot,
	entry *slog.Logger,
	db *sql.DB,
) {
	bw := mgrbot.NewBotWallet(handshake)
	doneC := ctx.Done()
	entry.With(logger.Loc("loop", 1)).Info(fmt.Sprintf("starting loop with wallet %s", bw.Key().PublicKey()))
	subStdout := instance.StdoutCustom(ctx, func(fp catmsg.FixedPair) bool {
		return true
	})
	defer subStdout.Unsubscribe()
	botErrorC := instance.CloseSignal()
	var err error
out:
	for {
		select {
		case <-doneC:
			break out
		case err = <-subStdout.ErrorC:
			err = fmt.Errorf("sub stdout error: %s", err)
			break out
		case x := <-subStdout.StreamC:
			key := x.Key()
			if len(key) != 1 {
				err = fmt.Errorf("key size wrong: %d vs %d", 1, len(key))
				break out
			}
			switch key[0] {
			case KeyFlagCommonAccountUsage:
				var entries []alt.AccountUsageEntry
				entries, err = alt.ParseAccountUsage(x.Value())
				if err != nil {
					err = fmt.Errorf("failed to parse account usage report: %s", err)
				} else if err = alt.RecordUsage(db, entries); err != nil {
					err = fmt.Errorf("failed to record account usage: %s", err)
				} else {
					entry.With(logger.Loc("loop", 5)).Info(fmt.Sprintf("recorded account usage report: %d account(s)", len(entries)))
				}
			case KeyFlagResidualSnapshotReport:
				payload := x.Value()
				if err = multimodel.SaveResidualSnapshot(db, payload); err != nil {
					err = fmt.Errorf("failed to save residual snapshot: %s", err)
				} else {
					entry.With(logger.Loc("loop", 6)).Info(fmt.Sprintf("saved residual snapshot: %d byte(s)", len(payload)))
				}
			default:
				err = fmt.Errorf("bad key %d", key[0])
			}
		case err = <-botErrorC:
			break out
		}
	}
	cancel(err)
}
