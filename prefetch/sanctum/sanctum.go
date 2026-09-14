// Package sanctum preloads Sanctum S Controller LST pool state.
package sanctum

import (
	"context"
	"database/sql"
	"fmt"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

var (
	ProgramID = sgo.MustPublicKeyFromBase58("5ocnV1qiCgaQR8Jb8xWnVbApfaygJ8tNoZfgPwsgx9kx")
)

// Sanctum holds pool state and LST entries loaded at startup.
type Sanctum struct {
	Lsts []*LstEntry
}

// Create queries the Sanctum S Controller PDAs and parses LST entries,
// persisting results to db (sanctum_lst). If db already has LSTs from a
// previous run, they're loaded back instead of re-fetching, unless force is
// true.
func Create(ctx context.Context, stateClient state.Client, db *sql.DB, force bool) (*Sanctum, error) {
	entry := logger.FromContext(ctx)
	s := new(Sanctum)
	n, err := lstCount(db)
	if err != nil {
		return nil, fmt.Errorf("failed to check sanctum lst count: %s", err)
	}
	if n == 0 || force {
		if err = s.fetch(ctx, stateClient, entry); err != nil {
			return nil, fmt.Errorf("failed to load sanctum data: %s", err)
		}
		if err = insertLsts(db, s.Lsts); err != nil {
			return nil, fmt.Errorf("failed to save sanctum data: %s", err)
		}
		return s, nil
	}
	s.Lsts, err = loadLsts(db)
	if err != nil {
		return nil, fmt.Errorf("failed to load sanctum data from db: %s", err)
	}
	return s, nil
}
