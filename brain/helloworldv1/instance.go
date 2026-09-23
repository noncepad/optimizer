package helloworldv1

// loopInstance runs in its own goroutine after Init completes.
// It subscribes to the bot's stdout stream, sends an initial echo request to
// verify the channel is live, then dispatches incoming messages:
//   - KeyFlagEchoResponse: logs the round-trip echo to confirm connectivity.
//   - KeyFlagLatencyReportV1: parses and queues a LatencyReportV1 for Evaluate
//     to flush to the latency log file.
//   - KeyFlagCommonAccountUsage: parses and persists the bot's reported
//     account-usage tally into prefetch.db's account_usage table (see
//     optimizer/prefetch/alt), the ranking source cmd/alt.go reads instead
//     of scanning transaction history via RPC.
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
	"git.noncepad.com/pkg/solpipe-util/logger"
	"github.com/noncepad/catmsg"
)

func loopInstance(
	ctx context.Context,
	cancel context.CancelCauseFunc,
	handshake mgrbot.Handshake,
	instance mgrbot.Bot,
	entry *slog.Logger,
	ps *pendingState,
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
				entry.With(logger.Loc("loop", 4)).Info(fmt.Sprintf("_______________)))))))echo response %s", string(x.Value())))
			case KeyFlagLatencyReportV1:
				var lh LatencyReportV1
				var n int
				data := x.Value()
				n, err = lh.Parse(data)
				if err != nil {
					err = fmt.Errorf("failed to parse tx latency report: %s", err)
				} else if n != len(data) {
					err = fmt.Errorf("mismatch tx latency report data size: %d vs %d", n, len(data))
				} else {
					ps.mx.Lock()
					ps.listLatencyReportV1.Append(lh)
					ps.mx.Unlock()
				}
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
