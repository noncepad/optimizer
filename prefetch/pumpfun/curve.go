package pumpfun

import (
	"encoding/binary"
	"fmt"

	sgo "github.com/gagliardetto/solana-go"
)

// BondingCurve account layout, verified against pump.fun's own published
// IDL (github.com/pump-fun/pump-public-docs, idl/pump.json) -- see
// edge-generator's src/pumpfun.rs for the same offsets, independently
// cross-checked there too.
const (
	bondingCurveMinLen        = 115
	offBcVirtualTokenReserves = 8  // u64
	offBcVirtualSolReserves   = 16 // u64 (aka virtual_quote_reserves)
	offBcRealTokenReserves    = 24 // u64
	offBcRealSolReserves      = 32 // u64 (aka real_quote_reserves)
	offBcComplete             = 48 // bool
	offBcCreator              = 49 // Pubkey
	offBcQuoteMint            = 83 // Pubkey
)

// sha256("account:BondingCurve")[..8] -- verified against pump.fun's own
// IDL (idl/pump.json's accounts[].discriminator) and independently
// recomputed against sha256("account:BondingCurve") during this session's
// research; see edge-generator/src/pumpfun.rs's bonding_curve_discriminator
// for the same value's Rust-side self-verifying test.
var bondingCurveDiscriminator = [8]byte{23, 183, 248, 55, 96, 216, 172, 96}

// PDAs derived from the Pump.fun program.
var (
	// globalPDA is the fixed root this package subscribes to -- see
	// event.go's fetch doc comment for why walking global's own graph
	// children (bonding_curve, then associated_bonding_curve) is enough to
	// discover every curve's mint without a program-wide token scan.
	globalPDA sgo.PublicKey
)

func init() {
	var err error
	globalPDA, _, err = sgo.FindProgramAddress([][]byte{[]byte("global")}, ProgramID)
	if err != nil {
		panic(fmt.Sprintf("failed to derive Pump.fun global PDA: %s", err))
	}
}

// bondingCurveAddress derives the deterministic bonding_curve PDA for a
// given mint -- seeds ["bonding-curve", mint], verified against the IDL.
// Not needed for discovery (bonding_curve accounts arrive directly via the
// global -> bonding_curve graph edge) -- used only as a sanity check in
// event.go, confirming a discovered (mint, bonding_curve) pair is really
// consistent with the on-chain PDA derivation.
func bondingCurveAddress(mint sgo.PublicKey) (sgo.PublicKey, error) {
	addr, _, err := sgo.FindProgramAddress([][]byte{[]byte("bonding-curve"), mint[:]}, ProgramID)
	return addr, err
}

// BondingCurveEntry holds one bonding curve's parsed state.
type BondingCurveEntry struct {
	Mint                 sgo.PublicKey `json:"mint"`
	BondingCurve         sgo.PublicKey `json:"bonding_curve"`
	Creator              sgo.PublicKey `json:"creator"`
	QuoteMint            sgo.PublicKey `json:"quote_mint"`
	VirtualTokenReserves uint64        `json:"virtual_token_reserves"`
	VirtualSolReserves   uint64        `json:"virtual_sol_reserves"`
	RealTokenReserves    uint64        `json:"real_token_reserves"`
	RealSolReserves      uint64        `json:"real_sol_reserves"`
	Complete             bool          `json:"complete"`
}

// parseBondingCurve parses a BondingCurve account's body (post-discriminator
// data starts at offset 0 of the fields above, i.e. the caller passes the
// full account including its 8-byte discriminator).
func parseBondingCurve(mint, bondingCurve sgo.PublicKey, data []byte) (*BondingCurveEntry, error) {
	if len(data) < bondingCurveMinLen {
		return nil, fmt.Errorf("bonding curve account too short: %d bytes", len(data))
	}
	return &BondingCurveEntry{
		Mint:                 mint,
		BondingCurve:         bondingCurve,
		Creator:              sgo.PublicKeyFromBytes(data[offBcCreator : offBcCreator+32]),
		QuoteMint:            sgo.PublicKeyFromBytes(data[offBcQuoteMint : offBcQuoteMint+32]),
		VirtualTokenReserves: binary.LittleEndian.Uint64(data[offBcVirtualTokenReserves:]),
		VirtualSolReserves:   binary.LittleEndian.Uint64(data[offBcVirtualSolReserves:]),
		RealTokenReserves:    binary.LittleEndian.Uint64(data[offBcRealTokenReserves:]),
		RealSolReserves:      binary.LittleEndian.Uint64(data[offBcRealSolReserves:]),
		Complete:             data[offBcComplete] != 0,
	}, nil
}
