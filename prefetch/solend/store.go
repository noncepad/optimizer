package solend

import (
	"database/sql"
	"fmt"

	sgo "github.com/gagliardetto/solana-go"
)

// reserveCount returns how many reserves are already persisted, so Create
// can skip re-fetching from the chain on repeat runs against the same
// database.
func reserveCount(db *sql.DB) (int, error) {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM solend_reserve`).Scan(&n); err != nil {
		return 0, fmt.Errorf("solend: count reserves: %w", err)
	}
	return n, nil
}

// loadReserves reconstructs the in-memory reserve list from the database.
func loadReserves(db *sql.DB) ([]*Reserve, map[sgo.PublicKey]int, error) {
	rows, err := db.Query(`SELECT pubkey, lending_market, mint, supply_vault FROM solend_reserve`)
	if err != nil {
		return nil, nil, fmt.Errorf("solend: query reserves: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var reserves []*Reserve
	mReserve := make(map[sgo.PublicKey]int)
	for rows.Next() {
		var pubkey, lendingMarket, mint, supplyVault []byte
		if err = rows.Scan(&pubkey, &lendingMarket, &mint, &supplyVault); err != nil {
			return nil, nil, fmt.Errorf("solend: scan reserve: %w", err)
		}
		r := &Reserve{
			Pubkey:        sgo.PublicKeyFromBytes(pubkey),
			LendingMarket: sgo.PublicKeyFromBytes(lendingMarket),
			Mint:          sgo.PublicKeyFromBytes(mint),
			SupplyVault:   sgo.PublicKeyFromBytes(supplyVault),
		}
		mReserve[r.Pubkey] = len(reserves)
		reserves = append(reserves, r)
	}
	if err = rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("solend: iterate reserves: %w", err)
	}
	return reserves, mReserve, nil
}
