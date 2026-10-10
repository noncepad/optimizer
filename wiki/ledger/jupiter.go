package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	sgo "github.com/solana-foundation/solana-go/v2"
)

// jupiterPriceBaseURL is the Jupiter Price API v3 endpoint -
// git.noncepad.com/pkg/optimizer/prefetch/price-feed already has its own
// client against the same API, but its fetch method is unexported and
// tied to a continuously-polling Poller/channel model this package has
// no use for: ProfitChild/ProfitParent (bookkeeping.go, the only
// callers) each need one point-in-time batch lookup, not a
// subscription, so this is a small, independent client rather than a new
// dependency on that package's polling machinery.
//
// A var, not a const, so bookkeeping_test.go can point it at an
// httptest.Server instead of making a real network call in tests.
var jupiterPriceBaseURL = "https://api.jup.ag/price/v3"

// jupiterMaxMintsPerRequest mirrors pricefeed.MaxMintsPerRequest - the
// most mint ids Jupiter accepts in one request's "ids" parameter.
const jupiterMaxMintsPerRequest = 50

type jupiterPrice struct {
	USDPrice float64 `json:"usdPrice"`
	Decimals uint8   `json:"decimals"`
}

// fetchJupiterPrices returns the current USD price/decimals Jupiter
// reports for each of mints it actually has a reliable price for -
// Jupiter omits a mint from its response instead of returning null when
// it has none, so a mint absent from the result (not an error) means
// "unpriced." Callers (profitFor) exclude an unpriced mint's
// contribution from a PnL total entirely rather than treating it as
// zero, the same posture optimizer/hedgefund/pnl.go already documents
// for its own USD totals.
//
// Only ever the *current* price: Jupiter's Price API has no historical
// endpoint, so a past [start, finish] window's balances are both valued
// at today's price, not the price on each of those two dates. This is a
// known, deliberate approximation (there is no price oracle anywhere in
// this codebase to do better), not a bug to fix later without first
// adding one.
func fetchJupiterPrices(ctx context.Context, mints []sgo.PublicKey) (map[sgo.PublicKey]jupiterPrice, error) {
	out := make(map[sgo.PublicKey]jupiterPrice, len(mints))
	for start := 0; start < len(mints); start += jupiterMaxMintsPerRequest {
		end := start + jupiterMaxMintsPerRequest
		if end > len(mints) {
			end = len(mints)
		}
		batch, err := fetchJupiterPriceBatch(ctx, mints[start:end])
		if err != nil {
			return nil, err
		}
		for mint, p := range batch {
			out[mint] = p
		}
	}
	return out, nil
}

func fetchJupiterPriceBatch(ctx context.Context, mints []sgo.PublicKey) (map[sgo.PublicKey]jupiterPrice, error) {
	ids := make([]string, len(mints))
	for i, m := range mints {
		ids[i] = m.String()
	}
	u, err := url.Parse(jupiterPriceBaseURL)
	if err != nil {
		return nil, fmt.Errorf("jupiter: parse base url %q: %w", jupiterPriceBaseURL, err)
	}
	q := u.Query()
	q.Set("ids", strings.Join(ids, ","))
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("jupiter: create request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jupiter: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("jupiter: unexpected status %s: %s", resp.Status, body)
	}
	var raw map[string]jupiterPrice
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("jupiter: decode response: %w", err)
	}
	prices := make(map[sgo.PublicKey]jupiterPrice, len(raw))
	for idStr, p := range raw {
		mint, err := sgo.PublicKeyFromBase58(idStr)
		if err != nil {
			return nil, fmt.Errorf("jupiter: parse mint %q: %w", idStr, err)
		}
		prices[mint] = p
	}
	return prices, nil
}
