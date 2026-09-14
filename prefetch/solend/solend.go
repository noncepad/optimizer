// Package solend preloads Solend/Save token-lending reserve accounts
// (global market data only -- Obligation, the per-wallet position account,
// is not fetched here since there is no bounded set to bulk-load).
package solend

import (
	"context"
	"database/sql"
	"fmt"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

var (
	ProgramID = sgo.MustPublicKeyFromBase58("So1endDq2YkqhipRh3WViPa8hdiSpxWy6z3Z6tMCpAo")
)

// Solend has no Anchor discriminator -- accounts are dispatched by exact
// byte length instead, same as the deployed token-lending program.
const (
	lendingMarketLen = 290
	reserveLen       = 619
)

// Solend holds all Solend/Save reserve accounts loaded at startup.
type Solend struct {
	Reserves []*Reserve
	mReserve map[sgo.PublicKey]int
}

// Create discovers Solend lending markets and reserves via an active,
// per-node Subscribe/ack chain (program -> lending_market, then
// lending_market -> reserve; see event.go, which mirrors
// orca.fetchWhirlpool's own config->pool chain) and collects all reserve
// accounts, persisting results to db (solend_reserve). If db already has
// reserves from a previous run, they're loaded back instead of
// re-fetching, unless force is true.
func Create(ctx context.Context, stateClient state.Client, db *sql.DB, force bool) (*Solend, error) {
	entry := logger.FromContext(ctx)
	s := new(Solend)
	n, err := reserveCount(db)
	if err != nil {
		return nil, fmt.Errorf("failed to check solend reserve count: %s", err)
	}
	if n == 0 || force {
		if err = s.fetch(ctx, stateClient, db, entry, force); err != nil {
			return nil, fmt.Errorf("failed to load solend data: %s", err)
		}
		return s, nil
	}
	s.Reserves, s.mReserve, err = loadReserves(db)
	if err != nil {
		return nil, fmt.Errorf("failed to load solend data from db: %s", err)
	}
	return s, nil
}

// Find looks up a reserve by its pubkey.
func (s *Solend) Find(reserveID sgo.PublicKey) *Reserve {
	i, present := s.mReserve[reserveID]
	if !present {
		return nil
	}
	return s.Reserves[i]
}
