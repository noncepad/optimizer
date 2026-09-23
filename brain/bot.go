package brain

import (
	"context"
	"io"
	"log/slog"
	"sync"

	mgrbot "git.noncepad.com/pkg/bot/catscope"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
	"github.com/noncepad/catmsg"
)

// onceCloser makes Close idempotent. Real, live-hit reason this exists
// (2026-09-16): the log file upload() opens gets closed from two places
// -- Bot.Close() closes it directly, and mgrbot.Bot.LogToFile's own
// goroutine (see catscope/log.go) closes the same file itself once its
// stream ends -- so whichever runs first turned the second into a
// double-close. Wrapping the file in this before it's ever handed to
// either one means only the first Close attempt actually reaches the
// underlying file; the rest are no-ops that return its result.
type onceCloser struct {
	io.WriteCloser
	once sync.Once
	err  error
}

func newOnceCloser(wc io.WriteCloser) *onceCloser {
	return &onceCloser{WriteCloser: wc}
}

func (c *onceCloser) Close() error {
	c.once.Do(func() {
		c.err = c.WriteCloser.Close()
	})
	return c.err
}

// sendBuf/recvBuf are how many messages SendC/RecvC can hold before a
// slow reader (or a burst from the bot) starts blocking the sender --
// generous enough for a burst of a few dozen messages, not sized against
// any real measured traffic since none exists yet for a caller of this
// generic package to have measured.
const (
	sendBuf = 64
	recvBuf = 64
)

// Bot represents one running WASM bot instance uploaded to a validator
// pipeline: its handshake, an on-disk log file of its real stderr
// output, and plain Go channels for its stdin/stdout messaging. Callers
// define their own wire protocol on top of the raw catmsg.FixedPair
// values flowing through SendC/RecvC -- this package doesn't know or
// care what any specific bot mode's key flags mean (see message.go in
// brain/arbv1 and siblings for that).
type Bot struct {
	mode      string
	pipeline  sgo.PublicKey
	handshake mgrbot.Handshake
	instance  mgrbot.Bot
	logPath   string
	logFile   io.WriteCloser

	// SendC delivers messages to the bot's stdin, in order -- send-only
	// from the caller's side. Keep sending until ErrC fires; there's no
	// need (and no way) to close SendC yourself.
	SendC chan<- catmsg.FixedPair
	// RecvC delivers every message the bot sends via stdout, in order.
	// Closed once the bot's connection ends (check ErrC for why).
	RecvC <-chan catmsg.FixedPair
	// ErrC receives exactly one value when the bot's connection ends --
	// nil for a clean shutdown (context cancelled), non-nil otherwise
	// (stdout subscription error or the bot process itself exiting).
	ErrC <-chan error
}

func newBot(
	ctx context.Context,
	mode string,
	pipeline sgo.PublicKey,
	handshake mgrbot.Handshake,
	instance mgrbot.Bot,
	logPath string,
	logFile io.WriteCloser,
	entry *slog.Logger,
) *Bot {
	sendC := make(chan catmsg.FixedPair, sendBuf)
	recvC := make(chan catmsg.FixedPair, recvBuf)
	errC := make(chan error, 1)
	b := &Bot{
		mode:      mode,
		pipeline:  pipeline,
		handshake: handshake,
		instance:  instance,
		logPath:   logPath,
		logFile:   logFile,
		SendC:     sendC,
		RecvC:     recvC,
		ErrC:      errC,
	}
	go loopSend(ctx, instance, sendC, entry)
	go loopRecv(ctx, instance, recvC, errC)
	return b
}

func (b *Bot) Mode() string                { return b.mode }
func (b *Bot) Pipeline() sgo.PublicKey     { return b.pipeline }
func (b *Bot) Handshake() mgrbot.Handshake { return b.handshake }
func (b *Bot) Wallet() sgo.PublicKey       { return b.handshake.Wallet() }
func (b *Bot) LogPath() string             { return b.logPath }

// Close closes the underlying bot connection and its log file. SendC/
// RecvC/ErrC are left as-is (RecvC closes and ErrC fires on its own,
// same as any other connection end, once the close takes effect).
func (b *Bot) Close() error {
	err := b.instance.Close()
	_ = b.logFile.Close()
	return err
}

// loopSend forwards sendC onto the bot's real stdin one message at a
// time -- a send failure is logged, not returned (SendC is a plain
// channel with no way to report a per-message error back to whoever
// sent it), and does not stop the loop: a transient send failure
// shouldn't silently kill delivery of every message queued after it.
func loopSend(ctx context.Context, instance mgrbot.Bot, sendC <-chan catmsg.FixedPair, entry *slog.Logger) {
	doneC := ctx.Done()
	for {
		select {
		case <-doneC:
			return
		case pair, ok := <-sendC:
			if !ok {
				return
			}
			if err := instance.CustomStdin(pair); err != nil {
				entry.With(logger.Loc("loopSend", 1), "err", err).Error("bot: stdin send failed")
			}
		}
	}
}

// loopRecv subscribes to the bot's real stdout stream and forwards every
// message onto recvC, until the bot's connection ends (its own exit, a
// subscription error, or ctx being cancelled) -- at which point it
// closes recvC and delivers exactly one value on errC.
func loopRecv(ctx context.Context, instance mgrbot.Bot, recvC chan<- catmsg.FixedPair, errC chan<- error) {
	defer close(recvC)
	sub := instance.StdoutCustom(ctx, func(catmsg.FixedPair) bool { return true })
	defer sub.Unsubscribe()
	botErrorC := instance.CloseSignal()
	doneC := ctx.Done()
	for {
		select {
		case <-doneC:
			errC <- ctx.Err()
			return
		case err := <-sub.ErrorC:
			errC <- err
			return
		case err := <-botErrorC:
			errC <- err
			return
		case msg := <-sub.StreamC:
			select {
			case recvC <- msg:
			case <-doneC:
				errC <- ctx.Err()
				return
			}
		}
	}
}
