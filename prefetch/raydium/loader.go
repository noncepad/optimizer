package raydium

import (
	"database/sql"
)

// Loader is a prefetch.StaticLoader that snapshots the live raydium.db
// (already populated in place by Create, via amm/clmm/cpmm's CommitStart/
// CommitFinish transactions) out to the wasm build's target directory.
type Loader struct {
	db *sql.DB
}

// NewLoader wraps the already-open raydium database for use as a StaticLoader.
func NewLoader(db *sql.DB) *Loader {
	return &Loader{db: db}
}

func (l *Loader) Args(args []string) ([]string, error) {
	return args, nil
}

func (l *Loader) Env(mEnv map[string]string) error {
	return nil
}
