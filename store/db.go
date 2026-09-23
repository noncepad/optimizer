// Package store persists prefetched on-chain data to a local SQLite database.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"git.noncepad.com/pkg/optimizer/bundler"
	"git.noncepad.com/pkg/optimizer/prefetch/alt"
	"git.noncepad.com/pkg/optimizer/prefetch/drift"
	"git.noncepad.com/pkg/optimizer/prefetch/jet"
	"git.noncepad.com/pkg/optimizer/prefetch/kamino"
	lstyield "git.noncepad.com/pkg/optimizer/prefetch/lst-yield"
	"git.noncepad.com/pkg/optimizer/prefetch/marginfi"
	"git.noncepad.com/pkg/optimizer/prefetch/mintinfo"
	"git.noncepad.com/pkg/optimizer/prefetch/multimodel"
	"git.noncepad.com/pkg/optimizer/prefetch/obligation"
	"git.noncepad.com/pkg/optimizer/prefetch/orca"
	"git.noncepad.com/pkg/optimizer/prefetch/perpfunding"
	"git.noncepad.com/pkg/optimizer/prefetch/phoenix"
	"git.noncepad.com/pkg/optimizer/prefetch/pnl"
	"git.noncepad.com/pkg/optimizer/prefetch/pumpfun"
	"git.noncepad.com/pkg/optimizer/prefetch/pumpswap"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium/amm"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium/clmm"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium/cpmm"
	"git.noncepad.com/pkg/optimizer/prefetch/sanctum"
	"git.noncepad.com/pkg/optimizer/prefetch/solend"
	_ "modernc.org/sqlite"
)

func ValidateSQL(query string) error {
	q := strings.TrimSpace(query)

	upper := strings.ToUpper(q)

	// Only allow SELECT.
	if !strings.HasPrefix(upper, "SELECT") {
		return fmt.Errorf("only SELECT statements are allowed")
	}

	// Reject multiple statements.
	if strings.Contains(q, ";") {
		return fmt.Errorf("multiple SQL statements are not allowed")
	}

	// Defense in depth.
	for _, forbidden := range []string{
		"INSERT ",
		"UPDATE ",
		"DELETE ",
		"DROP ",
		"ALTER ",
		"CREATE ",
		"ATTACH ",
		"DETACH ",
		"PRAGMA ",
		"VACUUM ",
		"REPLACE ",
	} {
		if strings.Contains(upper, forbidden) {
			return fmt.Errorf(
				"forbidden SQL keyword: %s",
				strings.TrimSpace(forbidden),
			)
		}
	}

	return nil
}

// DB wraps a SQLite connection and owns the schema lifecycle.
type DB struct {
	path string
	db   *sql.DB
}

// Query runs a caller-supplied, read-only SQL statement against the
// database and returns each row as a column-name-keyed map. ValidateSQL
// gates this to SELECT-only (no INSERT/UPDATE/DELETE/DDL/etc.) since,
// unlike every other method on DB, the query string itself is not a
// fixed literal in this codebase -- see e.g. the pnl agent's
// text-to-SQL node, which hands this a model-generated query.
func (d *DB) Query(
	ctx context.Context,
	query string,
	args []any,
) ([]map[string]any, error) {
	if err := ValidateSQL(query); err != nil {
		return nil, err
	}

	rows, err := d.db.QueryContext(
		ctx,
		query,
		args...,
	)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var results []map[string]any

	for rows.Next() {
		values := make([]any, len(columns))
		valuePtrs := make([]any, len(columns))

		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, err
		}

		row := make(map[string]any)

		for i, column := range columns {
			row[column] = values[i]
		}

		results = append(results, row)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return results, nil
}

// Open opens (or creates) the SQLite database at path and applies the Raydium schema.
func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	s := &DB{db: db, path: path}
	if err = s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Raw exposes the underlying *sql.DB for packages under prefetch/ whose
// store functions operate on a raw connection rather than this wrapper
// (e.g. perpfunding.SetTargetAllocation).
func (s *DB) Raw() *sql.DB {
	return s.db
}

func (s *DB) Schema(ctx context.Context) (string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT sql
		FROM sqlite_master
		WHERE type = 'table'
		  AND name NOT LIKE 'sqlite_%'
		ORDER BY name
	`)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = rows.Close()
	}()

	var schemas []string

	for rows.Next() {
		var ddl sql.NullString

		if err := rows.Scan(&ddl); err != nil {
			return "", err
		}

		if ddl.Valid {
			schemas = append(schemas, ddl.String)
		}
	}

	return strings.Join(schemas, "\n\n"), rows.Err()
}

func (s *DB) migrate() error {
	for _, ddl := range []string{
		cpmm.Schema, clmm.Schema, amm.Schema, orca.Schema, kamino.Schema, sanctum.Schema, mintinfo.Schema,
		marginfi.Schema, solend.Schema, drift.Schema, jet.Schema, pumpfun.Schema, pumpswap.Schema,
		bundler.Schema,
		phoenix.Schema, perpfunding.Schema, alt.Schema, pnl.Schema, lstyield.Schema, multimodel.Schema,
		obligation.Schema,
	} {
		if _, err := s.db.Exec(ddl); err != nil {
			return fmt.Errorf("store: migrate: %w", err)
		}
	}
	// CREATE TABLE IF NOT EXISTS (above) only creates tables from scratch --
	// it's a no-op against a table that already exists from before a schema
	// change, so a column added to schema.sql after a table first shipped
	// (like last_slot, added for freshness-based dedup) needs an explicit,
	// idempotent ALTER TABLE here or every eventHandler's last_slot
	// INSERT/UPDATE would fail against any prefetch.db created before this
	// column existed.
	for _, m := range []struct{ table, column, ddl string }{
		{"raydium_amm_pool", "last_slot", "INTEGER NOT NULL DEFAULT 0"},
		{"raydium_cpmm_pool", "last_slot", "INTEGER NOT NULL DEFAULT 0"},
		{"raydium_cpmm_config", "last_slot", "INTEGER NOT NULL DEFAULT 0"},
		{"raydium_clmm_pool", "last_slot", "INTEGER NOT NULL DEFAULT 0"},
		{"raydium_clmm_config", "last_slot", "INTEGER NOT NULL DEFAULT 0"},
		{"orca_whirlpool_pool", "last_slot", "INTEGER NOT NULL DEFAULT 0"},
		{"orca_whirlpool_pool", "vault_a_balance", "INTEGER"},
		{"orca_whirlpool_pool", "vault_b_balance", "INTEGER"},
		{"marginfi_bank", "last_slot", "INTEGER NOT NULL DEFAULT 0"},
		{"solend_reserve", "last_slot", "INTEGER NOT NULL DEFAULT 0"},
		{"sanctum_lst", "pool_state", "BLOB"},
		{"sanctum_lst", "reserve", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := s.addColumnIfMissing(m.table, m.column, m.ddl); err != nil {
			return fmt.Errorf("store: migrate: add %s.%s: %w", m.table, m.column, err)
		}
	}
	return nil
}

// addColumnIfMissing runs ALTER TABLE <table> ADD COLUMN <column> <ddl>
// exactly once -- SQLite has no "ADD COLUMN IF NOT EXISTS", and re-running
// it against a column that's already there errors ("duplicate column
// name"), so table_info is checked first to keep this idempotent across
// repeated store.Open calls.
func (s *DB) addColumnIfMissing(table, column, ddl string) error {
	rows, err := s.db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return err
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt sql.NullString
		if err = rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	_, err = s.db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, ddl))
	return err
}

// DB returns the underlying *sql.DB for passing directly to sub-packages.
func (s *DB) FilePath() string {
	return s.path
}

// DB returns the underlying *sql.DB for passing directly to sub-packages.
func (s *DB) DB() *sql.DB {
	return s.db
}

// Close closes the underlying database connection.
func (s *DB) Close() error {
	return s.db.Close()
}
