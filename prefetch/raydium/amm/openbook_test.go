package amm_test

import (
	"crypto/ed25519"
	"encoding/binary"
	"testing"

	"git.noncepad.com/pkg/optimizer/prefetch/raydium/amm"
	sgo "github.com/gagliardetto/solana-go"
)

// buildMarketBlob lays out a synthetic OpenBook/Serum MarketState account
// using the documented offsets (5-byte header padding, packed fields, 7-byte
// footer padding), so ParseOpenBookMarket can be exercised without a live
// chain fixture.
func buildMarketBlob(t *testing.T, nonce uint64, coinVault, pcVault, eventQ, bids, asks sgo.PublicKey) []byte {
	t.Helper()
	const size = 5 + 381 + 7
	buf := make([]byte, size)
	binary.LittleEndian.PutUint64(buf[45:53], nonce)
	copy(buf[117:149], coinVault[:])
	copy(buf[165:197], pcVault[:])
	copy(buf[253:285], eventQ[:])
	copy(buf[285:317], bids[:])
	copy(buf[317:349], asks[:])
	return buf
}

func randomPubkey(t *testing.T) sgo.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	var pk sgo.PublicKey
	copy(pk[:], pub)
	return pk
}

func TestParseOpenBookMarket(t *testing.T) {
	marketID := randomPubkey(t)
	coinVault := randomPubkey(t)
	pcVault := randomPubkey(t)
	eventQ := randomPubkey(t)
	bids := randomPubkey(t)
	asks := randomPubkey(t)
	const nonce = uint64(4)

	blob := buildMarketBlob(t, nonce, coinVault, pcVault, eventQ, bids, asks)

	m, err := amm.ParseOpenBookMarket(marketID, blob)
	if err != nil {
		t.Fatalf("parse failed: %s", err)
	}
	if m.VaultSignerNonce != nonce {
		t.Fatalf("nonce: got %d want %d", m.VaultSignerNonce, nonce)
	}
	if !m.CoinVault.Equals(coinVault) {
		t.Fatalf("coin vault mismatch: got %s want %s", m.CoinVault, coinVault)
	}
	if !m.PcVault.Equals(pcVault) {
		t.Fatalf("pc vault mismatch: got %s want %s", m.PcVault, pcVault)
	}
	if !m.EventQueue.Equals(eventQ) {
		t.Fatalf("event queue mismatch: got %s want %s", m.EventQueue, eventQ)
	}
	if !m.Bids.Equals(bids) {
		t.Fatalf("bids mismatch: got %s want %s", m.Bids, bids)
	}
	if !m.Asks.Equals(asks) {
		t.Fatalf("asks mismatch: got %s want %s", m.Asks, asks)
	}
}

func TestParseOpenBookMarketTooShort(t *testing.T) {
	if _, err := amm.ParseOpenBookMarket(randomPubkey(t), make([]byte, 10)); err == nil {
		t.Fatal("expected error for undersized market account data")
	}
}

// TestDeriveVaultSigner checks that DeriveVaultSigner is deterministic and
// off-curve (as CreateProgramAddress requires), by searching for a nonce that
// yields a valid PDA and confirming repeated derivation is stable.
func TestDeriveVaultSigner(t *testing.T) {
	market := randomPubkey(t)
	programID := randomPubkey(t)

	var (
		found bool
		nonce uint64
		want  sgo.PublicKey
	)
	for n := range uint64(256) {
		pk, err := amm.DeriveVaultSigner(market, n, programID)
		if err == nil {
			nonce, want, found = n, pk, true
			break
		}
	}
	if !found {
		t.Fatal("could not find any off-curve nonce in 256 attempts")
	}

	got, err := amm.DeriveVaultSigner(market, nonce, programID)
	if err != nil {
		t.Fatalf("second derivation failed: %s", err)
	}
	if !got.Equals(want) {
		t.Fatalf("derivation not deterministic: got %s want %s", got, want)
	}
}
