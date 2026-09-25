package marketwatchv1

// loopInstance runs in its own goroutine after Init completes. It
// subscribes to the bot's stdout stream, sends an initial echo request to
// verify the channel is live, then dispatches incoming messages:
//   - KeyFlagEchoResponse: logs the round-trip echo to confirm connectivity.
//   - KeyFlagMarketStats: parses the snapshot and records it into store
//     (see store.go) -- the only real data this bot mode produces.
// The loop exits and cancels the context if the bot process closes or any
// framing error occurs.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	mgrbot "git.noncepad.com/pkg/bot/catscope"
	"git.noncepad.com/pkg/solpipe-util/logger"
	"github.com/noncepad/catmsg"
)

func loopInstance(
	ctx context.Context,
	cancel context.CancelCauseFunc,
	instance mgrbot.Bot,
	entry *slog.Logger,
	store *Store,
) {
	doneC := ctx.Done()
	entry.With(logger.Loc("loop", 1)).Info("starting loop")
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
	err = instance.CustomStdin(DoEchoRequest("marketwatchv1 online"))
	if err != nil {
		err = fmt.Errorf("send echo request failed: %s", err)
		cancel(err)
		return
	}
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
			case KeyFlagEchoRequest:
				err = errors.New("cannot receive echo request")
			case KeyFlagEchoResponse:
				entry.With(logger.Loc("loop", 2)).Info(fmt.Sprintf("echo response %s", string(x.Value())))
			case KeyFlagMarketStats:
				var snap MarketStatsSnapshot
				var n int
				data := x.Value()
				n, err = snap.Parse(data)
				if err != nil {
					err = fmt.Errorf("failed to parse market stats: %s", err)
				} else if n != len(data) {
					err = fmt.Errorf("mismatch market stats data size: %d vs %d", n, len(data))
				} else {
					store.update(snap)
					err = nil
				}
			default:
				err = fmt.Errorf("bad key %d", key[0])
			}
			if err != nil {
				break out
			}
		case err = <-botErrorC:
			break out
		}
	}
	cancel(err)
}
