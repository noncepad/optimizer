// Package jet preloads Jet Protocol V1 lending pool reserve accounts
// (global market data only -- Obligation, the per-wallet position account,
// is not fetched here since there is no bounded set to bulk-load).
package jet

import (
	"context"
	"database/sql"
	"fmt"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

var (
	ProgramID = sgo.MustPublicKeyFromBase58("JPv1rCqrhagNNmJVM5J1he7msQ5ybtvE1nNuHpDHMNU")
)

// Jet Protocol V1 has no Anchor discriminator -- accounts are dispatched
// by exact byte length instead.
const (
	marketLen  = 12808
	reserveLen = 2056
)

// Jet holds all Jet Protocol V1 reserve accounts loaded at startup.
type Jet struct {
	Reserves []*Reserve
	mReserve map[sgo.PublicKey]int
}

// Create queries the Jet Protocol V1 program at depth 2 (program → market
// → reserve) and collects all reserve accounts, persisting results to db
// (jet_reserve). If db already has reserves from a previous run, they're
// loaded back instead of re-fetching, unless force is true.
func Create(ctx context.Context, stateClient state.Client, db *sql.DB, force bool) (*Jet, error) {
	entry := logger.FromContext(ctx)
	j := new(Jet)
	n, err := reserveCount(db)
	if err != nil {
		return nil, fmt.Errorf("failed to check jet reserve count: %s", err)
	}
	if n == 0 || force {
		if err = j.fetch(ctx, stateClient, entry); err != nil {
			return nil, fmt.Errorf("failed to load jet data: %s", err)
		}
		if err = insertReserves(db, j.Reserves); err != nil {
			return nil, fmt.Errorf("failed to save jet data: %s", err)
		}
		return j, nil
	}
	j.Reserves, j.mReserve, err = loadReserves(db)
	if err != nil {
		return nil, fmt.Errorf("failed to load jet data from db: %s", err)
	}
	return j, nil
}

// Find looks up a reserve by its pubkey.
func (j *Jet) Find(reserveID sgo.PublicKey) *Reserve {
	i, present := j.mReserve[reserveID]
	if !present {
		return nil
	}
	return j.Reserves[i]
}
