package leveragedloopv1

// loopInstance runs in its own goroutine after Init completes.
// It subscribes to the bot's stdout stream, sends an initial echo request to
// verify the channel is live, then dispatches incoming messages:
//   - KeyFlagEchoResponse: logs the round-trip echo to confirm connectivity.
//   - KeyFlagCommonAccountUsage: parses and persists the bot's reported
//     account-usage tally into prefetch.db's account_usage table (see
//     optimizer/prefetch/alt), same as every other bot mode.
// It also pushes real LST staking-yield estimates (optimizer/prefetch/
// lst-yield.EstimateAPY, fed by `optimizer watch-lst-yield` -- a separate
// process/command, not run from here) down to the bot on lstApyTicker, once
// at startup and every lstApyPushInterval thereafter -- mirrors
// testperpv1/instance.go's identical mechanism; see message.go's
// KeyFlagLstApy doc comment for what the Rust side does with it.
// The loop exits and cancels the context if the bot process closes or any
// framing error occurs.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	mgrbot "git.noncepad.com/pkg/bot/catscope"
	"git.noncepad.com/pkg/optimizer/prefetch/alt"
	lstyield "git.noncepad.com/pkg/optimizer/prefetch/lst-yield"
	"git.noncepad.com/pkg/solpipe-util/logger"
	"github.com/noncepad/catmsg"
)

// lstApyPushInterval matches watch-lst-yield's own default poll interval --
// no point pushing more often than the underlying exchange-rate timeseries
// actually gets new samples.
const lstApyPushInterval = 1 * time.Hour

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
	select {
	case <-time.After(5 * time.Second):
	case <-doneC:
		return
	}
	botErrorC := instance.CloseSignal()
	var err error
	err = instance.CustomStdin(DoEchoRequest("good bye world!"))
	if err != nil {
		err = fmt.Errorf("send pair failed: %s", err)
		cancel(err)
		return
	}
	pushLstApyEstimates(instance, db, entry)
	lstApyTicker := time.NewTicker(lstApyPushInterval)
	defer lstApyTicker.Stop()
out:
	for {
		select {
		case <-doneC:
			break out
		case <-lstApyTicker.C:
			pushLstApyEstimates(instance, db, entry)
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
			case KeyFlagEchoRequest:
				err = errors.New("cannot receive echo request")
			case KeyFlagEchoResponse:
				entry.With(logger.Loc("loop", 4)).Info(fmt.Sprintf("echo response %s", string(x.Value())))
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
			default:
				err = fmt.Errorf("bad key %d", key[0])
			}
		case err = <-botErrorC:
			break out
		}
	}
	cancel(err)
}

// lstApyEstimateWindow is how far back pushLstApyEstimates looks for a
// rate-of-change estimate -- wide enough to smooth out any single noisy
// snapshot, matching testperpv1/instance.go's identical constant.
const lstApyEstimateWindow = 7 * 24 * time.Hour

// pushLstApyEstimates reads lstyield.EstimateAPY for every real candidate
// LST (lstyield.TrackedLSTs -- 37 as of 2026-08-29, populated by a
// separately-run `optimizer watch-lst-yield`) and pushes whichever ones
// have a real estimate (not nil -- see EstimateAPY's own doc comment on
// why a fresh watch-lst-yield has nothing to report yet) down to the bot
// as a mint-keyed KeyFlagLstApy message each. One bad estimate or send
// never blocks the others. Unlike testperpv1/instance.go's identical-
// looking function (which still uses the narrower, symbol-keyed
// lstyield.KnownLSTs and its own package's symbol-keyed DoLstApy), this
// covers the full real candidate universe the DAG evaluates.
func pushLstApyEstimates(instance mgrbot.Bot, db *sql.DB, entry *slog.Logger) {
	for _, lst := range lstyield.TrackedLSTs {
		apy, err := lstyield.EstimateAPY(db, lst.Mint, lstApyEstimateWindow)
		if err != nil {
			entry.Error(fmt.Sprintf("lst apy estimate: %s: %s", lst.Label(), err))
			continue
		}
		if apy == nil {
			continue
		}
		if err := instance.CustomStdin(DoLstApy(lst.Mint, *apy)); err != nil {
			entry.Error(fmt.Sprintf("lst apy send: %s: %s", lst.Label(), err))
			continue
		}
		entry.Info(fmt.Sprintf("pushed lst apy estimate: %s=%.3f%%", lst.Label(), *apy*100))
	}
}
