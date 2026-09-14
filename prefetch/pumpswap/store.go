package pumpswap

import (
	"database/sql"
	"fmt"

	sgo "github.com/gagliardetto/solana-go"
)

// poolCount returns how many pools are already persisted, so Create can
// skip re-fetching from the chain on repeat runs against the same
// database.
func poolCount(db *sql.DB) (int, error) {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pumpswap_pool`).Scan(&n); err != nil {
		return 0, fmt.Errorf("pumpswap: count pools: %w", err)
	}
	return n, nil
}

// upsertPool persists a newly-discovered pool's identity (mints, vaults,
// coin_creator) -- called the moment a Pool account is parsed, before
// either vault balance is known (both default to 0 until saveBalance
// updates them).
func upsertPool(tx *sql.Tx, p *Pool) error {
	_, err := tx.Exec(`INSERT OR REPLACE INTO pumpswap_pool
		(pool, base_mint, quote_mint, base_vault, quote_vault, coin_creator,
		 base_balance, quote_balance)
		VALUES (?,?,?,?,?,?,
		        COALESCE((SELECT base_balance FROM pumpswap_pool WHERE pool = ?), 0),
		        COALESCE((SELECT quote_balance FROM pumpswap_pool WHERE pool = ?), 0))`,
		p.Pool[:], p.BaseMint[:], p.QuoteMint[:], p.BaseVault[:], p.QuoteVault[:], p.CoinCreator[:],
		p.Pool[:], p.Pool[:],
	)
	if err != nil {
		return fmt.Errorf("pumpswap: upsert pool %s: %w", p.Pool, err)
	}
	return nil
}

// saveBalance updates one vault's balance column -- isBase selects which.
func saveBalance(tx *sql.Tx, pool sgo.PublicKey, isBase bool, amount uint64) error {
	col := "quote_balance"
	if isBase {
		col = "base_balance"
	}
	if _, err := tx.Exec(`UPDATE pumpswap_pool SET `+col+` = ? WHERE pool = ?`, int64(amount), pool[:]); err != nil {
		return fmt.Errorf("pumpswap: save balance for pool %s: %w", pool, err)
	}
	return nil
}

// loadPools reconstructs the in-memory pool list from the database.
func loadPools(db *sql.DB) ([]*Pool, error) {
	rows, err := db.Query(`SELECT pool, base_mint, quote_mint, base_vault, quote_vault,
		coin_creator, base_balance, quote_balance FROM pumpswap_pool`)
	if err != nil {
		return nil, fmt.Errorf("pumpswap: query pools: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var out []*Pool
	for rows.Next() {
		var pool, baseMint, quoteMint, baseVault, quoteVault, coinCreator []byte
		p := new(Pool)
		if err = rows.Scan(&pool, &baseMint, &quoteMint, &baseVault, &quoteVault,
			&coinCreator, &p.BaseBalance, &p.QuoteBalance); err != nil {
			return nil, fmt.Errorf("pumpswap: scan pool: %w", err)
		}
		p.Pool = sgo.PublicKeyFromBytes(pool)
		p.BaseMint = sgo.PublicKeyFromBytes(baseMint)
		p.QuoteMint = sgo.PublicKeyFromBytes(quoteMint)
		p.BaseVault = sgo.PublicKeyFromBytes(baseVault)
		p.QuoteVault = sgo.PublicKeyFromBytes(quoteVault)
		p.CoinCreator = sgo.PublicKeyFromBytes(coinCreator)
		out = append(out, p)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("pumpswap: iterate pools: %w", err)
	}
	return out, nil
}
