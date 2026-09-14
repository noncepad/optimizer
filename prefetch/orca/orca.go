// Package orca preloads Orca Whirlpool trading pools.
package orca

import (
	"context"
	"database/sql"
	"fmt"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/prefetch/mintinfo"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

var (
	ProgramID     = sgo.MustPublicKeyFromBase58("whirLbMiicVdio4qvUfM5KAg6Ct8VwpYzGff3uctyCc")
	CheckPoolID   = sgo.MustPublicKeyFromBase58("Czfq3xZZDmsdGdUyrNLtRhGc47cXcZtLG4crryfu44zE")
	CheckConfigID = sgo.MustPublicKeyFromBase58("2LecshUwdy9xi7meFgHtFJQNSKk4KdTrcpvaB56dP2NQ")
)

var (
	DiscriminatorWhirlpoolConfig = [8]uint8{157, 20, 49, 224, 217, 87, 193, 254}
	DiscriminatorWhirlpool       = [8]uint8{63, 149, 209, 12, 225, 128, 99, 9}
	DiscriminatorTickArray       = [8]uint8{69, 97, 189, 190, 110, 7, 66, 187}
)

// Orca holds all Whirlpool pools loaded at startup.
type Orca struct {
	Pools []*Whirlpool
	mPool map[sgo.PublicKey]int
}

// Create queries the graph at depth 2 from the Orca program ID
// (program → WhirlpoolConfig → Whirlpool) and parses every pool account
// found, persisting results to db (orca_whirlpool_pool). If db already has
// pools from a previous run, they're loaded back instead of re-fetching,
// unless force is true.
func Create(ctx context.Context, stateClient state.Client, db *sql.DB, maxSubscriptionCount int, force bool, mintTracker *mintinfo.Tracker) (*Orca, error) {
	entry := logger.FromContext(ctx)
	orca := new(Orca)
	n, err := poolCount(db)
	if err != nil {
		return nil, fmt.Errorf("failed to check orca pool count: %s", err)
	}
	if n == 0 || force {
		if err = orca.fetchWhirlpool(ctx, stateClient, entry, maxSubscriptionCount, db, mintTracker, force); err != nil {
			return nil, fmt.Errorf("failed to load orca data: %s", err)
		}
		return orca, nil
	}
	orca.Pools, orca.mPool, err = loadPools(db)
	if err != nil {
		return nil, fmt.Errorf("failed to load orca data from db: %s", err)
	}
	return orca, nil
}

func (orca *Orca) Find(poolID sgo.PublicKey) *Whirlpool {
	i, present := orca.mPool[poolID]
	if !present {
		return nil
	}
	return orca.Pools[i]
}
