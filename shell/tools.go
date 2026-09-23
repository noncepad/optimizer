package shell

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/portfolio"
	"git.noncepad.com/pkg/optimizer/util"
	"git.noncepad.com/pkg/solpipe-util/common"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	sgo "github.com/gagliardetto/solana-go"
	_ "modernc.org/sqlite"
)

// pnlInput is empty -- get_pnl takes no arguments, it always reports every
// bot portfolio.db knows about.
type pnlInput struct{}

// pnlTool wraps the exact same portfolio package the dashboard's PnL card
// uses (see cmd/pnl.go) -- not a reimplementation, the same Positions/Bots
// calls, just rendered as text for a model instead of HTML for a browser.
func pnlTool(portfolioDBPath string) (tool.BaseTool, error) {
	return utils.InferTool(
		"get_pnl",
		"Get current profit-and-loss for every bot recorded in portfolio.db: "+
			"starting vs. current USD value per mint, grouped by bot (one bot = one market+pipeline pair).",
		func(_ context.Context, _ pnlInput) (string, error) {
			db, err := portfolio.Open(portfolioDBPath)
			if err != nil {
				return "", fmt.Errorf("open portfolio db: %w", err)
			}
			defer func() {
				_ = db.Close()
			}()
			bots, err := db.Bots()
			if err != nil {
				return "", fmt.Errorf("list bots: %w", err)
			}
			if len(bots) == 0 {
				return "No bots have reported any balance snapshots yet.", nil
			}
			var b strings.Builder
			for i, bot := range bots {
				accounts, err := db.AccountsForBot(bot)
				if err != nil {
					return "", fmt.Errorf("accounts for bot: %w", err)
				}
				fmt.Fprintf(&b, "Bot %d (market %s):\n", i+1, bot.Market)
				any := false
				for _, account := range accounts {
					positions, err := db.Positions(account)
					if err != nil {
						return "", fmt.Errorf("positions for %s: %w", account, err)
					}
					for _, p := range positions {
						if !p.Market.Equals(bot.Market) || !p.Pipeline.Equals(bot.Pipeline) {
							continue
						}
						any = true
						if p.DeltaUSDValue != nil {
							fmt.Fprintf(&b, "  %s: $%.2f -> $%.2f (delta %+.2f)\n",
								p.Mint, deref(p.StartUSDValue), deref(p.LatestUSDValue), *p.DeltaUSDValue)
						} else {
							fmt.Fprintf(&b, "  %s: price not available yet for one end of the range\n", p.Mint)
						}
					}
				}
				if !any {
					b.WriteString("  (no priced positions yet)\n")
				}
			}
			return b.String(), nil
		},
	)
}

func deref(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// walletBalanceInput is empty -- get_wallet_balance always reports the
// configured trading wallet, there's only ever one per session.
type walletBalanceInput struct{}

// tradingChildKeyIndex is the derivation index every bot mode in this
// repo uses for its actual trading key (see arbv1/testperpv1/
// helloworldv1/perpfundingv1's eval.go -- all four call
// common.DeriveChildKeyFromIndex(hs.parentKey, 1) identically). The
// fee-payer/parent key only pays gas and authenticates with the bidder
// daemon; this derived child is what the WASM bot actually signs trades
// with, so it's the balance that reflects real trading activity.
const tradingChildKeyIndex = 1

// walletBalanceTool reports both the fee-payer (parent) key's balance and
// the derived trading key's balance, clearly labeled -- mirrors
// cmd/balance.go's query (util.FetchWalletBalance) for each, reusing one
// dialer/stateClient for both rather than dialing twice.
// Dials fresh per call rather than holding a live connection open for the
// whole chat session -- matches how `optimizer balance` itself works, a
// one-shot query. If feePayerPath is empty, the tool reports itself
// unconfigured instead of erroring -- so a shell session started without
// wallet credentials still works for the other tools.
func walletBalanceTool(feePayerPath string) (tool.BaseTool, error) {
	return utils.InferTool(
		"get_wallet_balance",
		"Get current SOL and SPL token balances, live from the chain, for both the fee-payer wallet "+
			"and the derived trading wallet the WASM bot actually trades with (these are different keys).",
		func(ctx context.Context, _ walletBalanceInput) (string, error) {
			if feePayerPath == "" {
				return "Wallet balance lookup is not configured for this session (no fee-payer key set).", nil
			}
			parentKey, err := sgo.PrivateKeyFromSolanaKeygenFile(feePayerPath)
			if err != nil {
				return "", fmt.Errorf("load fee payer: %w", err)
			}
			childKey := common.DeriveChildKeyFromIndex(parentKey, tradingChildKeyIndex)

			dialer, err := bidder.CreateDialer(ctx, parentKey)
			if err != nil {
				return "", fmt.Errorf("create dialer: %w", err)
			}
			stateClient := dialer.State()

			var b strings.Builder
			fmt.Fprintf(&b, "trading wallet (child key, index %d -- what the WASM bot actually trades with):\n", tradingChildKeyIndex)
			if err := describeWalletBalance(ctx, stateClient, childKey.PublicKey(), &b); err != nil {
				return "", fmt.Errorf("trading wallet: %w", err)
			}
			b.WriteString("\nfee-payer wallet (pays gas only, not traded from):\n")
			if err := describeWalletBalance(ctx, stateClient, parentKey.PublicKey(), &b); err != nil {
				return "", fmt.Errorf("fee-payer wallet: %w", err)
			}
			return b.String(), nil
		},
	)
}

// describeWalletBalance queries one pubkey's SOL + SPL token balances and
// appends a human-readable report to b.
func describeWalletBalance(ctx context.Context, stateClient state.Client, pubkey sgo.PublicKey, b *strings.Builder) error {
	balance, err := util.FetchWalletBalance(ctx, stateClient, pubkey)
	if err != nil {
		return fmt.Errorf("query failed: %w", err)
	}
	if !balance.Found {
		fmt.Fprintf(b, "  %s: no funds\n", pubkey)
		return nil
	}
	fmt.Fprintf(b, "  %s (slot %d)\n", balance.Pubkey, balance.Slot)
	fmt.Fprintf(b, "  SOL: %d lamports\n", balance.Lamports)
	for _, t := range balance.Tokens {
		fmt.Fprintf(b, "  token mint %s: amount %d\n", t.Mint, t.Amount)
	}
	return nil
}

// prefetchQueryInput is the shape the model fills in when it decides to
// call query_prefetch_db -- a single SQL string, same text-to-SQL pattern
// as the (now-removed) DeepSeek harness experiment, just live instead of
// batch-graded.
type prefetchQueryInput struct {
	SQL string `json:"sql" jsonschema:"description=A single read-only SQLite SELECT (or PRAGMA) query to run against prefetch.db."`
}

// prefetchTableSchemaHint lists prefetch.db's real tables (see
// store/db.go's migrate()) so the model can write a plausible query
// without a separate round-trip to discover the schema first -- qwen2.5:7b
// is small enough that minimizing round-trips matters.
const prefetchTableSchemaHint = "Tables: raydium_amm_pool, raydium_clmm_pool, raydium_cpmm_pool, " +
	"orca_whirlpool_pool, sanctum_lst, mint_info, kamino_reserve, marginfi_bank, solend_reserve, " +
	"drift_spot_market, jet_reserve, pumpfun_bonding_curve, pumpswap_pool, phoenix_market. " +
	"Pubkey columns are 32-byte BLOBs -- use hex(col) to read them, not the raw column."

// prefetchQueryTool lets the model write its own SQL and see the result --
// same idea cmd/harness.go's question set exercises, but live and
// open-ended instead of a fixed graded list. Enforced read-only two ways:
// only SELECT/PRAGMA statements are accepted, and PRAGMA query_only=ON is
// set on the connection before running anything, so even a write disguised
// as one of those is rejected by SQLite itself, not just by the string
// check.
func prefetchQueryTool(prefetchDBPath string) (tool.BaseTool, error) {
	return utils.InferTool(
		"query_prefetch_db",
		"Run a read-only SQL query against prefetch.db, the local cache of Solana DEX/lending pool data. "+prefetchTableSchemaHint,
		func(_ context.Context, in prefetchQueryInput) (string, error) {
			trimmed := strings.TrimSpace(strings.ToUpper(in.SQL))
			if !strings.HasPrefix(trimmed, "SELECT") && !strings.HasPrefix(trimmed, "PRAGMA") {
				return "", fmt.Errorf("only SELECT/PRAGMA queries are allowed")
			}
			db, err := sql.Open("sqlite", prefetchDBPath)
			if err != nil {
				return "", fmt.Errorf("open prefetch db: %w", err)
			}
			defer func() {
				_ = db.Close()
			}()
			if _, err := db.Exec("PRAGMA query_only = ON"); err != nil {
				return "", fmt.Errorf("set query_only: %w", err)
			}
			rows, err := db.Query(in.SQL)
			if err != nil {
				return "", fmt.Errorf("query failed: %w", err)
			}
			defer func() {
				_ = rows.Close()
			}()
			cols, err := rows.Columns()
			if err != nil {
				return "", err
			}
			var b strings.Builder
			b.WriteString(strings.Join(cols, " | ") + "\n")
			n := 0
			const maxRows = 50
			for rows.Next() && n < maxRows {
				vals := make([]interface{}, len(cols))
				ptrs := make([]interface{}, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				if err := rows.Scan(ptrs...); err != nil {
					return "", err
				}
				parts := make([]string, len(vals))
				for i, v := range vals {
					parts[i] = fmt.Sprintf("%v", v)
				}
				b.WriteString(strings.Join(parts, " | ") + "\n")
				n++
			}
			if err := rows.Err(); err != nil {
				return "", err
			}
			if n == 0 {
				return "(no rows)", nil
			}
			if n == maxRows {
				b.WriteString(fmt.Sprintf("(truncated at %d rows)\n", maxRows))
			}
			return b.String(), nil
		},
	)
}
