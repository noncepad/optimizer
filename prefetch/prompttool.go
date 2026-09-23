package prefetch

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/portfolio"
	"git.noncepad.com/pkg/optimizer/prefetch/orca"
	"git.noncepad.com/pkg/optimizer/prefetch/phoenix"
	"git.noncepad.com/pkg/optimizer/prefetch/pumpfun"
	"git.noncepad.com/pkg/optimizer/prefetch/pumpswap"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium/amm"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium/clmm"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium/cpmm"
	"git.noncepad.com/pkg/optimizer/prefetch/sanctum"
	"git.noncepad.com/pkg/optimizer/shell"
	"git.noncepad.com/pkg/optimizer/store"
	"git.noncepad.com/pkg/optimizer/util"
	"git.noncepad.com/pkg/solpipe-util/common"
	"github.com/cloudwego/eino-ext/components/model/ollama"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	sgo "github.com/gagliardetto/solana-go"
)

func (pf *Prefetcher) AppendPrompt(tools []tool.BaseTool) ([]tool.BaseTool, error) {
	return appendAllDexTopPoolTools(pf.storeDB.DB(), tools)
}

func appendPromptDBOnly(storeDB *store.DB, tools []tool.BaseTool) ([]tool.BaseTool, error) {
	return appendAllDexTopPoolTools(storeDB.DB(), tools)
}

// dexTopPoolQuery is the common shape every DEX package's Query exposes
// once CreateQuery(db) has prepared its statement.
type dexTopPoolQuery interface {
	TopPools(ctx context.Context, count int) (string, error)
}

// dexNames lists every valid dex_name value for get_top_pools, in the
// exact order the model sees them enumerated in the schema. Keep this in
// sync with the switch in appendAllDexTopPoolTools below -- add a new
// DEX's name here and to the switch when it gets a Query.
var dexNames = []string{
	"raydium_amm", "raydium_clmm", "raydium_cpmm", "orca_whirlpool",
	"sanctum_lst", "pumpfun", "pumpswap", "phoenix",
}

// topPoolsInput is the argument shape InferTool reflects into a JSON
// schema for the model to fill in when it calls get_top_pools. Folding
// every DEX behind one tool (instead of one tool per DEX) is deliberate:
// a small model (qwen2.5:7b) was observed either calling the wrong
// per-DEX tool or fabricating pool names outright when faced with 8
// similarly-named tools -- one tool with a constrained enum argument
// gives it far less room to get confused.
type topPoolsInput struct {
	PoolCount int    `json:"pool_count" jsonschema:"description=how many pools/markets to return"`
	DexName   string `json:"dex_name" jsonschema:"description=which DEX to query,enum=raydium_amm,enum=raydium_clmm,enum=raydium_cpmm,enum=orca_whirlpool,enum=sanctum_lst,enum=pumpfun,enum=pumpswap,enum=phoenix"`
}

// appendAllDexTopPoolTools builds every DEX package's Query up front, then
// wires them all behind a single get_top_pools tool that dispatches on
// dex_name. Add a new DEX by constructing its Query here, adding its name
// to dexNames above, and adding a case to the switch below.
func appendAllDexTopPoolTools(db *sql.DB, tools []tool.BaseTool) ([]tool.BaseTool, error) {
	qClmm, err := clmm.CreateQuery(db)
	if err != nil {
		return nil, err
	}
	qAmm, err := amm.CreateQuery(db)
	if err != nil {
		return nil, err
	}
	qCpmm, err := cpmm.CreateQuery(db)
	if err != nil {
		return nil, err
	}
	qOrca, err := orca.CreateQuery(db)
	if err != nil {
		return nil, err
	}
	qSanctum, err := sanctum.CreateQuery(db)
	if err != nil {
		return nil, err
	}
	qPumpfun, err := pumpfun.CreateQuery(db)
	if err != nil {
		return nil, err
	}
	qPumpswap, err := pumpswap.CreateQuery(db)
	if err != nil {
		return nil, err
	}
	qPhoenix, err := phoenix.CreateQuery(db)
	if err != nil {
		return nil, err
	}

	byName := map[string]dexTopPoolQuery{
		"raydium_amm":    qAmm,
		"raydium_clmm":   qClmm,
		"raydium_cpmm":   qCpmm,
		"orca_whirlpool": qOrca,
		"sanctum_lst":    qSanctum,
		"pumpfun":        qPumpfun,
		"pumpswap":       qPumpswap,
		"phoenix":        qPhoenix,
	}

	getTopPools, err := utils.InferTool(
		strings.Join([]string{prompt_PREFIX, "get_top_pools"}, "_"),
		"Get the top pools/markets for one DEX, ranked by that DEX's own liquidity proxy "+
			"(phoenix has no liquidity data, so it lists markets alphabetically instead). "+
			"Valid dex_name values: "+strings.Join(dexNames, ", ")+".",
		func(ctx context.Context, in topPoolsInput) (string, error) {
			q, ok := byName[in.DexName]
			if !ok {
				return "", fmt.Errorf("unknown dex_name %q, must be one of: %s", in.DexName, strings.Join(dexNames, ", "))
			}
			return q.TopPools(ctx, in.PoolCount)
		},
	)
	if err != nil {
		return nil, err
	}
	return append(tools, getTopPools), nil
}

const prompt_PREFIX = "prefetch"

type prefetchOnlyChat struct {
	ctx          context.Context
	model        string
	baseURL      string
	store        *store.DB
	feePayerPath string
	listTool     []tool.BaseTool
}

func CreatePrefetchOnlyChat(
	ctx context.Context,
	model string,
	baseURL string,
	store *store.DB,
	feePayerPath string,
) (shell.ChatBuilder, error) {
	poc := new(prefetchOnlyChat)
	poc.model = model
	poc.baseURL = baseURL
	poc.store = store
	poc.feePayerPath = feePayerPath
	poc.listTool = make([]tool.BaseTool, 0)
	var err error
	poc.listTool, err = appendPromptDBOnly(store, poc.listTool)
	if err != nil {
		return nil, err
	}
	return poc, nil
}

type prefetchPrompter struct {
	ctx   context.Context
	agent *react.Agent
}

func (ep *prefetchPrompter) Prompt(prompt string) (string, error) {
	msg, err := ep.agent.Generate(ep.ctx, []*schema.Message{schema.UserMessage(prompt)})
	if err != nil {
		return "", fmt.Errorf("agent.Generate: %w", err)
	}
	return msg.Content, nil
}

func (ecb *prefetchOnlyChat) Create(ctx context.Context) (shell.ChatPrompter, error) {
	var err error
	ep := new(prefetchPrompter)
	ep.ctx = ctx
	tools := ecb.listTool
	var chat *ollama.ChatModel
	chat, err = ollama.NewChatModel(ctx, &ollama.ChatModelConfig{
		BaseURL: ecb.baseURL,
		Model:   ecb.model,
	})
	if err != nil {
		return nil, fmt.Errorf("eino-ollama-agent: create ollama chat model: %v", err)
	}
	ep.agent, err = react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: chat,
		ToolsConfig:      compose.ToolsNodeConfig{Tools: tools},
	})
	if err != nil {
		return nil, fmt.Errorf("create react agent: %w", err)
	}
	return ep, nil
}

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

// timeInput/calcInput are the argument shapes InferTool reflects into a JSON
// schema for the model to fill in when it decides to call each tool.
type timeInput struct {
	TimeZone string `json:"time_zone" jsonschema:"description=IANA time zone name (e.g. America/New_York, Asia/Tokyo, UTC)"`
}

type calcInput struct {
	A  float64 `json:"a" jsonschema:"description=left-hand operand"`
	B  float64 `json:"b" jsonschema:"description=right-hand operand"`
	Op string  `json:"op" jsonschema:"description=one of + - * /,enum=+,enum=-,enum=*,enum=/"`
}

func demoTools() ([]tool.BaseTool, error) {
	getTime, err := utils.InferTool("get_time", "Get the current time in a given IANA time zone.",
		func(_ context.Context, in timeInput) (string, error) {
			loc, err := time.LoadLocation(in.TimeZone)
			if err != nil {
				return "", fmt.Errorf("unknown time zone %q: %w", in.TimeZone, err)
			}
			return time.Now().In(loc).Format(time.RFC1123), nil
		})
	if err != nil {
		return nil, err
	}

	calculate, err := utils.InferTool("calculate", "Perform one arithmetic operation on two numbers.",
		func(_ context.Context, in calcInput) (float64, error) {
			switch in.Op {
			case "+":
				return in.A + in.B, nil
			case "-":
				return in.A - in.B, nil
			case "*":
				return in.A * in.B, nil
			case "/":
				if in.B == 0 {
					return 0, fmt.Errorf("division by zero")
				}
				return in.A / in.B, nil
			default:
				return 0, fmt.Errorf("unsupported op %q", in.Op)
			}
		})
	if err != nil {
		return nil, err
	}

	return []tool.BaseTool{getTime, calculate}, nil
}
