package helloworldv1

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
	handshake mgrbot.Handshake,
	instance mgrbot.Bot,
	entry *slog.Logger,
	ps *pendingState,
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
			default:
				err = fmt.Errorf("bad key %d", key[0])
			}
		case err = <-botErrorC:
			break out
		}
	}
	cancel(err)
}
