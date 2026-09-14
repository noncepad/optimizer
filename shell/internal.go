package shell

import (
	"context"
	"log/slog"
	"net"
)

type internal struct {
	ctx         context.Context
	listSignalC []chan<- error
	sessionC    chan<- sessionUpdate
	logger      *slog.Logger
	clientIndex int
	mClient     map[int]*clientInfo
}

func loopInternal(
	ctx context.Context,
	cancel context.CancelCauseFunc,
	internalC <-chan func(*internal),
	listenErrorC <-chan error,
	connC <-chan *clientInfo,
	entry *slog.Logger,
	listener net.Listener,
) {
	doneC := ctx.Done()
	sessionC := make(chan sessionUpdate, 10)
	in := new(internal)
	in.ctx = ctx
	in.sessionC = sessionC
	in.listSignalC = make([]chan<- error, 0)
	in.logger = entry
	in.clientIndex = 1
	in.mClient = make(map[int]*clientInfo)
	var err error

out:
	for {
		select {
		case <-doneC:
			break out
		case err = <-listenErrorC:
			break out
		case s := <-sessionC:
			in.onSession(s)
		case conn := <-connC:
			in.onConn(conn)
		case req := <-internalC:
			req(in)
		}
	}
	in.finish(err)
	cancel(err)
	_ = listener
}

func (in *internal) finish(err error) {
	in.logger.With("err", err).Info("exiting shell loop")
	for _, errorC := range in.listSignalC {
		errorC <- err
	}
}
