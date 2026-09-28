package kamino

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"

	sgo "github.com/solana-foundation/solana-go/v2"
	"github.com/solana-foundation/solana-go/v2/rpc"
)

// XstocksLendingMarket is Kamino's isolated lending market dedicated to
// xStocks collateral. Unlike the shared main market, every Obligation in
// this market by definition only ever references this market's own
// reserves -- so "every obligation with xStocks collateral" is exactly
// "every obligation whose lending_market field equals this address," a
// single getProgramAccounts call, not a per-reserve scan across each of
// an obligation's 8 deposit slots.
var XstocksLendingMarket = sgo.MustPublicKeyFromBase58("5wJeMrUYECGq41fxRESKALVcHnNX26TAWy4W98yULsua")

// DiscObligation is Kamino's real Anchor discriminator for the Obligation
// account type, from klend-sdk's own IDL-generated TS (not computed by
// hand) -- same value catscope-rust-bot's kamino.rs cites for the same
// reason.
var DiscObligation = [8]byte{168, 206, 141, 106, 88, 76, 172, 167}

// Obligation account layout constants, mirroring catscope-rust-bot's own
// (src/trader/dex/kamino.rs OFF_OB_DEPOSITS/OFF_OB_BORROWS and friends)
// byte-for-byte -- both sides must agree on these or Go's "does this
// obligation have a real position" filter and Rust's own parse would
// silently disagree about which accounts matter.
const (
	offObLendingMarket      = 32
	offObDeposits           = 96
	obligationDepositsCount = 8
	obligationCollateralLen = 136
	offOcDepositReserve     = 0
	offObBorrows            = 1208
	obligationBorrowsCount  = 5
	obligationLiquidityLen  = 200
	offOlBorrowReserve      = 0
	minObligationScanLen    = offObBorrows + obligationBorrowsCount*obligationLiquidityLen // 2208
)

// XstocksObligation is one discovered Obligation account address.
type XstocksObligation struct {
	Pubkey sgo.PublicKey `json:"pubkey"`
}

// isDefaultPubkey reports whether the 32 bytes at off are the all-zero
// pubkey -- Kamino's Obligation has no explicit deposits_len/borrows_len
// field; an unused slot's reserve simply decodes as all-zero (same
// convention catscope-rust-bot's parse_kamino_obligation relies on).
func isDefaultPubkey(body []byte, off int) bool {
	if off+32 > len(body) {
		return true
	}
	for _, b := range body[off : off+32] {
		if b != 0 {
			return false
		}
	}
	return true
}

// hasRealPosition reports whether body (an Obligation account's raw data,
// at least minObligationScanLen bytes) has at least one non-empty deposit
// or borrow slot. Most obligations opened against a market are opened and
// never funded, or fully closed out but not garbage-collected -- without
// this filter, "every obligation in the xStocks market" would embed and
// subscribe to thousands of accounts this bot has no reason to watch.
func hasRealPosition(body []byte) bool {
	if len(body) < minObligationScanLen {
		return false
	}
	for i := 0; i < obligationDepositsCount; i++ {
		base := offObDeposits + i*obligationCollateralLen
		if !isDefaultPubkey(body, base+offOcDepositReserve) {
			return true
		}
	}
	for i := 0; i < obligationBorrowsCount; i++ {
		base := offObBorrows + i*obligationLiquidityLen
		if !isDefaultPubkey(body, base+offOlBorrowReserve) {
			return true
		}
	}
	return false
}

// FetchXstocksObligations runs the single getProgramAccounts scan
// described on XstocksLendingMarket's doc comment: Obligation
// discriminator (offset 0) + lending_market == XstocksLendingMarket
// (offset 32), both filters ANDed by the RPC node itself. Slices each
// returned account down to minObligationScanLen bytes server-side
// (DataSlice) -- a real xStocks-market scan returns on the order of
// several thousand accounts, and Obligation's real on-chain size is well
// past what this bot needs to just decide "does this one have a real
// position," so slicing keeps the single RPC response from ballooning
// past what a public RPC node's response-size cap will tolerate.
//
// This is a genuinely different code path from the rest of this package
// (Create/fetch): those walk the Catscope graph via state.Client, which
// has no raw getProgramAccounts of its own (obligations were deliberately
// kept out of the graph -- see catscope-rust-bot's kamino.rs
// refresh_obligation doc comment). This talks straight to a Solana RPC
// node instead, same as cmd/alt.go and cmd/watchlstyield.go already do
// for their own one-off on-chain scans.
func FetchXstocksObligations(ctx context.Context, rpcClient *rpc.Client) ([]XstocksObligation, error) {
	offset := uint64(0)
	length := uint64(minObligationScanLen)
	result, err := rpcClient.GetProgramAccountsWithOpts(ctx, ProgramID, &rpc.GetProgramAccountsOpts{
		Encoding: sgo.EncodingBase64,
		DataSlice: &rpc.DataSlice{
			Offset: &offset,
			Length: &length,
		},
		Filters: []rpc.RPCFilter{
			{Memcmp: &rpc.RPCFilterMemcmp{Offset: 0, Bytes: sgo.Base58(DiscObligation[:])}},
			{Memcmp: &rpc.RPCFilterMemcmp{Offset: offObLendingMarket, Bytes: sgo.Base58(XstocksLendingMarket.Bytes())}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("kamino: xstocks obligation scan: %w", err)
	}
	out := make([]XstocksObligation, 0, len(result))
	for _, ka := range result {
		if ka.Account == nil || ka.Account.Data == nil {
			continue
		}
		body := ka.Account.Data.GetBinary()
		if !hasRealPosition(body) {
			continue
		}
		out = append(out, XstocksObligation{Pubkey: ka.Pubkey})
	}
	return out, nil
}

// xstocksObligationCount mirrors reserveCount -- lets CreateXstocksObligations
// resume from the db instead of re-scanning on every run.
func xstocksObligationCount(db *sql.DB) (int, error) {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM kamino_xstocks_obligation`).Scan(&n); err != nil {
		return 0, fmt.Errorf("kamino: count xstocks obligations: %w", err)
	}
	return n, nil
}

func insertXstocksObligations(db *sql.DB, obligations []XstocksObligation) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("kamino: begin: %w", err)
	}
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO kamino_xstocks_obligation (pubkey) VALUES (?)`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("kamino: prepare insert xstocks obligation: %w", err)
	}
	for _, o := range obligations {
		if _, err = stmt.Exec(o.Pubkey[:]); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return fmt.Errorf("kamino: insert xstocks obligation %s: %w", o.Pubkey, err)
		}
	}
	if err = stmt.Close(); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("kamino: close stmt: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("kamino: commit: %w", err)
	}
	return nil
}

func loadXstocksObligations(db *sql.DB) ([]XstocksObligation, error) {
	rows, err := db.Query(`SELECT pubkey FROM kamino_xstocks_obligation`)
	if err != nil {
		return nil, fmt.Errorf("kamino: query xstocks obligations: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var out []XstocksObligation
	for rows.Next() {
		var pubkey []byte
		if err = rows.Scan(&pubkey); err != nil {
			return nil, fmt.Errorf("kamino: scan xstocks obligation: %w", err)
		}
		out = append(out, XstocksObligation{Pubkey: sgo.PublicKeyFromBytes(pubkey)})
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("kamino: iterate xstocks obligations: %w", err)
	}
	return out, nil
}

// CreateXstocksObligations scans (or, on repeat runs, re-loads from db)
// every real-position Obligation account in Kamino's xStocks market. Same
// resume-from-db convention as Create (reserves): force re-runs the scan
// even if the table is already populated.
func CreateXstocksObligations(ctx context.Context, rpcClient *rpc.Client, db *sql.DB, force bool) ([]XstocksObligation, error) {
	n, err := xstocksObligationCount(db)
	if err != nil {
		return nil, fmt.Errorf("failed to check kamino xstocks obligation count: %w", err)
	}
	if n == 0 || force {
		obligations, err := FetchXstocksObligations(ctx, rpcClient)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch kamino xstocks obligations: %w", err)
		}
		if err = insertXstocksObligations(db, obligations); err != nil {
			return nil, fmt.Errorf("failed to save kamino xstocks obligations: %w", err)
		}
		return obligations, nil
	}
	return loadXstocksObligations(db)
}

// ExportXstocksObligationsJSON writes every tracked xStocks obligation to
// path as a JSON array -- catscope-rust-bot's build.rs reads this file to
// codegen the bot-embedded XSTOCKS_OBLIGATIONS_GENERATED const, mirroring
// ExportJSON (reserves) exactly.
func ExportXstocksObligationsJSON(db *sql.DB, path string) error {
	obligations, err := loadXstocksObligations(db)
	if err != nil {
		return fmt.Errorf("kamino: export xstocks obligations json: %w", err)
	}
	if obligations == nil {
		obligations = []XstocksObligation{}
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("kamino: create %s: %w", path, err)
	}
	defer func() {
		_ = f.Close()
	}()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "    ")
	if err := enc.Encode(obligations); err != nil {
		return fmt.Errorf("kamino: encode %s: %w", path, err)
	}
	return nil
}
