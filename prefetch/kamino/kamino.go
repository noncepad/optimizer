// Package kamino preloads Kamino Lending reserve accounts.
package kamino

import (
	"context"
	"database/sql"
	"fmt"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

var (
	ProgramID = sgo.MustPublicKeyFromBase58("KLend2g3cP87fffoy8q1mQqGKjrxjC8boSyAYavgmjD")
)

// Discriminators for Anchor account types.
var (
	DiscLendingMarket = [8]byte{246, 114, 50, 98, 72, 157, 28, 120}
	DiscReserve       = [8]byte{43, 242, 204, 202, 26, 247, 59, 127}
)

// Kamino holds all Kamino Lending reserve accounts loaded at startup.
type Kamino struct {
	Reserves []*Reserve
	mReserve map[sgo.PublicKey]int
}

// Create queries the Kamino Lending program at depth 2 (program → lending
// market → reserve) and collects all reserve accounts, persisting results to
// db (kamino_reserve). If db already has reserves from a previous run,
// they're loaded back instead of re-fetching, unless force is true.
func Create(ctx context.Context, stateClient state.Client, db *sql.DB, force bool) (*Kamino, error) {
	entry := logger.FromContext(ctx)
	k := new(Kamino)
	n, err := reserveCount(db)
	if err != nil {
		return nil, fmt.Errorf("failed to check kamino reserve count: %s", err)
	}
	if n == 0 || force {
		if err = k.fetch(ctx, stateClient, entry); err != nil {
			return nil, fmt.Errorf("failed to load kamino data: %s", err)
		}
		if err = insertReserves(db, k.Reserves); err != nil {
			return nil, fmt.Errorf("failed to save kamino data: %s", err)
		}
		return k, nil
	}
	k.Reserves, k.mReserve, err = loadReserves(db)
	if err != nil {
		return nil, fmt.Errorf("failed to load kamino data from db: %s", err)
	}
	return k, nil
}

// Find looks up a reserve by its pubkey.
func (k *Kamino) Find(reserveID sgo.PublicKey) *Reserve {
	i, present := k.mReserve[reserveID]
	if !present {
		return nil
	}
	return k.Reserves[i]
}
