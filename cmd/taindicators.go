package main

import "math"

// taindicators.go -- pure technical-analysis math (SMA, Bollinger Bands,
// RSI), computed from Store.PriceHistory's already-collected price
// series. No new data source: this is the exact same bounded history
// (see store.go's own historyLen doc comment) the existing per-symbol
// sparkline already reads, just fed through real indicator formulas
// instead of a min-max sparkline.

// sma returns the simple moving average of period-length trailing
// windows, aligned to the END of each window (out[i] covers
// values[i-period+1..i]) -- math.NaN() before the first full window,
// same "not enough data yet" convention every series here uses so
// callers can skip NaN points when plotting instead of drawing a
// misleading partial average.
func sma(values []float64, period int) []float64 {
	out := make([]float64, len(values))
	for i := range out {
		out[i] = math.NaN()
	}
	if period <= 0 || len(values) < period {
		return out
	}
	sum := 0.0
	for i, v := range values {
		sum += v
		if i >= period {
			sum -= values[i-period]
		}
		if i >= period-1 {
			out[i] = sum / float64(period)
		}
	}
	return out
}

// stddev returns the population standard deviation of the same trailing
// windows sma uses -- Bollinger Bands' own definition, not sample
// (n-1) stddev.
func stddev(values []float64, period int) []float64 {
	out := make([]float64, len(values))
	for i := range out {
		out[i] = math.NaN()
	}
	means := sma(values, period)
	for i := range values {
		if i < period-1 {
			continue
		}
		m := means[i]
		var sumSq float64
		for k := i - period + 1; k <= i; k++ {
			d := values[k] - m
			sumSq += d * d
		}
		out[i] = math.Sqrt(sumSq / float64(period))
	}
	return out
}

// minBandWidthFrac floors each Bollinger Band's half-width to this
// fraction of the mid price whenever real volatility collapses to (near)
// zero -- most often a frozen/flat price window, itself most often the
// trader's own outlier-rejection filter holding price_prev steady rather
// than a truly zero-volatility market (see SymbolStats.update in
// market_stats.rs, and REJECT_RATIO/anchor's own doc comments there for
// why prices freeze instead of drifting). Without this floor, a flat
// window's stddev also collapses to ~0, so upper/lower land exactly on
// mid -- and since the bands share terminal cells with the price/SMA
// line (drawn with lower layer-priority, see marketwatchchart.go's
// renderTAPanel), a zero-width band isn't just imperceptible, it's
// literally the same pixels as the line sitting on top of it. 0.1% is
// small enough to stay invisible next to any real move worth showing,
// but wide enough to read as "two distinct lines" at typical terminal
// chart resolution.
const minBandWidthFrac = 0.001

// bollingerBands returns the classic (SMA, SMA+k·σ, SMA-k·σ) triple over
// period-length trailing windows, with each band's half-width floored to
// minBandWidthFrac of the mid price (see its own doc comment) so the
// bands never fully collapse onto the price/SMA line.
func bollingerBands(values []float64, period int, numStdDev float64) (mid, upper, lower []float64) {
	mid = sma(values, period)
	sd := stddev(values, period)
	upper = make([]float64, len(values))
	lower = make([]float64, len(values))
	for i := range values {
		if math.IsNaN(mid[i]) || math.IsNaN(sd[i]) {
			upper[i], lower[i] = math.NaN(), math.NaN()
			continue
		}
		halfWidth := numStdDev * sd[i]
		if minHalfWidth := math.Abs(mid[i]) * minBandWidthFrac; halfWidth < minHalfWidth {
			halfWidth = minHalfWidth
		}
		upper[i] = mid[i] + halfWidth
		lower[i] = mid[i] - halfWidth
	}
	return mid, upper, lower
}

// rsi computes Wilder's Relative Strength Index (the standard 0-100
// overbought/oversold momentum oscillator) using Wilder's own smoothing
// (an EMA-like running average of gains/losses with weight 1/period per
// new observation, not a plain rolling-window average) -- the real
// definition every TA platform uses, not an approximation of it.
// out[i] is NaN until index `period` (the first index with a full
// period's worth of preceding changes to average).
func rsi(values []float64, period int) []float64 {
	out := make([]float64, len(values))
	for i := range out {
		out[i] = math.NaN()
	}
	if period <= 0 || len(values) < period+1 {
		return out
	}

	var avgGain, avgLoss float64
	for i := 1; i <= period; i++ {
		change := values[i] - values[i-1]
		if change > 0 {
			avgGain += change
		} else {
			avgLoss += -change
		}
	}
	avgGain /= float64(period)
	avgLoss /= float64(period)

	setRSI := func(idx int, gain, loss float64) {
		if loss == 0 {
			out[idx] = 100
			return
		}
		rs := gain / loss
		out[idx] = 100 - (100 / (1 + rs))
	}
	setRSI(period, avgGain, avgLoss)

	for i := period + 1; i < len(values); i++ {
		change := values[i] - values[i-1]
		gain, loss := 0.0, 0.0
		if change > 0 {
			gain = change
		} else {
			loss = -change
		}
		avgGain = (avgGain*float64(period-1) + gain) / float64(period)
		avgLoss = (avgLoss*float64(period-1) + loss) / float64(period)
		setRSI(i, avgGain, avgLoss)
	}
	return out
}
