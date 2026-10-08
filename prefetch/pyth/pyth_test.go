package pyth

import (
	"encoding/binary"
	"testing"

	sgo "github.com/solana-foundation/solana-go/v2"
)

// synthesizePushOracleBytes builds a synthetic PriceUpdateV2 account
// (either verification level variant) with a known price/conf/exponent,
// so the offset arithmetic can be checked without live account bytes --
// a direct port of catscope-rust-bot's own
// synthetic_push_oracle_bytes.
func synthesizePushOracleBytes(full bool, price int64, conf uint64, exponent int32) []byte {
	body := append([]byte{}, PushDiscriminator[:]...) // 0..8
	body = append(body, make([]byte, 32)...)          // 8..40 write_authority (unused)
	if full {
		body = append(body, verificationLevelFull) // 40
	} else {
		body = append(body, verificationLevelPartial) // 40
		body = append(body, 7)                        // 41: num_signatures (Partial-only extra byte)
	}
	feedID := make([]byte, 32)
	for i := range feedID {
		feedID[i] = 9
	}
	body = append(body, feedID...)

	le64 := func(v uint64) []byte {
		b := make([]byte, 8)
		binary.LittleEndian.PutUint64(b, v)
		return b
	}
	body = append(body, le64(uint64(price))...) // price
	body = append(body, le64(conf)...)          // conf
	expBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(expBytes, uint32(exponent))
	body = append(body, expBytes...)                    // exponent
	body = append(body, le64(uint64(1_700_000_000))...) // publish_time
	body = append(body, le64(uint64(1_699_999_999))...) // prev_publish_time
	body = append(body, le64(uint64(price))...)         // ema_price
	body = append(body, le64(conf)...)                  // ema_conf
	body = append(body, le64(123_456_789)...)           // posted_slot
	return body
}

func TestParsesPushOracleFullVerification(t *testing.T) {
	// price=15000000000, exponent=-8 -> $150.00, matching Pyth's real
	// SOL/USD exponent convention (-8).
	body := synthesizePushOracleBytes(true, 15_000_000_000, 5_000_000, -8)
	price, err := ParsePushOracle(body)
	if err != nil {
		t.Fatalf("ParsePushOracle (Full variant): %v", err)
	}
	if got, want := price.PriceUSD, 150.0; abs(got-want) > 1e-9 {
		t.Errorf("PriceUSD = %v, want %v", got, want)
	}
	if got, want := price.ConfidenceUSD, 0.05; abs(got-want) > 1e-9 {
		t.Errorf("ConfidenceUSD = %v, want %v", got, want)
	}
	wantFeed := [32]byte{}
	for i := range wantFeed {
		wantFeed[i] = 9
	}
	if price.FeedID != wantFeed {
		t.Errorf("FeedID = %x, want %x", price.FeedID, wantFeed)
	}
	if price.PublishTime != 1_700_000_000 {
		t.Errorf("PublishTime = %d, want %d", price.PublishTime, 1_700_000_000)
	}
}

func TestParsesPushOraclePartialVerification(t *testing.T) {
	// Same values, but the Partial variant's extra num_signatures byte
	// shifts price_message's real offset by one -- this is the exact
	// case that would silently misparse everything after it if the
	// offset were treated as fixed instead of branching on the
	// verification_level discriminant.
	body := synthesizePushOracleBytes(false, 15_000_000_000, 5_000_000, -8)
	price, err := ParsePushOracle(body)
	if err != nil {
		t.Fatalf("ParsePushOracle (Partial variant): %v", err)
	}
	if got, want := price.PriceUSD, 150.0; abs(got-want) > 1e-9 {
		t.Errorf("PriceUSD = %v, want %v", got, want)
	}
	if got, want := price.ConfidenceUSD, 0.05; abs(got-want) > 1e-9 {
		t.Errorf("ConfidenceUSD = %v, want %v", got, want)
	}
}

func TestPushOracleRejectsWrongDiscriminator(t *testing.T) {
	body := synthesizePushOracleBytes(true, 1, 1, 0)
	body[0] = 0 // corrupt the Anchor discriminator
	if _, err := ParsePushOracle(body); err == nil {
		t.Fatal("expected an error for a wrong discriminator")
	}
}

func TestPushOracleRejectsUnknownVerificationLevel(t *testing.T) {
	body := synthesizePushOracleBytes(true, 1, 1, 0)
	body[offVerificationLevel] = 2 // neither Partial (0) nor Full (1)
	if _, err := ParsePushOracle(body); err == nil {
		t.Fatal("expected an error for an unknown verification_level discriminant")
	}
}

func TestPushOracleRejectsTooShort(t *testing.T) {
	if _, err := ParsePushOracle(make([]byte, 10)); err == nil {
		t.Fatal("expected an error for a too-short account")
	}
	// Right discriminator, but truncated before price_message ends.
	body := synthesizePushOracleBytes(true, 1, 1, 0)
	body = body[:len(body)-1]
	if _, err := ParsePushOracle(body); err == nil {
		t.Fatal("expected an error for an account truncated mid price_message")
	}
}

// TestFeedAccountMatchesLiveVerifiedSOLUSDAddress confirms FeedAccount's
// PDA derivation for SOLUSDFeedID resolves to the exact address
// catscope-rust-bot verified live against a real mainnet account
// (7UVimffxr9ow1uXYxsr4LHAcV58mLzhmwaeKvJ1pjLiE) -- a regression test
// for the derivation itself (seed order/shard encoding), independent of
// whether a live state.Client connection is available to this test run.
func TestFeedAccountMatchesLiveVerifiedSOLUSDAddress(t *testing.T) {
	got, err := FeedAccount(SOLUSDFeedID)
	if err != nil {
		t.Fatalf("FeedAccount: %v", err)
	}
	want := sgo.MustPublicKeyFromBase58("7UVimffxr9ow1uXYxsr4LHAcV58mLzhmwaeKvJ1pjLiE")
	if got != want {
		t.Errorf("FeedAccount(SOLUSDFeedID) = %s, want %s", got, want)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
