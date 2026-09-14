package pricefeed_test

import (
	"context"
	"os"
	"testing"
	"time"

	pricefeed "git.noncepad.com/pkg/optimizer/prefetch/price-feed"
	sgo "github.com/gagliardetto/solana-go"
	"github.com/joho/godotenv"
)

// wrappedSOL used as the test mint
var wrappedSOL = sgo.MustPublicKeyFromBase58("So11111111111111111111111111111111111111112")

// usdc mint
var usdc = sgo.MustPublicKeyFromBase58("EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v")

// 55 other mints use for batching test
var otherRealMints = []string{
	"pumpCmXqMfrsAkQ5r49WcJnRayYRqmXz6ae8H7H9Dfn",
	"cbbtcf3aa214zXHbiAZQwf4122FBYbraNdFqgw4iMij",
	"A13oRB9FFaiUjfi6LdCg6p9ka1u8SfGkUFs4SKvPpump",
	"3fqify4QnaKFsvmFVqmLMUHaRKdiPki6w2H3GyDmpump",
	"23e4CNuJxvBQ7RjNLc8Bh3yN3pQq6jeiTbyzJGXYPgme",
	"Ai66LHZG9MCzg1WKdawwqduVAXpNDUuV8M3uyq5ppump",
	"ukHH6c7mMyiWCf1b9pnWe25TSpkDDt3H5pQZgZ74J82",
	"CWZ6BsdnjkDVTGkmL6bGbJXXig6ceef12KvyGQW14cMt",
	"Hv814fa7MB1B2ZBrvjyz9iHimBJ1YAx1Gir8FMQppump",
	"BCFffeVdfad5gCpuVXrnq5WAAuGLpxakM3z3zfB1pump",
	"A7bdiYdS5GjqGFtxf17ppRHtDKPkkRqbKtR27dxvQXaS",
	"98sMhvDwXj1RQi5c5Mndm3vPe9cBqPrbLaufMXFNMh5g",
	"Xs3oZwbHvqis4NYcf4YKWmEia2eC84wSiVrcYcTqpH8",
	"AENK1YJ9978xp19xQLKat6eNmndf7Jg2FxFfKiwvpump",
	"9cRCn9rGT8V2imeM2BaKs13yhMEais3ruM3rPvTGpump",
	"3zbV8nS9Wx2WxJ16uXYjTZZvoHbTnrvTQYyZE5xEEd5D",
	"Ge87EtsjwRQbHaqQmKRno69RFTwh9bfSsm99XNxTpump",
	"EtxCL9DfuQ1ZJcydWY3Mqd5B1XjhZhRKTUdAxpjSpump",
	"XsoCS1TfEyfFhfvj8EtZ528L3CaKBDBRqRapnBbDF2W",
	"6GmAFSYs4gk3FDao5FzzySQpPZaWsa4rUJHacpMpUNgx",
	"72Jp8y5bhRSPUb54JiqFsVthA2PmqaxRQU1NAfJVpump",
	"61V8vBaqAGMpgDQi4JcAwo1dmBGHsyhzodcPqnEVpump",
	"FQgtfugBdpFN7PZ6NdPrZpVLDBrPGxXesi4gVu3vErhY",
	"7ssJZGFT3twGqeYA1kvpoMWwZYvMZvrMEbjRaWgg46BL",
	"Dz2iVSLXFp7dXowD1nybWyCXuUcpV7cBZu68YPV5pump",
	"2zMMhcVQEXDtdE6vsFS7S7D5oUodfJHE8vd1gnBouauv",
	"27G8MtK7VtTcCHkpASjSDdkWWYfoqT6ggEuKidVJidD4",
	"6p6xgHyF7AeE6TZkSmFsko444wqoP15icUSqi2jfGiPN",
	"SKHYhSjuRWHgikq8eRKbtBbpABgJSkd7ytQV14i9EQ3",
	"2N5WjX4sy8W7vovpF4SVHStsYmdHre64aznmoUHApump",
	"tpg7sWJPSKijHqPqtpHY2BWBEUq1RwGxmJpD8Tppump",
	"DKxHTQCvDKUke1WpsHgbfueuRiTMqdzXrWeFHPvzpump",
	"EjD5Y9NVhXmtEqU7wYvAyZvDWZFQeEuHXFatJmTbpump",
	"7SNuFkbD7aVs5LMM6DWsSFTq4zsXNxYuD8Hdsp1Kpump",
	"94Sm8joZMSRzpQmcNVn5zZpgnRLN2DBesJYDXwuNpump",
	"SPCXxcqXj6e5dJDVNovHN8744zkbhM2bYudU45BimGb",
	"9BB6NFEcjBCtnNLFko2FqVQBq8HHM13kCyYcdQbgpump",
	"JUPyiwrYJFskUPiHa7hkeR8VUtAeFoSYbKedZNsDvCN",
	"XSTuo1fV7HHMhs4BYiwtrWSLsMCJNrooH2AssWTYZqP",
	"AZ8Yd2PttzKRxZv7CTxZpg7LmN7FdjkzmnG35Dgepump",
	"EeB76LHyVZPMRvTpLcxJqqfSz4gg9f9XgsUmFybcpump",
	"6f8ZQhxqfigdv7UszZ1rirbV7Sgv83s3rxv1N2Zopump",
	"6J4fmDstZ9vQoFU9PxmyGgwx5VEcLSeVjdxNrU9Xpump",
	"BDdzUjksj1J4bSnMveQ5tV9Up8A9c6YS1tHrxNw3pump",
	"BCdwQBAn8dYB5YjTsoB6TdHAWokxv28k2oZUodERpump",
	"PreweJYECqtQwBtpxHL171nL2K6umo692gTm7Q3rpgF",
	"FPfi9q1AixdUeWQVPFHJMJQ7a43S78dm6UZ4fzN4pump",
	"MUxEsUKSMACyw5fZf68wxf5FLnZVhtU9CwH8uNNGay1",
	"9Pfync3ejPC9eHqVzq3nYQJAhyhjqpnB9UsaSfLxpump",
	"EHuJuRhEi8vZiajJGm4w1o2aA1vftwSZajaWfcQwpump",
	"Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB",
	"9BEcn9aPEmhSPbPQeFGjidRiEKki46fVQDyPpSQXPA2D",
	"USD1ttGY1N17NEEHLmELoaybftRBUSErhqYiQzvEmuB",
}

// TestLivePoll hits the real Jupiter Price API v3 using JUPITER_API_KEY
//
// Run: go test ./prefetch/price-feed/... -run TestLivePoll -v
func TestLivePoll(t *testing.T) {
	_ = godotenv.Load("../../.env")
	apiKey := os.Getenv("JUPITER_API_KEY")
	if len(apiKey) == 0 {
		t.Skip("JUPITER_API_KEY not set; skipping live Jupiter Price API test")
	}

	p, err := pricefeed.New(pricefeed.Config{
		APIKey:   apiKey,
		Mints:    []sgo.PublicKey{wrappedSOL},
		Interval: pricefeed.FreeTierInterval,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	updateC := p.Run(ctx)

	select {
	case u, ok := <-updateC:
		if !ok {
			t.Fatal("channel closed before any update arrived")
		}
		if !u.Mint.Equals(wrappedSOL) {
			t.Fatalf("mint = %s, want %s", u.Mint, wrappedSOL)
		}
		if u.USDPrice <= 0 {
			t.Fatalf("usd price = %v, want > 0", u.USDPrice)
		}
		if u.Decimals != 9 {
			t.Fatalf("decimals = %v, want 9 (wSOL)", u.Decimals)
		}
		if u.BlockID == 0 {
			t.Fatal("block id = 0, want a real slot")
		}
		if u.FetchedAt.IsZero() {
			t.Fatal("fetched at is zero")
		}
		t.Logf("wSOL price: $%.4f (block %d, 24h change %.2f%%)", u.USDPrice, u.BlockID, u.PriceChange24h)
	case <-ctx.Done():
		t.Fatal("timed out waiting for a live price update")
	}
}

// TestLivePollBatching sends 55 real mints so New splits them into two batches
//
// Run: go test ./prefetch/price-feed/... -run TestLivePollBatching -v
func TestLivePollBatching(t *testing.T) {
	_ = godotenv.Load("../../.env")
	apiKey := os.Getenv("JUPITER_API_KEY")
	if len(apiKey) == 0 {
		t.Skip("JUPITER_API_KEY not set; skipping live Jupiter Price API test")
	}

	mints := make([]sgo.PublicKey, 0, len(otherRealMints)+2)
	mints = append(mints, wrappedSOL)
	for _, addr := range otherRealMints {
		mints = append(mints, sgo.MustPublicKeyFromBase58(addr))
	}
	mints = append(mints, usdc)
	if len(mints) <= pricefeed.MaxMintsPerRequest {
		t.Fatalf("test needs more than %d mints to exercise batching, got %d", pricefeed.MaxMintsPerRequest, len(mints))
	}

	p, err := pricefeed.New(pricefeed.Config{
		APIKey:   apiKey,
		Mints:    mints,
		Interval: pricefeed.FreeTierInterval,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Two ticks needed (one per batch) at FreeTierInterval, plus slack for the requests themselves.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	updateC := p.Run(ctx)

	pending := make(map[sgo.PublicKey]bool, len(mints))
	for _, m := range mints {
		pending[m] = true
	}
	for u := range updateC {
		t.Logf("%s: $%.6f (block %d, 24h change %.2f%%)", u.Mint, u.USDPrice, u.BlockID, u.PriceChange24h)
		delete(pending, u.Mint)
		if len(pending) == 0 {
			break
		}
	}
	if len(pending) != 0 {
		missing := make([]string, 0, len(pending))
		for m := range pending {
			missing = append(missing, m.String())
		}
		t.Errorf("never saw a price update for %d/%d mints (batching may be broken): %v", len(missing), len(mints), missing)
	}
}
