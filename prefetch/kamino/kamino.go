// Package kamino preloads Kamino Lending reserve accounts.
package kamino

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

const kaminoFilePath = "kamino.json"

// Create queries the Kamino Lending program at depth 2 (program → lending market → reserve)
// and collects all reserve accounts.
func Create(ctx context.Context, stateClient state.Client, workingDir string) (*Kamino, error) {
	entry := logger.FromContext(ctx)
	k := new(Kamino)
	fp := filepath.Join(workingDir, kaminoFilePath)
	f, err := os.Open(fp)
	if err != nil {
		err = k.fetch(ctx, stateClient, entry)
		if err != nil {
			return nil, fmt.Errorf("failed to load kamino data: %s", err)
		}
		f, err = os.Create(fp)
		if err != nil {
			return nil, fmt.Errorf("failed to save kamino data to %s: %s", fp, err)
		}
		err = json.NewEncoder(f).Encode(k)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to save kamino to file %s: %s", fp, err)
		}
	} else {
		err = json.NewDecoder(f).Decode(k)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to load kamino from %s: %s", fp, err)
		}
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
