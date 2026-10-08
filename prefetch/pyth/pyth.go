// Package pyth decodes Pyth Push Oracle price accounts and derives
// their well-known PDAs -- a Go port of catscope-rust-bot's own
// src/trader/dex/pyth.rs (parse_push_oracle and the SOL/USD feed
// account derivation); offsets/behavior are verified there against a
// real, live mainnet account, not re-derived here.
//
// Only the Push Oracle (PriceUpdateV2) format is ported -- Pyth's older
// legacy `Price` account format is deliberately left out. The Rust
// source's own module doc comment documents a real incident: a
// "well-known" legacy SOL/USD address turned out to be ~728 days stale
// (Pyth's crank has moved on to the push-oracle model for major feeds),
// so a well-known address alone was never evidence of liveness for that
// format -- there's no live legacy account worth decoding here.
package pyth

import (
	"encoding/binary"
	"fmt"
	"math"

	sgo "github.com/solana-foundation/solana-go/v2"
)

// PushOracleProgramID is Pyth's Push Oracle program
// (DEFAULT_PUSH_ORACLE_PROGRAM_ID in Pyth's own pyth_solana_receiver
// SDK, address.ts) -- the program a PriceUpdateV2 "price feed account"
// PDA is derived against. Distinct from the Receiver program
// (rec5EKMGg6MxZYaMdyBfgwp4d5rB9T1VQH5pJv5LtFJ), which owns the
// account's actual bytes but isn't part of the PDA seeds.
var PushOracleProgramID = sgo.MustPublicKeyFromBase58("pythWSnswVUd12oZpeFP8e9CVaEqJg25g1Vtc2biRsT")

// SOLUSDFeedID is Pyth's SOL/USD price feed ID, looked up live from
// Pyth's own Hermes API (hermes.pyth.network/v2/price_feeds?query=SOL/USD)
// in catscope-rust-bot -- copied from that verified constant, not
// re-looked-up here.
var SOLUSDFeedID = [32]byte{
	0xef, 0x0d, 0x8b, 0x6f, 0xda, 0x2c, 0xeb, 0xa4, 0x1d, 0xa1, 0x5d, 0x40, 0x95, 0xd1, 0xda, 0x39,
	0x2a, 0x0d, 0x2f, 0x8e, 0xd0, 0xc6, 0xc7, 0xbc, 0x0f, 0x4c, 0xfa, 0xc8, 0xc2, 0x80, 0xb5, 0x6d,
}

// FeedAccount derives the PDA holding feedID's current push-oracle
// price, for shard 0 -- Pyth's own crank keeps shard 0 continuously
// updated for popular feeds; other shards are ephemeral, SDK-caller-
// created (see getPriceFeedAccountForProgram in pyth_solana_receiver's
// address.ts). Verified live in catscope-rust-bot (this exact
// derivation for SOLUSDFeedID): resolves to
// 7UVimffxr9ow1uXYxsr4LHAcV58mLzhmwaeKvJ1pjLiE, owned by the Receiver
// program, decoding to the right discriminator/feed_id with
// publish_time only ~31 seconds behind wall-clock at the time it was
// checked.
func FeedAccount(feedID [32]byte) (sgo.PublicKey, error) {
	shardIDZeroLE := []byte{0, 0} // shard_id = 0, little-endian u16
	pda, _, err := sgo.FindProgramAddress([][]byte{shardIDZeroLE, feedID[:]}, PushOracleProgramID)
	if err != nil {
		return sgo.PublicKey{}, fmt.Errorf("pyth: derive feed account for feed %x: %w", feedID, err)
	}
	return pda, nil
}

// PushDiscriminator is PriceUpdateV2's Anchor account discriminator
// (sha256("account:PriceUpdateV2")[:8]).
var PushDiscriminator = [8]byte{34, 241, 35, 99, 157, 126, 244, 205}

const (
	offVerificationLevel = 40

	verificationLevelPartial = 0
	verificationLevelFull    = 1
)

// PriceUpdateV2 is a parsed Pyth Push Oracle price.
type PriceUpdateV2 struct {
	PriceUSD      float64
	ConfidenceUSD float64
	// FeedID is the feed this account claims to be pricing -- callers
	// that need to be sure they're reading the *right* feed
	// (ParsePushOracle doesn't know what feed_id to expect) should check
	// this against a known value, same as Pyth's own SDK does in its
	// get_price_no_older_than helpers.
	FeedID      [32]byte
	PublishTime int64 // unix seconds
}

// ParsePushOracle parses a Pyth Push Oracle PriceUpdateV2 account --
// ported from catscope-rust-bot's src/trader/dex/pyth.rs
// parse_push_oracle; see that file's own doc comment for the full
// account layout (offset table) and how it was verified against a real
// mainnet account.
//
// # Account layout — PriceUpdateV2 (Anchor)
//
//	offset          size  field
//	──────          ────  ────────────────────────────────────────────
//	  0               8   Anchor discriminator
//	  8              32   write_authority (Pubkey, unused here)
//	 40               1   verification_level discriminant (0=Partial, 1=Full)
//	 41   0 or 1 (Partial only)  num_signatures (u8) -- only present for Partial
//	price_message starts at 41 (Full) or 42 (Partial):
//	 +0              32   feed_id
//	 +32              8   price (i64)
//	 +40              8   conf (u64)
//	 +48              4   exponent (i32)
//	 +52              8   publish_time (i64)
//	 +60              8   prev_publish_time (i64, unused)
//	 +68              8   ema_price (i64, unused)
//	 +76              8   ema_conf (u64, unused)
//	price_message ends at +84, followed by posted_slot (u64, unused)
//
// VerificationLevel is a Borsh enum (Partial{num_signatures: u8}
// declared first = discriminant 0, Full declared second = discriminant
// 1) -- not a fixed-size field, so price_message's real offset depends
// on which variant is actually present.
//
// Returns an error if body doesn't start with PriceUpdateV2's Anchor
// discriminator (wrong account, or not this format at all), has an
// unrecognized verification_level discriminant, or is too short for
// whichever variant it claims to be.
func ParsePushOracle(body []byte) (PriceUpdateV2, error) {
	if len(body) < 8+32+1 {
		return PriceUpdateV2{}, fmt.Errorf("pyth: account too short (%d bytes)", len(body))
	}
	var disc [8]byte
	copy(disc[:], body[:8])
	if disc != PushDiscriminator {
		return PriceUpdateV2{}, fmt.Errorf("pyth: wrong discriminator %x, not a PriceUpdateV2 account", disc)
	}

	verificationDiscriminant := body[offVerificationLevel]
	var priceMessageOff int
	switch verificationDiscriminant {
	case verificationLevelFull:
		priceMessageOff = offVerificationLevel + 1
	case verificationLevelPartial:
		priceMessageOff = offVerificationLevel + 2
	default:
		return PriceUpdateV2{}, fmt.Errorf("pyth: unknown verification_level discriminant %d", verificationDiscriminant)
	}

	if len(body) < priceMessageOff+84+8 {
		return PriceUpdateV2{}, fmt.Errorf("pyth: account too short (%d bytes) for its own verification_level variant", len(body))
	}

	readI64 := func(off int) int64 { return int64(binary.LittleEndian.Uint64(body[off : off+8])) }
	readU64 := func(off int) uint64 { return binary.LittleEndian.Uint64(body[off : off+8]) }

	var feedID [32]byte
	copy(feedID[:], body[priceMessageOff:priceMessageOff+32])
	price := readI64(priceMessageOff + 32)
	conf := readU64(priceMessageOff + 40)
	exponent := int32(binary.LittleEndian.Uint32(body[priceMessageOff+48 : priceMessageOff+52]))
	publishTime := readI64(priceMessageOff + 52)

	scale := math.Pow(10, float64(exponent))
	return PriceUpdateV2{
		PriceUSD:      float64(price) * scale,
		ConfidenceUSD: float64(conf) * scale,
		FeedID:        feedID,
		PublishTime:   publishTime,
	}, nil
}
