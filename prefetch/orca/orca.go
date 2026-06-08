// Package orca preloads Orca Whirlpool trading pools.
package orca

import (
	"context"
	"fmt"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/solpipe-util/graph"
	sgo "github.com/gagliardetto/solana-go"
)

var (
	ProgramID     = sgo.MustPublicKeyFromBase58("whirLbMiicVdio4qvUfM5KAg6Ct8VwpYzGff3uctyCc")
	CheckPoolID   = sgo.MustPublicKeyFromBase58("Czfq3xZZDmsdGdUyrNLtRhGc47cXcZtLG4crryfu44zE")
	CheckConfigID = sgo.MustPublicKeyFromBase58("2LecshUwdy9xi7meFgHtFJQNSKk4KdTrcpvaB56dP2NQ")
)

var (
	DiscriminatorWhirlpool = [8]uint8{63, 149, 209, 12, 225, 128, 99, 9}
	DiscriminatorTickArray = [8]uint8{69, 97, 189, 190, 110, 7, 66, 187}
)

// Orca holds all Whirlpool pools loaded at startup.
type Orca struct {
	Pools []*Whirlpool
	mPool map[sgo.PublicKey]int
	em    *graph.EdgeManager
}

// Create queries the graph at depth 2 from the Orca program ID
// (program → WhirlpoolConfig → Whirlpool) and parses every pool account found.
func Create(ctx context.Context, stateClient state.Client) (*Orca, error) {
	em, err := stateClient.QuerySingleShot(
		ctx, []state.QueryRequest{
			{
				Root:         ProgramID,
				FilterWeight: graph.WeightAll,
				Depth:        0,
			},
			{
				Root:         CheckConfigID,
				FilterWeight: graph.WeightAll,
				Depth:        0,
			},
			{
				Root:         CheckPoolID,
				FilterWeight: graph.WeightAll,
				Depth:        0,
			},
		},
	)
	if err != nil {
		return nil, fmt.Errorf("query failed: %s", err)
	}

	// depth-1 children of the program are WhirlpoolConfig accounts
	em.Lock()
	configs := em.EdgeDown(ProgramID)
	em.Unlock()
	gotOrcaConfigCheck := false
	gotOrcaPoolCheck := false
	var pools []*Whirlpool
	var disc [8]uint8
	mPool := make(map[sgo.PublicKey]int)
	for configPubkey := range configs {
		if configPubkey.Equals(CheckConfigID) {
			gotOrcaConfigCheck = true
		}
		// depth-2 children of each config are Whirlpool pool accounts
		em.Lock()
		poolCandidates := em.EdgeDown(configPubkey)
		em.Unlock()

		for poolPubkey := range poolCandidates {
			if poolPubkey.Equals(CheckPoolID) {
				gotOrcaPoolCheck = true
			}
			em.Lock()
			a := em.UnsafeAccount(poolPubkey)
			em.Unlock()
			if a == nil {
				continue
			}
			data := a.Data()
			if len(data) < 8 {
				continue
			}
			copy(disc[:], data[:8])
			if disc != DiscriminatorWhirlpool {
				continue
			}
			pool, err := parseWhirlpool(poolPubkey, data)
			if err != nil {
				continue
			}
			mPool[poolPubkey] = len(pools)
			pools = append(pools, pool)
		}
	}
	if !gotOrcaConfigCheck {
		return nil, fmt.Errorf("missing config %s", CheckConfigID)
	}
	if !gotOrcaPoolCheck {
		return nil, fmt.Errorf("missing pool %s", CheckPoolID)
	}
	return &Orca{Pools: pools, em: em, mPool: mPool}, nil
}

func (orca *Orca) Find(poolID sgo.PublicKey) *Whirlpool {
	i, present := orca.mPool[poolID]
	if !present {
		return nil
	}
	return orca.Pools[i]
}
