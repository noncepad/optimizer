package astralane

import (
	"context"
	"fmt"
	"log/slog"

	"git.noncepad.com/pkg/solpipe-util/graph"
)

type internal struct {
	ctx          context.Context
	signalCList  []chan<- error
	logger       *slog.Logger
	distribution [5]graph.Lamports
	// haveSnapshot is true once at least one real TipSnapshot has been
	// applied -- lets Distribution() distinguish "no data received yet"
	// from a real all-zero snapshot, instead of silently handing back
	// phantom zero values as if they were live data.
	haveSnapshot bool
}

// applySnapshot updates distribution from a real TipSnapshot -- called via
// internalC from runTipStream (astralane.go) whenever a new one arrives
// over the live websocket.
func (in *internal) applySnapshot(s TipSnapshot) {
	in.distribution = [5]graph.Lamports{
		graph.Lamports(s.LandedTips25thPercentile),
		graph.Lamports(s.LandedTips50thPercentile),
		graph.Lamports(s.LandedTips75thPercentile),
		graph.Lamports(s.LandedTips95thPercentile),
		graph.Lamports(s.LandedTips99thPercentile),
	}
	in.haveSnapshot = true
}

func loopInternal(
	ctx context.Context,
	cancel context.CancelCauseFunc,
	entry *slog.Logger,
	internalC <-chan func(*internal),
) {
	doneC := ctx.Done()

	in := new(internal)
	in.ctx = ctx
	in.signalCList = make([]chan<- error, 0)
	in.logger = entry
	var err error

out:
	for {
		select {
		case <-doneC:
			break out
		case req := <-internalC:
			req(in)
		}
	}
	in.finish(err)
	cancel(err)
}

func (in *internal) finish(err error) {
	in.logger.Info(fmt.Sprintf("astralane internal exit: %+v", err))
	for _, errorC := range in.signalCList {
		errorC <- err
	}
}
