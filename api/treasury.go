package api

import (
	"context"
	"time"

	"git.noncepad.com/pkg/solpipe-util/graph"
	sgo "github.com/gagliardetto/solana-go"
)

type Treasury interface {
	// Parent public key that distributes funds
	Parent() Wallet
	Children() []sgo.PublicKey
	// Child derives a child key; this is for observing the current balances
	Child(childID sgo.PublicKey) Wallet
	// SOLBalance returns the current SOL balance in lamports
	SOLBalance() uint64
	// TokenBalance returns the current balance of tokens
	TokenBalance() map[sgo.PublicKey]uint64
	// manage funds via budget
	Budget(childID sgo.PublicKey) Budget
}

type Budget interface {
	ID() sgo.PublicKey
	Pubkey() sgo.PublicKey
	// if the target falls below SolDelta, top up the account
	SetSOL(budget graph.Lamports)
	// send money to a wallet
	Fund(mint sgo.PublicKey, amount uint64)
	// sweep token funds, leave minAmount in account
	Sweep(mint sgo.PublicKey, minAmount uint64)
	// Close all token accounts, close all protocol positions; sweep funds back to parent; Use Jupiter to power sweeps.
	Close() error
	// SetPositionCloser registers the callback Close() invokes first, to
	// unwind whatever protocol-specific positions (lending deposits,
	// perps, LP, etc.) this child wallet holds, before its resulting
	// SOL/SPL token balances get swept back to the parent. The treasury
	// package has no protocol-specific knowledge of its own -- these
	// positions live entirely in each running bot instance's own
	// on-chain state -- so whoever owns that context (typically the
	// brain module driving this child's bot instance) must supply it.
	// Close only sweeps SOL/token balances, skipping this step entirely,
	// if nothing has been registered.
	SetPositionCloser(closer func(ctx context.Context) error)
}

const SolDelta graph.Lamports = sgo.LAMPORTS_PER_SOL >> 3

type Wallet interface {
	ID() sgo.PublicKey
	PublicKey() sgo.PublicKey
	// SOLBalance returns the current SOL balance in lamports
	SOLBalance() uint64
	// TokenBalance returns the current balance of tokens
	TokenBalance() TokenBalance
}

type BookKeeping interface {
	// take a snapshot of all token positions and market into an accounting book
	Snapshot(Treasury) error
	// Profit and Loss for a wallet
	ProfitChild(start time.Time, finish time.Time, valuationMint sgo.PublicKey, childID sgo.PublicKey) (child float64, err error)
	// Profit and Loss for the parent and aggregate the net income from child wallets
	ProfitParent(start time.Time, finish time.Time, valuationMint sgo.PublicKey) (parentOnly float64, overall float64, err error)
}

type (
	TokenBalance = map[sgo.PublicKey]uint64
	Valuation    struct {
		Valuation float64
		Token     TokenBalance
	}
)
