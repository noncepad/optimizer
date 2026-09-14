// Package pumpfun preloads Pump.fun bonding-curve state.
package pumpfun

import (
	"context"
	"database/sql"
	"fmt"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

var (
	ProgramID = sgo.MustPublicKeyFromBase58("6EF8rrecthR5Dkzon8Nwu78hRvfCKubJ14M5uBEwF6P")
)

// Pumpfun holds bonding curves loaded at startup.
type Pumpfun struct {
	Curves []*BondingCurveEntry
}

// Create discovers Pump.fun bonding curves and persists results to db
// (pumpfun_bonding_curve). If db already has curves from a previous run,
// they're loaded back instead of re-fetching, unless force is true.
//
// maxSubscriptionCount bounds how many roots may be in flight at once (see
// util.PendingSubscriptionStatus) -- same purpose as orca.Create's
// parameter of the same name. Pump.fun's mint volume is Orca-scale or
// larger (every token ever created via pump.fun's bonding curve, most
// long-dead), so this throttle matters here too.
func Create(ctx context.Context, stateClient state.Client, db *sql.DB, maxSubscriptionCount int, force bool) (*Pumpfun, error) {
	entry := logger.FromContext(ctx)
	p := new(Pumpfun)
	n, err := curveCount(db)
	if err != nil {
		return nil, fmt.Errorf("failed to check pumpfun curve count: %s", err)
	}
	if n == 0 || force {
		if err = p.fetch(ctx, stateClient, entry, maxSubscriptionCount, db); err != nil {
			return nil, fmt.Errorf("failed to load pumpfun data: %s", err)
		}
		return p, nil
	}
	p.Curves, err = loadCurves(db)
	if err != nil {
		return nil, fmt.Errorf("failed to load pumpfun data from db: %s", err)
	}
	return p, nil
}
