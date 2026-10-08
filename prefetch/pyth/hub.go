package pyth

import "sync"

// Hub fans out live price updates from one underlying Watch subscription
// to any number of independent goroutine subscribers -- Watch's own
// channel has exactly one consumer (whoever reads it), so anything that
// wants more than one independent reader (e.g. one goroutine recording
// into prefetch.db, another feeding an agent's own live-price tool, in
// two entirely separate processes each running their own Hub fed by
// their own Watch call, or several consumers within the same process)
// needs a broadcaster in front of it. Hub is that broadcaster; it has
// no opinion about where updates come from (Run, below, is the
// convenience for the common case of "everything Watch produces").
//
// Zero value is not usable -- construct with NewHub.
type Hub struct {
	mu   sync.Mutex
	subs map[chan PriceUpdate]struct{}
	// last caches the most recently published update per feed, so a
	// Subscribe call immediately delivers the current price instead of
	// leaving a new subscriber to wait for the next on-chain change
	// just to learn what's already known.
	last map[[32]byte]PriceUpdate
}

// NewHub returns a ready-to-use Hub with no subscribers and no known
// prices yet.
func NewHub() *Hub {
	return &Hub{
		subs: make(map[chan PriceUpdate]struct{}),
		last: make(map[[32]byte]PriceUpdate),
	}
}

// subscriberBuffer is how many updates a subscriber can be behind
// before Publish starts dropping updates for it rather than blocking --
// see Publish's own doc comment for why dropping, not blocking, is the
// right behavior for a "latest value" stream like this.
const subscriberBuffer = 4

// Subscribe registers a new subscriber and returns a channel of live
// updates plus an unsubscribe func the caller must call when done (a
// deferred call in the subscribing goroutine is the usual shape) to
// stop receiving and let Hub release the channel. Safe to call from any
// goroutine, any number of times, concurrently with Publish.
//
// If Hub already knows a price for one or more feeds (Publish has been
// called for them before), those are sent on the returned channel
// immediately, before any new live update -- see Hub's own doc comment
// on why. The channel is closed once unsubscribe is called; it is not
// closed by ctx cancellation or anything else, since Hub itself has no
// context of its own -- callers that want cancellation should select on
// both the returned channel and their own ctx.Done(), and call
// unsubscribe when they stop reading.
func (h *Hub) Subscribe() (updateC <-chan PriceUpdate, unsubscribe func()) {
	ch := make(chan PriceUpdate, subscriberBuffer)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	initial := make([]PriceUpdate, 0, len(h.last))
	for _, u := range h.last {
		initial = append(initial, u)
	}
	h.mu.Unlock()

	// Sent after releasing h.mu (not while holding it) -- initial is a
	// private copy taken under the lock above, so this is still a
	// consistent snapshot; the point is just to avoid holding the lock
	// during these sends, which block if the buffer's full (impossible
	// here, since ch was just created empty, but this keeps the
	// same "never send while holding h.mu" discipline Publish also
	// follows, in one place, rather than as a special case for this
	// one caller).
	for _, u := range initial {
		ch <- u
	}

	unsubscribe = func() {
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
	return ch, unsubscribe
}

// Publish delivers update to every current subscriber and updates the
// cached "current price" new Subscribe calls receive immediately.
//
// Non-blocking per subscriber: a subscriber whose channel is already
// full (it's fallen more than subscriberBuffer updates behind) has this
// update dropped for it rather than blocking Publish -- for a "what's
// the current price" stream, a slow subscriber catching up to a slightly
// newer value than the one it missed is the right behavior, not making
// every other subscriber (or the goroutine driving Run) stall on
// whichever one is slowest.
func (h *Hub) Publish(update PriceUpdate) {
	h.mu.Lock()
	h.last[update.Feed] = update
	subs := make([]chan PriceUpdate, 0, len(h.subs))
	for ch := range h.subs {
		subs = append(subs, ch)
	}
	h.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- update:
		default:
		}
	}
}

// Run publishes every update updateC produces (typically Watch's own
// return value) until updateC closes -- the common "feed a Hub from one
// live Watch subscription" wiring, so callers don't need to write this
// loop themselves. Blocks until updateC closes; run it in its own
// goroutine.
func (h *Hub) Run(updateC <-chan PriceUpdate) {
	for u := range updateC {
		h.Publish(u)
	}
}

// Latest returns the most recently published price for feed, if any --
// a non-blocking alternative to Subscribe for a caller that just wants
// a one-off read of the current value rather than an ongoing
// subscription (e.g. an agent tool answering a single question, not a
// goroutine that stays running).
func (h *Hub) Latest(feed [32]byte) (PriceUpdate, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	u, ok := h.last[feed]
	return u, ok
}
