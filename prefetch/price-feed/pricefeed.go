// Package pricefeed polls the Jupiter Price API v3 for a configurable list
// of token mints and publishes price updates on a channel.
//
// https://developers.jup.ag/docs/price
//
// An API key is optional -- when set it's sent as the x-api-key header for
// the paid tier's higher rate limit; when empty, requests go out
// unauthenticated against the free public tier (1 request/sec, up to 50
// mints per request; mint lists longer than 50 are split into batches).
package pricefeed

import (
	"time"

	sgo "github.com/gagliardetto/solana-go"
)

// Jupiter Price API v3 endpoint
const DefaultBaseURL = "https://api.jup.ag/price/v3"

// The maximum number of token mints Jupiter accepts in a single request's "ids" parameter.
const MaxMintsPerRequest = 50

// Jupiter's free-tier rate. Jupiter's own docs claim 1 req/sec, but that
// reliably drew 429s in practice against the real public gateway; 3s
// between requests was confirmed empirically (5/5 requests succeeded).
const FreeTierInterval = 3 * time.Second

//Jupiter's $25/mo paid tier rate (10 req/sec)
const PaidTierInterval = 100 * time.Millisecond

// Mint's price at single poll
type PriceUpdate struct {
	Mint           sgo.PublicKey
	USDPrice       float64
	Decimals       uint8
	BlockID        uint64    // block/slot Jupiter computed this price against
	PriceChange24h float64   // 24h percentage change
	FetchedAt      time.Time // when the poller received the response (local clock)
}

// Poller config
type Config struct {
	APIKey   string          // optional; sent as the x-api-key header when non-empty
	Mints    []sgo.PublicKey // configurable list of token mints to price
	Interval time.Duration   // how often one batch is polled (default: FreeTierInterval)
}


