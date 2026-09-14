package sanctum

import (
	_ "embed"
	"fmt"

	"github.com/BurntSushi/toml"
	sgo "github.com/gagliardetto/solana-go"
)

// Vendored snapshot of igneous-labs/sanctum-lst-list's sanctum-lst-list.toml
// -- see that file's own header comment for provenance/refresh instructions.
//
//go:embed sanctum-lst-list.toml
var lstListTOML []byte

// lstListFile mirrors just the fields of sanctum-lst-list.toml needed to
// resolve a "generic pool calculator" LST's underlying stake-pool address.
type lstListFile struct {
	Entries []lstListEntry `toml:"sanctum_lst_list"`
}

type lstListEntry struct {
	Mint string         `toml:"mint"`
	Pool lstListPoolRef `toml:"pool"`
}

type lstListPoolRef struct {
	Program string `toml:"program"`
	Pool    string `toml:"pool"`
}

// poolStateRegistry maps an LST mint to its stake-pool ("pool_state")
// address, for the three SOL-value-calculator kinds that genuinely need
// per-LST data (SanctumSpl, SanctumSplMulti, Spl) -- Marinade/Lido/Wsol
// have a single fixed pool_state (or none), resolved entirely on the
// Rust side without this registry. Built once from the vendored
// sanctum-lst-list.toml snapshot; entries for any other "program" kind,
// or with unparseable pubkeys, are skipped.
func poolStateRegistry() (map[sgo.PublicKey]sgo.PublicKey, error) {
	var f lstListFile
	if _, err := toml.Decode(string(lstListTOML), &f); err != nil {
		return nil, fmt.Errorf("sanctum: decode vendored lst list: %w", err)
	}
	reg := make(map[sgo.PublicKey]sgo.PublicKey, len(f.Entries))
	for _, e := range f.Entries {
		switch e.Pool.Program {
		case "SanctumSpl", "SanctumSplMulti", "Spl":
		default:
			continue
		}
		mint, err := sgo.PublicKeyFromBase58(e.Mint)
		if err != nil {
			continue
		}
		pool, err := sgo.PublicKeyFromBase58(e.Pool.Pool)
		if err != nil {
			continue
		}
		reg[mint] = pool
	}
	return reg, nil
}
