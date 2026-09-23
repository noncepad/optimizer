package botruntime

import (
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"

	mgrbot "git.noncepad.com/pkg/bot/catscope"
	"git.noncepad.com/pkg/optimizer/api"
	sgo "github.com/gagliardetto/solana-go"
	"github.com/noncepad/catmsg"
)

// botAdapter is the one concrete api.Bot -- mgrbot.Bot's own method shapes
// (CustomStdin(pair) error, StdoutCustom(ctx, filter) a subscription with
// StreamC/ErrorC, LogToFile(ctx, io.WriteCloser, isString) a blocking
// writer loop) don't match api.Bot's channel/io.ReadCloser shape directly
// -- see this type's three loop goroutines below, one per method that
// needs adapting.
type botAdapter struct {
	instance mgrbot.Bot
	hash     sgo.Hash
	sendC    chan catmsg.FixedPair
	recvC    chan catmsg.FixedPair
	stderrR  *io.PipeReader
	entry    *slog.Logger
	// isBidding records whether runtime.Budget has last been called with
	// a nonzero amount -- runtime.Status() reads it via bidding() to
	// distinguish api.StatusTypeBidder from api.StatusTypeNonbidder.
	// Lives here (rather than on runtime) since it's per-bot state in
	// spirit, even though this package only ever tracks one bot per
	// runtime today -- see runtime's own doc comment.
	isBidding atomic.Bool
}

// newBotAdapter wraps an already-handshaked instance. Every loop below
// exits on instance.Ctx().Done() -- the bot's own real lifecycle (ends
// when the bot process exits or its connection breaks), not a separately
// owned context, so there's nothing extra for a caller to cancel.
func newBotAdapter(instance mgrbot.Bot, entry *slog.Logger) *botAdapter {
	hash, _ := instance.LocalBotID()
	pr, pw := io.Pipe()
	ba := &botAdapter{
		instance: instance,
		hash:     hash,
		sendC:    make(chan catmsg.FixedPair, 16),
		recvC:    make(chan catmsg.FixedPair, 16),
		stderrR:  pr,
		entry:    entry,
	}
	go ba.loopSend()
	go ba.loopRecv()
	// isString=false: origWtr gets the bot's raw stderr bytes directly
	// (see mgrbot.Bot.LogToFile's own isString branch) -- exactly what
	// io.ReadCloser callers of Stderr() expect, no line-buffering
	// reshaping needed.
	go instance.LogToFile(instance.Ctx(), pw, false)
	return ba
}

func (ba *botAdapter) Hash() sgo.Hash {
	return ba.hash
}

// Stderr returns the read side of the pipe LogToFile is writing into.
// Closing it ends LogToFile's own loop too (its next write fails with a
// closed-pipe error, which is already one of its two normal exit paths --
// see LogToFile's own `if err != nil { break out }`), so there's no extra
// teardown needed beyond the io.ReadCloser contract itself.
func (ba *botAdapter) Stderr() io.ReadCloser {
	return ba.stderrR
}

func (ba *botAdapter) Send() chan<- catmsg.FixedPair {
	return ba.sendC
}

func (ba *botAdapter) Recv() <-chan catmsg.FixedPair {
	return ba.recvC
}

// bidding and setBidding back runtime.Status()/runtime.Budget() -- see
// isBidding's own field doc comment.
func (ba *botAdapter) bidding() bool {
	return ba.isBidding.Load()
}

func (ba *botAdapter) setBidding(v bool) {
	ba.isBidding.Store(v)
}

// loopSend adapts Send()'s channel onto mgrbot.Bot.CustomStdin, which
// sends synchronously and returns an error per call -- api.Bot.Send has no
// error channel of its own, so a failed send is logged, not surfaced to
// the caller, the same fire-and-forget convention harness/harness.go's
// SetSOL/Fund/Sweep already use for their own no-error-return mutating
// methods.
func (ba *botAdapter) loopSend() {
	doneC := ba.instance.Ctx().Done()
	for {
		select {
		case <-doneC:
			return
		case pair := <-ba.sendC:
			if err := ba.instance.CustomStdin(pair); err != nil {
				ba.entry.Error(fmt.Sprintf("botruntime: bot %s: send failed: %s", ba.hash, err))
			}
		}
	}
}

// loopRecv adapts mgrbot.Bot.StdoutCustom's subscription onto Recv()'s
// plain channel -- every message is accepted (filter always returns true)
// since api.Bot.Recv has no filtering concept of its own.
func (ba *botAdapter) loopRecv() {
	sub := ba.instance.StdoutCustom(ba.instance.Ctx(), func(catmsg.FixedPair) bool { return true })
	defer sub.Unsubscribe()
	doneC := ba.instance.Ctx().Done()
	for {
		select {
		case <-doneC:
			return
		case err := <-sub.ErrorC:
			ba.entry.Error(fmt.Sprintf("botruntime: bot %s: recv subscription error: %s", ba.hash, err))
			return
		case fp := <-sub.StreamC:
			select {
			case ba.recvC <- fp:
			case <-doneC:
				return
			}
		}
	}
}

var _ api.Bot = (*botAdapter)(nil)
