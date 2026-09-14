package obligation

import (
	"encoding/binary"
	"fmt"
	"math/big"

	sgo "github.com/gagliardetto/solana-go"
)

// Entry is one deposit or borrow line inside a parsed obligation.
type Entry struct {
	Reserve sgo.PublicKey
	// Amount is the raw on-chain unit for this (protocol, kind) --
	// see schema.sql's own doc comment for the exact convention per
	// protocol/kind combination.
	Amount uint64
}

// Parsed is one obligation account's real deposits/borrows.
type Parsed struct {
	Deposits []Entry
	Borrows  []Entry
}

// Solend's fixed-slot (not Borsh) obligation account layout -- mirrors
// catscope-rust-bot's src/trader/dex/solend.rs exactly (OFF_OB_OWNER,
// OFF_OB_DEPOSITS_LEN, etc., live-verified there this session).
const (
	solendOffDepositsLen        = 202
	solendOffBorrowsLen         = 203
	solendOffDataFlat           = 204
	solendCollateralLen         = 88
	solendOffDepositReserve     = 0
	solendOffDepositedAmount    = 32
	solendLiquidityLen          = 112
	solendOffBorrowReserve      = 0
	solendOffBorrowedAmountWads = 48
)

var wadScale = new(big.Float).SetFloat64(1e18)

// ParseSolend mirrors catscope-rust-bot's solend::parse_obligation.
// Deposits are raw underlying-token units (no WAD conversion). Borrows
// are WAD-scaled in the source account; this converts them (/1e18)
// before returning, same convention parse_obligation itself uses.
func ParseSolend(body []byte) (*Parsed, error) {
	if len(body) < solendOffDataFlat {
		return nil, fmt.Errorf("obligation: solend body too short (%d bytes)", len(body))
	}
	depositsLen := int(body[solendOffDepositsLen])
	borrowsLen := int(body[solendOffBorrowsLen])
	depositsEnd := solendOffDataFlat + depositsLen*solendCollateralLen
	borrowsEnd := depositsEnd + borrowsLen*solendLiquidityLen
	if len(body) < borrowsEnd {
		return nil, fmt.Errorf("obligation: solend body too short for %d deposit(s)/%d borrow(s) (%d bytes)", depositsLen, borrowsLen, len(body))
	}

	p := &Parsed{Deposits: make([]Entry, 0, depositsLen), Borrows: make([]Entry, 0, borrowsLen)}
	for i := 0; i < depositsLen; i++ {
		base := solendOffDataFlat + i*solendCollateralLen
		p.Deposits = append(p.Deposits, Entry{
			Reserve: sgo.PublicKeyFromBytes(body[base+solendOffDepositReserve : base+solendOffDepositReserve+32]),
			Amount:  binary.LittleEndian.Uint64(body[base+solendOffDepositedAmount : base+solendOffDepositedAmount+8]),
		})
	}
	for i := 0; i < borrowsLen; i++ {
		base := depositsEnd + i*solendLiquidityLen
		wad := new(big.Int).SetBytes(reverse(body[base+solendOffBorrowedAmountWads : base+solendOffBorrowedAmountWads+16]))
		amount, _ := new(big.Float).Quo(new(big.Float).SetInt(wad), wadScale).Uint64()
		p.Borrows = append(p.Borrows, Entry{
			Reserve: sgo.PublicKeyFromBytes(body[base+solendOffBorrowReserve : base+solendOffBorrowReserve+32]),
			Amount:  amount,
		})
	}
	return p, nil
}

// Kamino's fixed-slot obligation account layout -- mirrors
// catscope-rust-bot's src/trader/dex/kamino.rs exactly (8 fixed deposit
// slots, only 5 borrow slots -- genuinely differs from Solend's 8/8, see
// that file's own comment). There's no explicit length byte; an unused
// slot's reserve pubkey decodes as all-zero and is filtered out here,
// same as parse_kamino_obligation does.
const (
	kaminoOffDeposits        = 96
	kaminoDepositsCount      = 8
	kaminoCollateralLen      = 136
	kaminoOffDepositReserve  = 0
	kaminoOffDepositedAmount = 32
	kaminoOffBorrows         = 1208
	kaminoBorrowsCount       = 5
	kaminoLiquidityLen       = 200
	kaminoOffBorrowReserve   = 0
	kaminoOffBorrowedAmtSF   = 88
	kaminoMinLen             = kaminoOffBorrows + kaminoBorrowsCount*kaminoLiquidityLen
)

// sfScale is 2^60, klend's fixed-point scale for borrowed_amount_sf.
var sfScale = new(big.Float).SetInt(new(big.Int).Lsh(big.NewInt(1), 60))

// ParseKamino mirrors catscope-rust-bot's kamino::parse_kamino_obligation.
// Deposits are raw cToken units (NOT yet converted to underlying -- see
// this package's schema.sql doc comment). Borrows are SF-scaled in the
// source account; this converts them (/2^60) before returning.
func ParseKamino(body []byte) (*Parsed, error) {
	if len(body) < kaminoMinLen {
		return nil, fmt.Errorf("obligation: kamino body too short (%d bytes, want >= %d)", len(body), kaminoMinLen)
	}
	p := &Parsed{}
	for i := 0; i < kaminoDepositsCount; i++ {
		base := kaminoOffDeposits + i*kaminoCollateralLen
		reserveBytes := body[base+kaminoOffDepositReserve : base+kaminoOffDepositReserve+32]
		if isZero(reserveBytes) {
			continue
		}
		p.Deposits = append(p.Deposits, Entry{
			Reserve: sgo.PublicKeyFromBytes(reserveBytes),
			Amount:  binary.LittleEndian.Uint64(body[base+kaminoOffDepositedAmount : base+kaminoOffDepositedAmount+8]),
		})
	}
	for i := 0; i < kaminoBorrowsCount; i++ {
		base := kaminoOffBorrows + i*kaminoLiquidityLen
		reserveBytes := body[base+kaminoOffBorrowReserve : base+kaminoOffBorrowReserve+32]
		if isZero(reserveBytes) {
			continue
		}
		sf := new(big.Int).SetBytes(reverse(body[base+kaminoOffBorrowedAmtSF : base+kaminoOffBorrowedAmtSF+16]))
		amount, _ := new(big.Float).Quo(new(big.Float).SetInt(sf), sfScale).Uint64()
		p.Borrows = append(p.Borrows, Entry{
			Reserve: sgo.PublicKeyFromBytes(reserveBytes),
			Amount:  amount,
		})
	}
	return p, nil
}

func isZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// reverse returns a big-endian copy of a little-endian byte slice, for
// feeding into math/big.Int.SetBytes (which expects big-endian).
func reverse(le []byte) []byte {
	be := make([]byte, len(le))
	for i, b := range le {
		be[len(le)-1-i] = b
	}
	return be
}
