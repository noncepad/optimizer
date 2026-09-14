package pumpswap

import (
	"testing"

	sgo "github.com/gagliardetto/solana-go"
)

func testPubkey(b byte) sgo.PublicKey {
	var buf [32]byte
	for i := range buf {
		buf[i] = b
	}
	return sgo.PublicKeyFromBytes(buf[:])
}

func syntheticPoolBody(baseMint, quoteMint, baseVault, quoteVault, coinCreator sgo.PublicKey) []byte {
	data := make([]byte, poolMinLen)
	copy(data[0:8], poolDiscriminator[:])
	copy(data[offPoolBaseMint:], baseMint[:])
	copy(data[offPoolQuoteMint:], quoteMint[:])
	copy(data[offPoolBaseVault:], baseVault[:])
	copy(data[offPoolQuoteVault:], quoteVault[:])
	copy(data[offPoolCoinCreator:], coinCreator[:])
	return data
}

func TestParsePool(t *testing.T) {
	pool := testPubkey(1)
	baseMint := testPubkey(2)
	quoteMint := testPubkey(3)
	baseVault := testPubkey(4)
	quoteVault := testPubkey(5)
	coinCreator := testPubkey(6)
	data := syntheticPoolBody(baseMint, quoteMint, baseVault, quoteVault, coinCreator)

	p, err := parsePool(pool, data)
	if err != nil {
		t.Fatal(err)
	}
	if p.Pool != pool {
		t.Fatalf("pool: got %s, want %s", p.Pool, pool)
	}
	if p.BaseMint != baseMint {
		t.Fatalf("base_mint: got %s, want %s", p.BaseMint, baseMint)
	}
	if p.QuoteMint != quoteMint {
		t.Fatalf("quote_mint: got %s, want %s", p.QuoteMint, quoteMint)
	}
	if p.BaseVault != baseVault {
		t.Fatalf("base_vault: got %s, want %s", p.BaseVault, baseVault)
	}
	if p.QuoteVault != quoteVault {
		t.Fatalf("quote_vault: got %s, want %s", p.QuoteVault, quoteVault)
	}
	if p.CoinCreator != coinCreator {
		t.Fatalf("coin_creator: got %s, want %s", p.CoinCreator, coinCreator)
	}

	if _, err = parsePool(pool, data[:10]); err == nil {
		t.Fatal("expected an error for truncated data")
	}
}

func TestGlobalConfigPDAIsFixed(t *testing.T) {
	if globalConfigPDA.IsZero() {
		t.Fatal("expected a real derived global_config PDA")
	}
}
