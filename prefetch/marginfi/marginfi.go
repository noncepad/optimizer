// Package marginfi preloads marginfi-v2 Bank accounts (global market data
// only -- MarginfiAccount, the per-wallet position account, is not fetched
// here since there is no bounded set to bulk-load).
package marginfi

import (
	"context"
	"database/sql"
	"fmt"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

var (
	ProgramID = sgo.MustPublicKeyFromBase58("MFv2hWf31Z9kbCa1snEPYctwafyhdvnV7FZnsebVacA")
)

// Discriminators for Anchor account types.
var (
	DiscMarginfiGroup = [8]byte{182, 23, 173, 240, 151, 206, 182, 67}
	DiscBank          = [8]byte{142, 49, 166, 242, 50, 66, 97, 188}
)

// MarginFi holds all marginfi-v2 Bank accounts loaded at startup.
type MarginFi struct {
	Banks []*Bank
	mBank map[sgo.PublicKey]int
}

// Create discovers marginfi-v2 groups and banks via an active,
// per-node Subscribe/ack chain (program -> group, then group -> bank; see
// event.go, which mirrors orca.fetchWhirlpool's own config->pool chain)
// and collects all bank accounts, persisting results to db
// (marginfi_bank). If db already has banks from a previous run, they're
// loaded back instead of re-fetching, unless force is true.
func Create(ctx context.Context, stateClient state.Client, db *sql.DB, force bool) (*MarginFi, error) {
	entry := logger.FromContext(ctx)
	m := new(MarginFi)
	n, err := bankCount(db)
	if err != nil {
		return nil, fmt.Errorf("failed to check marginfi bank count: %s", err)
	}
	if n == 0 || force {
		if err = m.fetch(ctx, stateClient, db, entry); err != nil {
			return nil, fmt.Errorf("failed to load marginfi data: %s", err)
		}
		return m, nil
	}
	m.Banks, m.mBank, err = loadBanks(db)
	if err != nil {
		return nil, fmt.Errorf("failed to load marginfi data from db: %s", err)
	}
	return m, nil
}

// Find looks up a bank by its pubkey.
func (m *MarginFi) Find(bankID sgo.PublicKey) *Bank {
	i, present := m.mBank[bankID]
	if !present {
		return nil
	}
	return m.Banks[i]
}
