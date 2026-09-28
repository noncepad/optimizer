package marketwatchv1

// store.go holds the live data this bot's TUI dashboard actually renders.
// Unlike arbv1's pendingState (a drain-once queue -- Evaluate consumes
// and discards each batch to write it to a log file), a dashboard wants
// to re-read "what's the current state" on every redraw and wants a
// short rolling history per symbol for sparkline-style charts -- so this
// is a latest-snapshot-plus-bounded-history store instead, safe for one
// writer (loopInstance) and many concurrent readers (the TUI's render
// loop).

import "sync"

// historyLen bounds how many past prices/volatilities each symbol keeps
// for its sparkline -- at the bot's own MARKET_STATS_INTERVAL_SLOTS
// (10 slots, ~4-6s) push cadence, 120 points is ~8-12 minutes of history,
// enough for a dashboard sparkline to show a real, visible trend without
// growing unbounded over a long-running session.
const historyLen = 120

// Store is the shared live state between loopInstance (the only writer,
// one goroutine) and any number of readers (the TUI's render loop, one
// per redraw tick). All access goes through the exported methods below,
// which hold mx for the duration -- callers never see a torn snapshot.
type Store struct {
	mx        sync.Mutex
	latest    MarketStatsSnapshot
	haveOne   bool
	priceHist [NSymbols][]float64
	volHist   [NSymbols][]float64
	// throughputHist is UpdatesPerSec's own bounded history -- a single
	// series (not per-symbol, unlike priceHist/volHist), since
	// throughput describes this bot's own data pipeline, not any one
	// tracked symbol.
	throughputHist []float64
}

func newStore() *Store {
	return &Store{}
}

// update records a freshly parsed snapshot and appends this tick's
// price/volatility onto each symbol's bounded history ring. Skips a
// symbol entirely for this tick if it hasn't been priced yet (Prices[i]
// <= 0 -- see MarketStatsSnapshot.Prices's own doc comment: 0.0 is the
// wire protocol's "no live quote yet" sentinel, not a real price). Real,
// live-observed bug this fixes: a symbol's very first snapshot or two,
// pushed before its pool subscriptions finish warming up, carried this
// 0.0 sentinel straight into priceHist -- a real, negative-looking price
// that then sat in the bounded 120-point window (up to ~8-12 minutes on
// every single restart) dragging PriceHistory's own min toward/through
// zero, visibly corrupting the sparkline and the TA panel's Bollinger-
// derived "low" axis label (see marketwatchchart.go's seriesRange) even
// though every symbol was actually pricing correctly moments later.
func (s *Store) update(snap MarketStatsSnapshot) {
	s.mx.Lock()
	defer s.mx.Unlock()
	s.latest = snap
	s.haveOne = true
	for i := 0; i < NSymbols; i++ {
		if snap.Prices[i] <= 0 {
			continue
		}
		s.priceHist[i] = appendBounded(s.priceHist[i], snap.Prices[i], historyLen)
		s.volHist[i] = appendBounded(s.volHist[i], snap.Volatilities[i], historyLen)
	}
	s.throughputHist = appendBounded(s.throughputHist, snap.UpdatesPerSec, historyLen)
}

func appendBounded(list []float64, v float64, max int) []float64 {
	list = append(list, v)
	if len(list) > max {
		list = list[len(list)-max:]
	}
	return list
}

// Latest returns the most recently received snapshot and whether one has
// arrived yet (false before the bot's first push).
func (s *Store) Latest() (MarketStatsSnapshot, bool) {
	s.mx.Lock()
	defer s.mx.Unlock()
	return s.latest, s.haveOne
}

// PriceHistory returns a copy of TrackedSymbols[i]'s recent price
// history, oldest first, up to historyLen points. A copy, not the live
// slice -- safe to hold onto/render from after the call returns without
// racing the next update.
func (s *Store) PriceHistory(i int) []float64 {
	s.mx.Lock()
	defer s.mx.Unlock()
	out := make([]float64, len(s.priceHist[i]))
	copy(out, s.priceHist[i])
	return out
}

// VolatilityHistory is PriceHistory's twin for volatility.
func (s *Store) VolatilityHistory(i int) []float64 {
	s.mx.Lock()
	defer s.mx.Unlock()
	out := make([]float64, len(s.volHist[i]))
	copy(out, s.volHist[i])
	return out
}

// ThroughputHistory is PriceHistory's twin for UpdatesPerSec -- one
// series, not per-symbol (see Store.throughputHist's own doc comment).
func (s *Store) ThroughputHistory() []float64 {
	s.mx.Lock()
	defer s.mx.Unlock()
	out := make([]float64, len(s.throughputHist))
	copy(out, s.throughputHist)
	return out
}
