package obligation

import (
	"fmt"

	"git.noncepad.com/pkg/optimizer/prefetch/kamino"
	"git.noncepad.com/pkg/optimizer/prefetch/solend"
	sgo "github.com/gagliardetto/solana-go"
)

// KaminoMainMarket is the single lending market every tracked trade
// type's Kamino obligation lives in -- see catscope-rust-bot's
// kamino::KAMINO_MAIN_MARKET (src/trader/dex/kamino.rs).
var KaminoMainMarket = sgo.MustPublicKeyFromBase58("7u3HeHxYDLhnCoErrtycNokbQYbWGzLs6JSDqGAv5PfF")

// TradeType is which strategy an obligation belongs to -- pair/
// directional/hawkes were multimodelv1's trade types (since removed);
// kept here because a real obligation any of them opened on-chain still
// needs tracking by its exact id regardless of whether the Rust side
// that opened it still exists.
type TradeType string

const (
	TradePair        TradeType = "pair"
	TradeDirectional TradeType = "directional"
	TradeHawkes      TradeType = "hawkes"
)

// Protocol is which lending protocol an obligation is on.
type Protocol string

const (
	ProtocolSolend Protocol = "solend"
	ProtocolKamino Protocol = "kamino"
)

// TrackedObligation is one (protocol, trade_type) obligation this bot's
// multimodelv1 mode (since removed) may have bootstrapped -- its id
// mirrored the exact PAIR_*_OBLIGATION_ID/DIRECTIONAL_*_OBLIGATION_ID/
// HAWKES_*_OBLIGATION_ID constants multimodelv1's Rust side used to
// define. Kept as a fixed table rather than deleted -- any obligation
// multimodelv1 actually opened on-chain is real and still needs tracking
// by this exact id regardless of whether the Rust side that opened it
// still exists.
var TrackedObligations = []struct {
	TradeType TradeType
	Protocol  Protocol
	ID        uint8
}{
	{TradePair, ProtocolSolend, 1},
	{TradePair, ProtocolKamino, 2},
	{TradeDirectional, ProtocolSolend, 2},
	{TradeDirectional, ProtocolKamino, 3},
	{TradeHawkes, ProtocolSolend, 3},
	{TradeHawkes, ProtocolKamino, 4},
}

// solendObligationSeed mirrors catscope-rust-bot's
// solend::obligation_seed: id=0 keeps the original, unparameterized seed
// (a real, currently-open mainnet obligation depends on this staying
// byte-identical); every other id appends "-{id}".
func solendObligationSeed(id uint8) string {
	if id == 0 {
		return "solend-obligation"
	}
	return fmt.Sprintf("solend-obligation-%d", id)
}

// SolendObligationAddress mirrors solend::obligation_address: a
// create-with-seed address (not a PDA), deterministic from owner and id.
func SolendObligationAddress(owner sgo.PublicKey, id uint8) (sgo.PublicKey, error) {
	return sgo.CreateWithSeed(owner, solendObligationSeed(id), solend.ProgramID)
}

// KaminoObligationAddress mirrors catscope-rust-bot's kamino::
// obligation_pda: seeds `[[0], [id], owner, lending_market, default,
// default]` (tag=0, standard single-market-slot obligation; seed1/seed2
// always the default/all-zero pubkey for every obligation this bot
// creates -- see obligation_pda's own doc comment in kamino.rs).
func KaminoObligationAddress(owner sgo.PublicKey, id uint8) (sgo.PublicKey, error) {
	var defaultPk sgo.PublicKey // all-zero
	addr, _, err := sgo.FindProgramAddress(
		[][]byte{
			{0},
			{id},
			owner.Bytes(),
			KaminoMainMarket.Bytes(),
			defaultPk.Bytes(),
			defaultPk.Bytes(),
		},
		kamino.ProgramID,
	)
	return addr, err
}

// Address resolves a TrackedObligation entry's real on-chain address for owner.
func Address(owner sgo.PublicKey, protocol Protocol, id uint8) (sgo.PublicKey, error) {
	switch protocol {
	case ProtocolSolend:
		return SolendObligationAddress(owner, id)
	case ProtocolKamino:
		return KaminoObligationAddress(owner, id)
	default:
		return sgo.PublicKey{}, fmt.Errorf("obligation: unknown protocol %q", protocol)
	}
}
