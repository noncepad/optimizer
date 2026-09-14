// Package shell sets up a telnet endpoint
package shell

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
)

type Shell struct {
	ctx       context.Context
	cancel    context.CancelCauseFunc
	internalC chan<- func(*internal)
	// chatBuilder ChatBuilder
}

func Create(parentCtx context.Context, addr net.Addr, entry *slog.Logger, chatBuilder ChatBuilder) (Shell, error) {
	if addr.Network() != "tcp" {
		return Shell{}, fmt.Errorf("shell only supports tcp: have %s", addr.Network())
	}
	listener, err := net.Listen("tcp", addr.String())
	if err != nil {
		return Shell{}, fmt.Errorf("starting server failed: %s", err)
	}
	listenErrorC := make(chan error, 20)
	connC := make(chan *clientInfo, 20)
	internalC := make(chan func(*internal), 100)
	ctx, cancel := context.WithCancelCause(parentCtx)
	go loopListen(ctx, listener, listenErrorC, connC, entry, chatBuilder)
	go loopInternal(
		ctx,
		cancel,
		internalC,
		listenErrorC,
		connC,
		entry,
		listener,
	)
	return Shell{
		ctx:       ctx,
		cancel:    cancel,
		internalC: internalC,
	}, nil
}

func (s Shell) CloseSignal() <-chan error {
	doneC := s.ctx.Done()
	signalC := make(chan error, 1)
	select {
	case <-doneC:
		signalC <- s.ctx.Err()
	case s.internalC <- func(in *internal) {
		in.listSignalC = append(in.listSignalC, signalC)
	}:
	}
	return signalC
}

func (s Shell) Close() error {
	signalC := s.CloseSignal()
	s.cancel(errors.New("close signal"))
	return <-signalC
}
