package marginfi

import (
	"database/sql"
	"fmt"

	sgo "github.com/gagliardetto/solana-go"
)

// bankCount returns how many banks are already persisted, so Create can
// skip re-fetching from the chain on repeat runs against the same
// database.
func bankCount(db *sql.DB) (int, error) {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM marginfi_bank`).Scan(&n); err != nil {
		return 0, fmt.Errorf("marginfi: count banks: %w", err)
	}
	return n, nil
}

// loadBanks reconstructs the in-memory bank list from the database.
func loadBanks(db *sql.DB) ([]*Bank, map[sgo.PublicKey]int, error) {
	rows, err := db.Query(`SELECT pubkey, group_pubkey, mint, oracle_setup, oracle_key FROM marginfi_bank`)
	if err != nil {
		return nil, nil, fmt.Errorf("marginfi: query banks: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var banks []*Bank
	mBank := make(map[sgo.PublicKey]int)
	for rows.Next() {
		var pubkey, group, mint, oracleKey []byte
		var oracleSetup uint8
		if err = rows.Scan(&pubkey, &group, &mint, &oracleSetup, &oracleKey); err != nil {
			return nil, nil, fmt.Errorf("marginfi: scan bank: %w", err)
		}
		b := &Bank{
			Pubkey:      sgo.PublicKeyFromBytes(pubkey),
			Group:       sgo.PublicKeyFromBytes(group),
			Mint:        sgo.PublicKeyFromBytes(mint),
			OracleSetup: oracleSetup,
			OracleKey:   sgo.PublicKeyFromBytes(oracleKey),
		}
		mBank[b.Pubkey] = len(banks)
		banks = append(banks, b)
	}
	if err = rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("marginfi: iterate banks: %w", err)
	}
	return banks, mBank, nil
}
