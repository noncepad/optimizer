// Package phoenix preloads Phoenix perpetuals market identity (symbol,
// asset_id, market_account) into prefetch.db.
//
// Unlike every other prefetch package (pumpswap, pumpfun, kamino, ...),
// this does NOT discover markets live from the chain -- it just loads the
// same small, fixed market list `phoenix.json` used to carry directly
// (see market.go's fixedMarkets), skipping the subscribe/parse machinery
// entirely. That's a deliberate simplification: Phoenix has ~65 live
// markets and no edge-generator FilterEdges are needed to reach any of
// them (GlobalConfiguration is a fixed address that embeds PerpAssetMap's
// pubkey directly), so live discovery only trades a bit of code for
// automatically picking up new markets -- not worth it for now.
package phoenix

import (
	"context"
	"database/sql"
	"fmt"

	"git.noncepad.com/pkg/bot/state"
	sgo "github.com/gagliardetto/solana-go"
)

var ProgramID = sgo.MustPublicKeyFromBase58("EtrnLzgbS7nMMy5fbD42kXiUzGg8XQzJ972Xtk1cjWih")

// Phoenix holds markets loaded at startup.
type Phoenix struct {
	Markets []*Market
}

// Create loads Phoenix's fixed market list (fixedMarkets) into db
// (phoenix_market). If db already has markets from a previous run,
// they're loaded back instead of re-inserting, unless force is true.
// ctx/stateClient/maxSubscriptionCount are accepted only to match every
// other prefetch package's Create signature (so cmd/download.go's
// dispatch doesn't need a special case) -- unused here since there's no
// live chain subscription.
func Create(ctx context.Context, stateClient state.Client, db *sql.DB, maxSubscriptionCount int, force bool) (*Phoenix, error) {
	p := new(Phoenix)
	n, err := marketCount(db)
	if err != nil {
		return nil, fmt.Errorf("failed to check phoenix market count: %s", err)
	}
	if n == 0 || force {
		if err = insertMarkets(db, fixedMarkets); err != nil {
			return nil, fmt.Errorf("failed to save phoenix data: %s", err)
		}
		p.Markets = fixedMarkets
		return p, nil
	}
	p.Markets, err = loadMarkets(db)
	if err != nil {
		return nil, fmt.Errorf("failed to load phoenix data from db: %s", err)
	}
	return p, nil
}
