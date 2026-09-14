package obligation

import (
	"testing"

	sgo "github.com/gagliardetto/solana-go"
)

// TestKnownWalletAddressesMatchRustDerivation is a golden test against
// real, live-confirmed addresses: this session (2026-09-07), the same
// six addresses were independently derived from catscope-rust-bot's own
// Rust obligation_address/obligation_pda functions (via a temporary
// native cargo test) for the real trading wallet
// Hg2p3cfmg3dratEVy94JArTVM7KzEywgNdmFnrfhroh9, then verified on-chain
// (four of the six existed with real deposit data; the other two,
// pair's, correctly don't exist yet). Any drift between this Go
// reimplementation and the Rust original would silently point
// watch-obligations at the wrong accounts, so this pins the exact
// expected output rather than just checking "no error".
func TestKnownWalletAddressesMatchRustDerivation(t *testing.T) {
	owner := sgo.MustPublicKeyFromBase58("Hg2p3cfmg3dratEVy94JArTVM7KzEywgNdmFnrfhroh9")

	solendCases := []struct {
		id   uint8
		want string
	}{
		{1, "7yAd1MHiPEFjVzt4eS1V1M2miYBuBX1og7nhmoWzoHxe"},
		{2, "Hp18XryH3fHaS2ERse7k615pN8zDrSJyosLj6UJdupmX"},
		{3, "CZwjqPcwizZBSoqKKsG9peKrpPNXTe3eKFaBGsi2cnZo"},
	}
	for _, c := range solendCases {
		got, err := SolendObligationAddress(owner, c.id)
		if err != nil {
			t.Fatalf("SolendObligationAddress(id=%d): %s", c.id, err)
		}
		if got.String() != c.want {
			t.Errorf("SolendObligationAddress(id=%d) = %s, want %s", c.id, got, c.want)
		}
	}

	kaminoCases := []struct {
		id   uint8
		want string
	}{
		{2, "FeqpYfFeAeF86n7Bgg9nd2XRXAopgLoY2UkYdBDnbgHU"},
		{3, "36r3b4z8yGnhdZuFL7cBubZ3z1bNNFxnmEnLdmBZesmR"},
		{4, "DftCHr8nFvutz6PjKeGgzke2LWYcDYbNjXkCGTq1mESE"},
	}
	for _, c := range kaminoCases {
		got, err := KaminoObligationAddress(owner, c.id)
		if err != nil {
			t.Fatalf("KaminoObligationAddress(id=%d): %s", c.id, err)
		}
		if got.String() != c.want {
			t.Errorf("KaminoObligationAddress(id=%d) = %s, want %s", c.id, got, c.want)
		}
	}
}

// TestTrackedObligationsMatchAddressFunctions verifies obligation.Address
// (the dispatcher watch-obligations actually calls) agrees with the
// protocol-specific functions above for every entry in the real,
// live-used table.
func TestTrackedObligationsMatchAddressFunctions(t *testing.T) {
	owner := sgo.MustPublicKeyFromBase58("Hg2p3cfmg3dratEVy94JArTVM7KzEywgNdmFnrfhroh9")
	for _, tr := range TrackedObligations {
		got, err := Address(owner, tr.Protocol, tr.ID)
		if err != nil {
			t.Fatalf("Address(%s/%s, id=%d): %s", tr.Protocol, tr.TradeType, tr.ID, err)
		}
		var want sgo.PublicKey
		switch tr.Protocol {
		case ProtocolSolend:
			want, err = SolendObligationAddress(owner, tr.ID)
		case ProtocolKamino:
			want, err = KaminoObligationAddress(owner, tr.ID)
		}
		if err != nil {
			t.Fatalf("direct derivation for %s/%s: %s", tr.Protocol, tr.TradeType, err)
		}
		if !got.Equals(want) {
			t.Errorf("Address(%s/%s, id=%d) = %s, want %s", tr.Protocol, tr.TradeType, tr.ID, got, want)
		}
	}
}
