// Package drift preloads Drift v2 SpotMarket accounts (global market data
// only -- User, the per-wallet position account, is not fetched here since
// there is no bounded set to bulk-load, and PerpMarket is skipped since it
// has no mint/vault of its own -- perp markets settle in the program's
// quote asset).
package drift

import (
	"context"
	"database/sql"
	"fmt"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

var (
	ProgramID = sgo.MustPublicKeyFromBase58("dRiftyHA39MWEi3m9aunc5MzRF1JYuBsbn6VPcn33UH")
)

// Discriminator for the Anchor SpotMarket account type.
var DiscSpotMarket = [8]byte{100, 177, 8, 107, 168, 65, 65, 39}

// Drift holds all Drift v2 SpotMarket accounts loaded at startup.
type Drift struct {
	SpotMarkets []*SpotMarket
	mMarket     map[sgo.PublicKey]int
}

// Create queries the Drift v2 program at depth 1 (program → spot market)
// and collects all spot market accounts, persisting results to db
// (drift_spot_market). If db already has spot markets from a previous run,
// they're loaded back instead of re-fetching, unless force is true.
func Create(ctx context.Context, stateClient state.Client, db *sql.DB, force bool) (*Drift, error) {
	entry := logger.FromContext(ctx)
	d := new(Drift)
	n, err := spotMarketCount(db)
	if err != nil {
		return nil, fmt.Errorf("failed to check drift spot market count: %s", err)
	}
	if n == 0 || force {
		if err = d.fetch(ctx, stateClient, entry); err != nil {
			return nil, fmt.Errorf("failed to load drift data: %s", err)
		}
		if err = insertSpotMarkets(db, d.SpotMarkets); err != nil {
			return nil, fmt.Errorf("failed to save drift data: %s", err)
		}
		return d, nil
	}
	d.SpotMarkets, d.mMarket, err = loadSpotMarkets(db)
	if err != nil {
		return nil, fmt.Errorf("failed to load drift data from db: %s", err)
	}
	return d, nil
}

// Find looks up a spot market by its pubkey.
func (d *Drift) Find(marketID sgo.PublicKey) *SpotMarket {
	i, present := d.mMarket[marketID]
	if !present {
		return nil
	}
	return d.SpotMarkets[i]
}
