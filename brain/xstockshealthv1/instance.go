package xstockshealthv1

// loopInstance runs in its own goroutine after Init completes. Much
// shorter than testperpv1's own: this mode's Rust side
// (catscope-rust-bot/src/brain/xstockshealthv1) sends exactly one real
// outbound message, KeyFlagHealthBoard -- no LST-APY/account-usage push
// loop to run here. Just parse that one message into board and wait for
// the bot process to close or the context to cancel.

import (
	"context"
	"fmt"
	"log/slog"

	mgrbot "git.noncepad.com/pkg/bot/catscope"
	"git.noncepad.com/pkg/solpipe-util/logger"
	"github.com/noncepad/catmsg"
)

func loopInstance(
	ctx context.Context,
	cancel context.CancelCauseFunc,
	handshake mgrbot.Handshake,
	instance mgrbot.Bot,
	entry *slog.Logger,
	board *boardStore,
	liquidations *liquidationStore,
	dex *dexStore,
	overview *overviewStore,
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
				entry.With(logger.Loc("loop", 2)).Info(fmt.Sprintf("bad stdout key size: %d", len(key)))
				continue
			}
			switch key[0] {
			case KeyFlagHealthBoard:
				totalTracked, entries, parseErr := ParseHealthBoard(x.Value())
				if parseErr != nil {
					entry.With(logger.Loc("loop", 3)).Error(fmt.Sprintf("failed to parse health board: %s", parseErr))
					continue
				}
				board.set(totalTracked, entries)
			case KeyFlagLiquidationEvent:
				ev, parseErr := ParseLiquidationEvent(x.Value())
				if parseErr != nil {
					entry.With(logger.Loc("loop", 5)).Error(fmt.Sprintf("failed to parse liquidation event: %s", parseErr))
					continue
				}
				entry.With(logger.Loc("loop", 6)).Warn(fmt.Sprintf(
					"liquidation attempt: obligation=%d repay=%s->%.2f seize=%s sentOK=%t sig=%s",
					ev.Obligation, ev.RepayTicker, ev.RepayUSD, ev.WithdrawTicker, ev.SentOK, ev.Signature,
				))
				liquidations.add(ev)
			case KeyFlagDexActivity:
				ticks, parseErr := ParseDexActivity(x.Value())
				if parseErr != nil {
					entry.With(logger.Loc("loop", 7)).Error(fmt.Sprintf("failed to parse dex activity: %s", parseErr))
					continue
				}
				dex.add(ticks)
			case KeyFlagMarketOverview:
				m, parseErr := ParseMarketOverview(x.Value())
				if parseErr != nil {
					entry.With(logger.Loc("loop", 8)).Error(fmt.Sprintf("failed to parse market overview: %s", parseErr))
					continue
				}
				overview.set(m)
			default:
				entry.With(logger.Loc("loop", 4)).Info(fmt.Sprintf("unexpected stdout message: key %d", key[0]))
			}
		case err = <-botErrorC:
			break out
		}
	}
	cancel(err)
}
