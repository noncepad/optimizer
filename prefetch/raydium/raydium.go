// Package raydium preloads Raydium AMM v4 trading pools.
package raydium

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium/amm"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium/clmm"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium/cpmm"
	sgo "github.com/gagliardetto/solana-go"
)

var ProgramID = sgo.MustPublicKeyFromBase58("675kPX9MHTjS2zt1qfr1NYHuzeLXfQM9H24wFSUt1Mp8")

// Raydium holds all AMM v4 pools loaded at startup.
type Raydium struct {
	Amm  *amm.Configuration
	Clmm *clmm.Configuration
	Cpmm *cpmm.Configuration
}

const raydiumFilePath = "raydium_amm.json"

// Create queries the Raydium AMM program and parses every pool account found,
// then fetches the associated OpenBook market accounts for each pool.
func Create(ctx context.Context, stateClient state.Client, workingDir string) (*Raydium, error) {
	r := new(Raydium)
	fp := filepath.Join(workingDir, raydiumFilePath)
	f, err := os.Open(fp)
	if err != nil {
		r.Amm, err = amm.Download(ctx, stateClient)
		if err != nil {
			return nil, fmt.Errorf("failed to load raydium data: %s", err)
		}
		r.Cpmm, err = cpmm.Download(ctx, stateClient)
		if err != nil {
			return nil, fmt.Errorf("failed to load raydium data: %s", err)
		}
		r.Clmm, err = clmm.Download(ctx, stateClient)
		if err != nil {
			return nil, fmt.Errorf("failed to load raydium data: %s", err)
		}
		f, err = os.Create(fp)
		if err != nil {
			return nil, fmt.Errorf("failed to save raydium data to %s: %s", fp, err)
		}
		err = json.NewEncoder(f).Encode(r)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to save raydium to file %s: %s", fp, err)
		}
	} else {
		err = json.NewDecoder(f).Decode(r)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to load raydium from %s: %s", fp, err)
		}
	}
	return r, nil
}
