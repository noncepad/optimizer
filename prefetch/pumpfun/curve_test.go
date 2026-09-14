package pumpfun

import (
	"encoding/binary"
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

func TestBondingCurveAddressIsDeterministic(t *testing.T) {
	mintA := testPubkey(1)
	mintB := testPubkey(2)
	a1, err := bondingCurveAddress(mintA)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := bondingCurveAddress(mintA)
	if err != nil {
		t.Fatal(err)
	}
	if a1 != a2 {
		t.Fatalf("same mint must derive the same bonding_curve address: %s != %s", a1, a2)
	}
	b, err := bondingCurveAddress(mintB)
	if err != nil {
		t.Fatal(err)
	}
	if a1 == b {
		t.Fatalf("different mints must derive different bonding_curve addresses")
	}
}

func TestParseBondingCurve(t *testing.T) {
	data := make([]byte, bondingCurveMinLen)
	binary.LittleEndian.PutUint64(data[offBcVirtualTokenReserves:], 1_073_000_000_000_000)
	binary.LittleEndian.PutUint64(data[offBcVirtualSolReserves:], 30_000_000_000)
	binary.LittleEndian.PutUint64(data[offBcRealTokenReserves:], 793_100_000_000_000)
	binary.LittleEndian.PutUint64(data[offBcRealSolReserves:], 0)
	data[offBcComplete] = 0
	creator := testPubkey(7)
	copy(data[offBcCreator:], creator[:])
	quoteMint := testPubkey(8)
	copy(data[offBcQuoteMint:], quoteMint[:])

	mint := testPubkey(1)
	curve := testPubkey(2)
	e, err := parseBondingCurve(mint, curve, data)
	if err != nil {
		t.Fatal(err)
	}
	if e.VirtualTokenReserves != 1_073_000_000_000_000 {
		t.Fatalf("virtual_token_reserves: got %d", e.VirtualTokenReserves)
	}
	if e.VirtualSolReserves != 30_000_000_000 {
		t.Fatalf("virtual_sol_reserves: got %d", e.VirtualSolReserves)
	}
	if e.Complete {
		t.Fatal("expected complete=false")
	}
	if e.Creator != creator {
		t.Fatalf("creator: got %s, want %s", e.Creator, creator)
	}
	if e.QuoteMint != quoteMint {
		t.Fatalf("quote_mint: got %s, want %s", e.QuoteMint, quoteMint)
	}

	if _, err = parseBondingCurve(mint, curve, data[:10]); err == nil {
		t.Fatal("expected an error for truncated data")
	}
}
