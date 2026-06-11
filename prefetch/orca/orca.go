// Package orca preloads Orca Whirlpool trading pools.
package orca

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.noncepad.com/pkg/bot/state"
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

const orcaFilePath = "orca.json"

// Create queries the graph at depth 2 from the Orca program ID
// (program → WhirlpoolConfig → Whirlpool) and parses every pool account found.
func Create(ctx context.Context, stateClient state.Client, workingDir string) (*Orca, error) {
	entry := logger.FromContext(ctx)
	orca := new(Orca)
	fp := filepath.Join(workingDir, orcaFilePath)
	f, err := os.Open(fp)
	if err != nil {
		err = orca.fetchWhirlpool(ctx, stateClient, entry)
		if err != nil {
			return nil, fmt.Errorf("failed to load orca data: %s", err)
		}
		f, err = os.Create(fp)
		if err != nil {
			return nil, fmt.Errorf("failed to save orca data to %s: %s", fp, err)
		}
		err = json.NewEncoder(f).Encode(orca)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to save orca to file %s: %s", fp, err)
		}
	} else {
		err = json.NewDecoder(f).Decode(orca)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to load orca from %s: %s", fp, err)
		}
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
