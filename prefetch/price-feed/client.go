package pricefeed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	sgo "github.com/gagliardetto/solana-go"
)

// One price entry keyed by mint 
// https://developers.jup.ag/docs/price
type tokenPrice struct {
	USDPrice       float64 `json:"usdPrice"`
	BlockID        uint64  `json:"blockId"`
	Decimals       uint8   `json:"decimals"`
	PriceChange24h float64 `json:"priceChange24h"`
}

// Requests prices for exactly this batch (must be <= MaxMintsPerRequest)
// (Jupiter omits mints it has no reliable price for rather than returning null, so a
// missing key means "no price available", not an error)
func (p *Poller) fetch(ctx context.Context, mints []sgo.PublicKey) (map[sgo.PublicKey]tokenPrice, error) {
	ids := make([]string, len(mints))
	for i, m := range mints {
		ids[i] = m.String()
	}
	u, err := url.Parse(DefaultBaseURL)
	if err != nil {
		return nil, fmt.Errorf("jupiter: parse base url %q: %w", DefaultBaseURL, err)
	}
	q := u.Query()
	q.Set("ids", strings.Join(ids, ","))
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("jupiter: create request: %w", err)
	}
	if len(p.cfg.APIKey) > 0 {
		req.Header.Set("x-api-key", p.cfg.APIKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jupiter: request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("jupiter: unexpected status %s: %s", resp.Status, body)
	}
	var raw map[string]tokenPrice
	if err = json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("jupiter: decode response: %w", err)
	}
	prices := make(map[sgo.PublicKey]tokenPrice, len(raw))
	for idStr, tp := range raw {
		mint, err := sgo.PublicKeyFromBase58(idStr)
		if err != nil {
			return nil, fmt.Errorf("jupiter: parse mint %q: %w", idStr, err)
		}
		prices[mint] = tp
	}
	return prices, nil
}
