package pricefeed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

// Polls the Jupiter Price API v3 for Config.Mints on a fixed
// interval and publishes results on the channel returned by Run.
type Poller struct {
	cfg    Config
	client *http.Client
	batches [][]sgo.PublicKey 	// batches cfg.Mints into groups of at most MaxMintsPerRequest
}

// Validates cfg and returns a  Poller
func New(cfg Config) (*Poller, error) {
	if len(cfg.Mints) == 0 {
		return nil, errors.New("jupiter: at least one mint is required")
	}
	p := &Poller{cfg: cfg, client: &http.Client{Timeout: 10 * time.Second}}
	if p.cfg.Interval <= 0 {
		p.cfg.Interval = FreeTierInterval
	}
	p.batches = batchMints(cfg.Mints, MaxMintsPerRequest)
	return p, nil
}

func batchMints(mints []sgo.PublicKey, size int) [][]sgo.PublicKey {
	batches := make([][]sgo.PublicKey, 0, (len(mints)+size-1)/size)
	for len(mints) > 0 {
		n := size
		if len(mints) < n {
			n = len(mints)
		}
		batch := make([]sgo.PublicKey, n)
		copy(batch, mints[:n])
		batches = append(batches, batch)
		mints = mints[n:]
	}
	return batches
}

// Run starts polling in a goroutine and returns the channel
// (channel is closed once ctx is cancelled and the poll loop exits)
func (p *Poller) Run(ctx context.Context) <-chan PriceUpdate {
	updateC := make(chan PriceUpdate, len(p.cfg.Mints))
	go p.loop(ctx, updateC)
	return updateC
}

func (p *Poller) loop(ctx context.Context, updateC chan<- PriceUpdate) {
	defer close(updateC)
	entry := logger.FromContext(ctx).With("component", "jupiter")
	ticker := time.NewTicker(p.cfg.Interval)
	defer ticker.Stop()
	batchIdx := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pollBatch(ctx, entry, p.batches[batchIdx], updateC)
			batchIdx = (batchIdx + 1) % len(p.batches)
		}
	}
}

// pollBatch fetches one batch and publishes a PriceUpdate for every mint
func (p *Poller) pollBatch(ctx context.Context, entry *slog.Logger, batch []sgo.PublicKey, updateC chan<- PriceUpdate) {
	prices, err := p.fetch(ctx, batch)
	if err != nil {
		entry.Warn(fmt.Sprintf("jupiter: poll failed: %s", err))
		return
	}
	now := time.Now()
	for _, mint := range batch {
		tp, present := prices[mint]
		if !present {
			continue
		}
		update := PriceUpdate{
			Mint:           mint,
			USDPrice:       tp.USDPrice,
			Decimals:       tp.Decimals,
			BlockID:        tp.BlockID,
			PriceChange24h: tp.PriceChange24h,
			FetchedAt:      now,
		}
		select {
		case updateC <- update:
		case <-ctx.Done():
			return
		}
	}
}
