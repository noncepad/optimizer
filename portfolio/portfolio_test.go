package portfolio_test

import (
	"path/filepath"
	"testing"
	"time"

	"git.noncepad.com/pkg/optimizer/portfolio"
	sgo "github.com/gagliardetto/solana-go"
)

func f64(v float64) *float64 { return &v }
func u8(v uint8) *uint8      { return &v }

func TestPositionsPnL(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "portfolio.db")
	db, err := portfolio.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %s", err)
	}
	defer func() {
		_ = db.Close()
	}()

	market := sgo.NewWallet().PublicKey()
	pipeline := sgo.NewWallet().PublicKey()
	account := sgo.NewWallet().PublicKey()
	mint := sgo.NewWallet().PublicKey()

	base := time.Now().Add(-time.Hour)
	// starting snapshot: 2 whole tokens (9 decimals) at $10 -> $20
	if err := db.Insert(portfolio.Snapshot{
		Time: base, Market: market, Pipeline: pipeline, Account: account, Mint: mint,
		Balance: 2_000_000_000, Decimals: u8(9), USDPrice: f64(10),
	}); err != nil {
		t.Fatalf("insert start: %s", err)
	}
	// a snapshot in between shouldn't affect start/latest picking
	if err := db.Insert(portfolio.Snapshot{
		Time: base.Add(20 * time.Minute), Market: market, Pipeline: pipeline, Account: account, Mint: mint,
		Balance: 2_500_000_000, Decimals: u8(9), USDPrice: f64(11),
	}); err != nil {
		t.Fatalf("insert middle: %s", err)
	}
	// latest snapshot: 3 whole tokens at $12 -> $36
	if err := db.Insert(portfolio.Snapshot{
		Time: base.Add(40 * time.Minute), Market: market, Pipeline: pipeline, Account: account, Mint: mint,
		Balance: 3_000_000_000, Decimals: u8(9), USDPrice: f64(12),
	}); err != nil {
		t.Fatalf("insert latest: %s", err)
	}

	positions, err := db.Positions(account)
	if err != nil {
		t.Fatalf("Positions: %s", err)
	}
	if len(positions) != 1 {
		t.Fatalf("len(positions) = %d, want 1", len(positions))
	}
	p := positions[0]
	if !p.Mint.Equals(mint) {
		t.Errorf("mint = %s, want %s", p.Mint, mint)
	}
	if p.StartUSDValue == nil || *p.StartUSDValue != 20 {
		t.Errorf("StartUSDValue = %v, want 20", p.StartUSDValue)
	}
	if p.LatestUSDValue == nil || *p.LatestUSDValue != 36 {
		t.Errorf("LatestUSDValue = %v, want 36", p.LatestUSDValue)
	}
	if p.DeltaUSDValue == nil || *p.DeltaUSDValue != 16 {
		t.Errorf("DeltaUSDValue = %v, want 16", p.DeltaUSDValue)
	}
}

func TestPositionsUnknownPriceStaysNil(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "portfolio.db")
	db, err := portfolio.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %s", err)
	}
	defer func() {
		_ = db.Close()
	}()

	account := sgo.NewWallet().PublicKey()
	mint := sgo.NewWallet().PublicKey()
	if err := db.Insert(portfolio.Snapshot{
		Time: time.Now(), Market: sgo.NewWallet().PublicKey(), Pipeline: sgo.NewWallet().PublicKey(),
		Account: account, Mint: mint, Balance: 1000, // no Decimals/USDPrice yet
	}); err != nil {
		t.Fatalf("insert: %s", err)
	}
	positions, err := db.Positions(account)
	if err != nil {
		t.Fatalf("Positions: %s", err)
	}
	if len(positions) != 1 {
		t.Fatalf("len(positions) = %d, want 1", len(positions))
	}
	if positions[0].DeltaUSDValue != nil {
		t.Errorf("DeltaUSDValue = %v, want nil (price unknown)", *positions[0].DeltaUSDValue)
	}
}

func TestBotsAndAccountsForBot(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "portfolio.db")
	db, err := portfolio.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %s", err)
	}
	defer func() {
		_ = db.Close()
	}()

	botA := portfolio.BotKey{Market: sgo.NewWallet().PublicKey(), Pipeline: sgo.NewWallet().PublicKey()}
	botB := portfolio.BotKey{Market: sgo.NewWallet().PublicKey(), Pipeline: sgo.NewWallet().PublicKey()}
	accountA1 := sgo.NewWallet().PublicKey()
	accountA2 := sgo.NewWallet().PublicKey()
	accountB1 := sgo.NewWallet().PublicKey()
	mint := sgo.NewWallet().PublicKey()

	for _, s := range []portfolio.Snapshot{
		{Time: time.Now(), Market: botA.Market, Pipeline: botA.Pipeline, Account: accountA1, Mint: mint, Balance: 1},
		{Time: time.Now(), Market: botA.Market, Pipeline: botA.Pipeline, Account: accountA2, Mint: mint, Balance: 2},
		{Time: time.Now(), Market: botB.Market, Pipeline: botB.Pipeline, Account: accountB1, Mint: mint, Balance: 3},
	} {
		if err := db.Insert(s); err != nil {
			t.Fatalf("insert: %s", err)
		}
	}

	bots, err := db.Bots()
	if err != nil {
		t.Fatalf("Bots: %s", err)
	}
	if len(bots) != 2 {
		t.Fatalf("len(bots) = %d, want 2", len(bots))
	}

	accountsA, err := db.AccountsForBot(botA)
	if err != nil {
		t.Fatalf("AccountsForBot(botA): %s", err)
	}
	if len(accountsA) != 2 {
		t.Fatalf("len(accountsA) = %d, want 2", len(accountsA))
	}

	accountsB, err := db.AccountsForBot(botB)
	if err != nil {
		t.Fatalf("AccountsForBot(botB): %s", err)
	}
	if len(accountsB) != 1 || !accountsB[0].Equals(accountB1) {
		t.Fatalf("accountsB = %v, want [%s]", accountsB, accountB1)
	}
}
