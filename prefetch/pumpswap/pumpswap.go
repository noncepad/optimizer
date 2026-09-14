// Package pumpswap preloads PumpSwap AMM pool state.
package pumpswap

import (
	"context"
	"database/sql"
	"fmt"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

var (
	ProgramID = sgo.MustPublicKeyFromBase58("pAMMBay6oceH9fJKBRHGP5D4bD4sWpmSwMn52FMfXEA")
)

// Pumpswap holds pools loaded at startup.
type Pumpswap struct {
	Pools []*Pool
}

// Create discovers PumpSwap pools and persists results to db
// (pumpswap_pool). If db already has pools from a previous run, they're
// loaded back instead of re-fetching, unless force is true.
func Create(ctx context.Context, stateClient state.Client, db *sql.DB, maxSubscriptionCount int, force bool) (*Pumpswap, error) {
	entry := logger.FromContext(ctx)
	p := new(Pumpswap)
	n, err := poolCount(db)
	if err != nil {
		return nil, fmt.Errorf("failed to check pumpswap pool count: %s", err)
	}
	if n == 0 || force {
		if err = p.fetch(ctx, stateClient, entry, maxSubscriptionCount, db); err != nil {
			return nil, fmt.Errorf("failed to load pumpswap data: %s", err)
		}
		return p, nil
	}
	p.Pools, err = loadPools(db)
	if err != nil {
		return nil, fmt.Errorf("failed to load pumpswap data from db: %s", err)
	}
	return p, nil
}
