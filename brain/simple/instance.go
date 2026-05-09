package simple

import (
	"context"
	"log/slog"
	"os"

	mgrbot "git.noncepad.com/pkg/bot/solpipe/bidder/manager/bot"
	"git.noncepad.com/pkg/solpipe-util/logger"
)

func loopInstance(
	ctx context.Context,
	cancel context.CancelFunc,
	instance mgrbot.Bot,
	entry *slog.Logger,
) {
	doneC := ctx.Done()
	entry.With(logger.Loc("loop", 1)).Info("starting instance loop")
	botErrorC := instance.CloseSignal()
	go instance.LogToFile(ctx, os.Stderr, true)
	var err error
out:
	for {
		select {
		case <-doneC:
			break out
		case err = <-botErrorC:
			break out
		}
	}
	entry.With(logger.Loc("loop", 10), "err", err).Info("finished loop")
	cancel()
}
