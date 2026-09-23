package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/prefetch/obligation"
	"git.noncepad.com/pkg/optimizer/util"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	sgo "github.com/gagliardetto/solana-go"
)

// wrappedSolMint is Jupiter's convention for "native SOL" as a priceable
// mint -- there is no real SPL mint for lamports, but every price/quote
// API in this codebase (see the manual repair/sweep scripts this harness
// is distilled from) accepts this address to mean native SOL.
const wrappedSolMint = "So11111111111111111111111111111111111111112"

// noInput is used for tools that need no arguments -- utils.InferTool
// still wants some input struct to reflect a (trivial, empty) JSON
// schema from, same shape as timeInput/calcInput in the reference
// example, just with zero fields.
type noInput struct{}

// WalletTools builds the read-only wallet-inspection toolkit this harness
// exposes to the agent, distilled directly from the ad hoc Go scripts run
// by hand during this session's live unwind of a real Solana trading
// wallet (checkob/main.go, the balance checks around the sweep-to-USDC
// step, and the portfolio-value computation) -- see demoTools() in
// go-wiki/examples/eino-ollama-agent/main.go for the shape this mirrors
// (utils.InferTool + a plain Go function per tool).
//
// Every tool here is READ-ONLY: it queries live on-chain/off-chain state
// and never signs or sends anything. The real manual-repair actions from
// that session (hand-built Kamino/Solend repay/withdraw instructions,
// sweeping dust tokens to USDC via Jupiter) are deliberately NOT exposed
// as callable tools here -- those move real money, and in the session
// they were only ever run after an explicit, per-action human go-ahead
// in conversation ("go ahead and build it", "go ahead and send it").
// Wiring them into a freely-callable agent tool would remove that human
// gate entirely; this harness intentionally stops at "let the model see
// what a human would see," not "let the model act."
func WalletTools(stateClient state.Client, owner sgo.PublicKey) ([]tool.BaseTool, error) {
	checkObligations, err := utils.InferTool(
		"check_obligations",
		"List every tracked Solend/Kamino lending obligation (pair, directional, and hawkes trade types, on both protocols) for this bot's trading wallet, with real deposit/borrow amounts read live from mainnet. Amounts are raw on-chain units (not decimal-adjusted) -- USDC has 6 decimals, most other reserves 6 or 9.",
		func(ctx context.Context, _ noInput) (string, error) {
			return checkObligationsImpl(ctx, stateClient, owner)
		})
	if err != nil {
		return nil, fmt.Errorf("infer check_obligations: %w", err)
	}

	getWalletBalances, err := utils.InferTool(
		"get_wallet_balances",
		"List the trading wallet's native SOL balance and every non-zero SPL token balance it currently holds, read live from mainnet. Amounts are raw on-chain units alongside a decimal-adjusted UI amount.",
		func(ctx context.Context, _ noInput) (string, error) {
			return getWalletBalancesImpl(ctx, stateClient, owner)
		})
	if err != nil {
		return nil, fmt.Errorf("infer get_wallet_balances: %w", err)
	}

	getTokenPrice, err := utils.InferTool(
		"get_token_price_usd",
		"Get live USD prices for one or more Solana token mints via Jupiter's price API. Use \"So11111111111111111111111111111111111111112\" for native SOL. Mints Jupiter has no liquidity/route for come back marked as unpriced, not an error.",
		func(ctx context.Context, in priceInput) (string, error) {
			return getTokenPriceImpl(in.Mints)
		})
	if err != nil {
		return nil, fmt.Errorf("infer get_token_price_usd: %w", err)
	}

	getPortfolioValue, err := utils.InferTool(
		"get_portfolio_value_usd",
		"Compute the trading wallet's total current value in USD: every non-zero SPL token balance plus native SOL, each priced live via Jupiter, summed. Tokens with no Jupiter route are reported separately as untradeable dust and excluded from the total, not silently dropped.",
		func(ctx context.Context, _ noInput) (string, error) {
			return getPortfolioValueImpl(ctx, stateClient, owner)
		})
	if err != nil {
		return nil, fmt.Errorf("infer get_portfolio_value_usd: %w", err)
	}

	return []tool.BaseTool{checkObligations, getWalletBalances, getTokenPrice, getPortfolioValue}, nil
}

// checkObligationsImpl mirrors checkob/main.go exactly: derive each
// tracked obligation's deterministic address, fetch it live via
// util.FetchAccount (a single depth-1 subscription per obligation, the
// same internal state.Client/gRPC graph every other read in this
// codebase uses -- not the public Solana RPC endpoint), and parse
// deposits/borrows with the same obligation.ParseSolend/ParseKamino this
// bot's own Go side uses.
func checkObligationsImpl(ctx context.Context, stateClient state.Client, owner sgo.PublicKey) (string, error) {
	var sb strings.Builder
	for _, t := range obligation.TrackedObligations {
		addr, err := obligation.Address(owner, t.Protocol, t.ID)
		if err != nil {
			fmt.Fprintf(&sb, "%s/%s (id=%d): failed to derive address: %s\n", t.Protocol, t.TradeType, t.ID, err)
			continue
		}

		account, found, err := util.FetchAccount(ctx, stateClient, addr)
		if err != nil {
			return "", fmt.Errorf("fetch %s/%s obligation %s: %w", t.Protocol, t.TradeType, addr, err)
		}
		if !found {
			fmt.Fprintf(&sb, "%s/%s (id=%d) %s: account does not exist (never opened)\n", t.Protocol, t.TradeType, t.ID, addr)
			continue
		}

		data := account.Data()
		var parsed *obligation.Parsed
		switch t.Protocol {
		case obligation.ProtocolSolend:
			parsed, err = obligation.ParseSolend(data)
		case obligation.ProtocolKamino:
			parsed, err = obligation.ParseKamino(data)
		}
		if err != nil {
			fmt.Fprintf(&sb, "%s/%s (id=%d) %s: parse failed: %s\n", t.Protocol, t.TradeType, t.ID, addr, err)
			continue
		}

		if len(parsed.Deposits) == 0 && len(parsed.Borrows) == 0 {
			fmt.Fprintf(&sb, "%s/%s (id=%d) %s: empty (no deposits, no borrows)\n", t.Protocol, t.TradeType, t.ID, addr)
			continue
		}
		fmt.Fprintf(&sb, "%s/%s (id=%d) %s:\n", t.Protocol, t.TradeType, t.ID, addr)
		for _, d := range parsed.Deposits {
			fmt.Fprintf(&sb, "  deposit reserve=%s amount=%d\n", d.Reserve, d.Amount)
		}
		for _, b := range parsed.Borrows {
			fmt.Fprintf(&sb, "  borrow  reserve=%s amount=%d\n", b.Reserve, b.Amount)
		}
	}
	return sb.String(), nil
}

type walletBalance struct {
	Mint  string
	Raw   string
	UI    string
	IsSOL bool
	// UIKnown is false when the mint's decimals couldn't be resolved (see
	// util.TokenBalance.DecimalsKnown) -- UI is a human-readable notice,
	// not a number, in that case, and callers computing USD value must
	// treat this balance as unpriceable rather than parsing UI as 0.
	UIKnown bool
}

// fetchWalletBalances returns native SOL (as a synthetic wrappedSolMint
// entry, IsSOL=true) followed by every non-zero SPL token balance, via
// util.FetchWalletBalance -- the same internal state.Client/gRPC graph
// every other read in this codebase uses, not the public Solana RPC
// endpoint (see util/walletbalance.go's own doc comment for why: a raw
// RPC client's reliability issues were the prime suspect behind a
// recurring multi-hour silent freeze elsewhere in this codebase).
func fetchWalletBalances(ctx context.Context, stateClient state.Client, owner sgo.PublicKey) ([]walletBalance, error) {
	wb, err := util.FetchWalletBalance(ctx, stateClient, owner)
	if err != nil {
		return nil, fmt.Errorf("fetch wallet balance: %w", err)
	}

	var out []walletBalance
	if wb.Found && wb.Lamports > 0 {
		out = append(out, walletBalance{
			Mint:    wrappedSolMint,
			Raw:     fmt.Sprintf("%d", wb.Lamports),
			UI:      fmt.Sprintf("%.9f", float64(wb.Lamports)/1e9),
			IsSOL:   true,
			UIKnown: true,
		})
	}
	for _, t := range wb.Tokens {
		if t.Amount == 0 {
			continue
		}
		b := walletBalance{Mint: t.Mint.String(), Raw: fmt.Sprintf("%d", t.Amount)}
		if t.DecimalsKnown {
			b.UI = fmt.Sprintf("%.*f", int(t.Decimals), float64(t.Amount)/pow10(t.Decimals))
			b.UIKnown = true
		} else {
			b.UI = "unknown (mint decimals unresolved)"
		}
		out = append(out, b)
	}
	return out, nil
}

// pow10 returns 10^n as a float64, for decimal-adjusting a raw token
// amount by its mint's decimals.
func pow10(n uint8) float64 {
	v := 1.0
	for range n {
		v *= 10
	}
	return v
}

func getWalletBalancesImpl(ctx context.Context, stateClient state.Client, owner sgo.PublicKey) (string, error) {
	balances, err := fetchWalletBalances(ctx, stateClient, owner)
	if err != nil {
		return "", err
	}
	if len(balances) == 0 {
		return fmt.Sprintf("wallet %s holds no SOL and no non-zero SPL token balances", owner), nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "wallet %s:\n", owner)
	for _, b := range balances {
		label := b.Mint
		if b.IsSOL {
			label = "native SOL"
		}
		fmt.Fprintf(&sb, "  %-46s raw=%-16s ui=%s\n", label, b.Raw, b.UI)
	}
	return sb.String(), nil
}

type priceInput struct {
	Mints []string `json:"mints" jsonschema:"description=One or more Solana token mint addresses (base58) to fetch live USD prices for. Use So11111111111111111111111111111111111111112 for native SOL."`
}

type jupiterPriceEntry struct {
	UsdPrice float64 `json:"usdPrice"`
}

// fetchJupiterPrices calls Jupiter's price API for a batch of mints,
// returning only the ones it actually priced -- a mint absent from the
// response has no liquidity/route, same convention the manual sweep
// script (sweeptousdc/main.go) treated as "not tradable", not an error.
func fetchJupiterPrices(mints []string) (map[string]float64, error) {
	if len(mints) == 0 {
		return nil, nil
	}
	url := "https://lite-api.jup.ag/price/v3?ids=" + strings.Join(mints, ",")
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var parsed map[string]jupiterPriceEntry
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode jupiter price response: %w (body=%s)", err, string(body))
	}
	out := make(map[string]float64, len(parsed))
	for mint, entry := range parsed {
		out[mint] = entry.UsdPrice
	}
	return out, nil
}

func getTokenPriceImpl(mints []string) (string, error) {
	if len(mints) == 0 {
		return "", fmt.Errorf("no mints given")
	}
	prices, err := fetchJupiterPrices(mints)
	if err != nil {
		return "", fmt.Errorf("fetch jupiter prices: %w", err)
	}
	var sb strings.Builder
	for _, m := range mints {
		if p, ok := prices[m]; ok {
			// %g, not %.6f -- a real price below 1e-6 (common for
			// low-decimal memecoins) would otherwise print as the
			// misleading "$0.000000" and read as worthless/untradeable
			// when it's actually just very small. Live-caught: a real
			// tool run on this exact wallet showed E89yx72At...'s price
			// as $0.000000 despite it being valued at $0.02 by
			// get_portfolio_value_usd on the very same holding.
			fmt.Fprintf(&sb, "%s: $%g\n", m, p)
		} else {
			fmt.Fprintf(&sb, "%s: no price available (untradeable / no Jupiter route)\n", m)
		}
	}
	return sb.String(), nil
}

func getPortfolioValueImpl(ctx context.Context, stateClient state.Client, owner sgo.PublicKey) (string, error) {
	balances, err := fetchWalletBalances(ctx, stateClient, owner)
	if err != nil {
		return "", err
	}
	if len(balances) == 0 {
		return fmt.Sprintf("wallet %s holds no SOL and no non-zero SPL token balances -- total value $0.00", owner), nil
	}

	mints := make([]string, len(balances))
	for i, b := range balances {
		mints[i] = b.Mint
	}
	prices, err := fetchJupiterPrices(mints)
	if err != nil {
		return "", fmt.Errorf("fetch jupiter prices: %w", err)
	}

	type valued struct {
		label  string
		ui     float64
		value  float64
		priced bool
		// note explains why this row is excluded from the total when
		// priced is false -- either unpriced (no Jupiter route) or
		// unresolved (mint decimals never resolved, so no UI amount to
		// price in the first place). Never silently treated as $0.
		note string
	}
	var rows []valued
	total := 0.0
	for _, b := range balances {
		label := b.Mint
		if b.IsSOL {
			label = "native SOL"
		}
		if !b.UIKnown {
			rows = append(rows, valued{label: label, note: "mint decimals unresolved, cannot value"})
			continue
		}
		var ui float64
		fmt.Sscanf(b.UI, "%f", &ui)
		price, ok := prices[b.Mint]
		if !ok {
			rows = append(rows, valued{label: label, ui: ui, note: "unpriced (untradeable, excluded from total)"})
			continue
		}
		value := ui * price
		total += value
		rows = append(rows, valued{label: label, ui: ui, value: value, priced: true})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].value > rows[j].value })

	var sb strings.Builder
	fmt.Fprintf(&sb, "wallet %s -- total value: $%.2f\n", owner, total)
	for _, r := range rows {
		if r.priced {
			fmt.Fprintf(&sb, "  %-46s ui=%-16.6f value=$%.2f\n", r.label, r.ui, r.value)
		} else {
			fmt.Fprintf(&sb, "  %-46s ui=%-16.6f %s\n", r.label, r.ui, r.note)
		}
	}
	return sb.String(), nil
}
