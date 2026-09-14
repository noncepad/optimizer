// Package astralane wraps bundler functionality
package astralane

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"git.noncepad.com/pkg/optimizer/bundler"
	"git.noncepad.com/pkg/solpipe-util/graph"
	sgo "github.com/gagliardetto/solana-go"
)

// errNoTipSnapshotYet is returned by Distribution() until the live
// websocket (runTipStream) has delivered at least one real TipSnapshot --
// see internal.haveSnapshot's doc comment.
var errNoTipSnapshotYet = errors.New("astralane: no tip distribution snapshot received yet")

// tipReconnectBackoff bounds how quickly runTipStream retries after
// connectAndListen's connection drops. TipConnect itself is one-shot (see
// tips.go), so without a wrapping reconnect loop a single network hiccup
// would silently stop tip-distribution updates for the rest of the
// process's life.
const tipReconnectBackoff = 5 * time.Second

var listTipAddress = []sgo.PublicKey{
	sgo.MustPublicKeyFromBase58("astrazznxsGUhWShqgNtAdfrzP2G83DzcWVJDxwV9bF"),
	sgo.MustPublicKeyFromBase58("astra4uejePWneqNaJKuFFA8oonqCE1sqF6b45kDMZm"),
	sgo.MustPublicKeyFromBase58("astra9xWY93QyfG6yM8zwsKsRodscjQ2uU2HKNL5prk"),
	sgo.MustPublicKeyFromBase58("astraRVUuTHjpwEVvNBeQEgwYx9w9CFyfxjYoobCZhL"),
	sgo.MustPublicKeyFromBase58("astraEJ2fEj8Xmy6KLG7B3VfbKfsHXhHrNdCQx7iGJK"),
	sgo.MustPublicKeyFromBase58("astraubkDw81n4LuutzSQ8uzHCv4BhPVhfvTcYv8SKC"),
	sgo.MustPublicKeyFromBase58("astraZW5GLFefxNPAatceHhYjfA1ciq9gvfEg2S47xk"),
	sgo.MustPublicKeyFromBase58("astrawVNP4xDBKT7rAdxrLYiTSTdqtUr63fSMduivXK"),
	sgo.MustPublicKeyFromBase58("AstrA1ejL4UeXC2SBP4cpeEmtcFPZVLxx3XGKXyCW6to"),
	sgo.MustPublicKeyFromBase58("AsTra79FET4aCKWspPqeSFvjJNyp96SvAnrmyAxqg5b7"),
	sgo.MustPublicKeyFromBase58("AstrABAu8CBTyuPXpV4eSCJ5fePEPnxN8NqBaPKQ9fHR"),
	sgo.MustPublicKeyFromBase58("AsTRADtvb6tTmrsqULQ9Wji9PigDMjhfEMza6zkynEvV"),
	sgo.MustPublicKeyFromBase58("AsTRAEoyMofR3vUPpf9k68Gsfb6ymTZttEtsAbv8Bk4d"),
	sgo.MustPublicKeyFromBase58("AStrAJv2RN2hKCHxwUMtqmSxgdcNZbihCwc1mCSnG83W"),
	sgo.MustPublicKeyFromBase58("Astran35aiQUF57XZsmkWMtNCtXGLzs8upfiqXxth2bz"),
	sgo.MustPublicKeyFromBase58("AStRAnpi6kFrKypragExgeRoJ1QnKH7pbSjLAKQVWUum"),
	sgo.MustPublicKeyFromBase58("ASTRaoF93eYt73TYvwtsv6fMWHWbGmMUZfVZPo3CRU9C"),
}

type external struct {
	ctx       context.Context
	cancel    context.CancelCauseFunc
	internalC chan<- func(*internal)
}

func Create(
	parentCtx context.Context,
	entry *slog.Logger,
) (bundler.Bundler, error) {
	entry = entry.With("bundler", "astralane")
	ctx, cancel := context.WithCancelCause(parentCtx)
	internalC := make(chan func(*internal), 100)
	go loopInternal(
		ctx,
		cancel,
		entry,
		internalC,
	)
	go runTipStream(ctx, entry, internalC)
	return external{ctx: ctx, cancel: cancel, internalC: internalC}, nil
}

// runTipStream keeps a live TipConnect websocket connection open for the
// life of ctx, applying every real snapshot to internal state via
// internalC and reconnecting (fixed backoff) whenever the connection drops
// -- TipConnect itself is one-shot (returns on the first read error), so
// without this loop a single network hiccup would silently stop
// tip-distribution updates for the rest of the process's life.
func runTipStream(ctx context.Context, entry *slog.Logger, internalC chan<- func(*internal)) {
	doneC := ctx.Done()
	for {
		select {
		case <-doneC:
			return
		default:
		}
		dataC := make(chan TipSnapshot, 100)
		errorC := make(chan error, 1)
		go TipConnect(ctx, entry, dataC, errorC)
	inner:
		for {
			select {
			case <-doneC:
				return
			case snapshot := <-dataC:
				select {
				case <-doneC:
					return
				case internalC <- func(in *internal) { in.applySnapshot(snapshot) }:
				}
			case err := <-errorC:
				entry.Warn(fmt.Sprintf("astralane: tip stream disconnected, reconnecting in %s: %s", tipReconnectBackoff, err))
				break inner
			}
		}
		select {
		case <-doneC:
			return
		case <-time.After(tipReconnectBackoff):
		}
	}
}

func (e1 external) CloseSignal() <-chan error {
	signalC := make(chan error, 1)
	doneC := e1.ctx.Done()

	select {
	case <-doneC:
		signalC <- e1.ctx.Err()
	case e1.internalC <- func(in *internal) {
		in.signalCList = append(in.signalCList, signalC)
	}:
	}
	return signalC
}

func (e1 external) Close() error {
	signalC := e1.CloseSignal()
	e1.cancel(errors.New("forcing cancel"))
	return <-signalC
}

// round robin this to avoid write locks
func (e1 external) Tip() ([]sgo.PublicKey, error) {
	list := make([]sgo.PublicKey, len(listTipAddress))
	copy(list[:], listTipAddress[:])
	return list, nil
}

func (e1 external) Distribution() ([5]graph.Lamports, error) {
	var list [5]graph.Lamports
	var haveSnapshot bool
	doneC := e1.ctx.Done()
	wroteC := make(chan struct{}, 1)
	select {
	case <-doneC:
		return list, e1.ctx.Err()
	case e1.internalC <- func(in *internal) {
		copy(list[:], in.distribution[:])
		haveSnapshot = in.haveSnapshot
		wroteC <- struct{}{}
	}:
	}
	select {
	case <-doneC:
		return list, e1.ctx.Err()
	case <-wroteC:
	}
	if !haveSnapshot {
		return list, errNoTipSnapshotYet
	}
	return list, nil
}

func (e1 external) Code() uint8 {
	return bundler.BundlerAstralane
}
