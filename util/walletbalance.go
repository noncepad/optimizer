package util

import (
	"context"
	"errors"
	"fmt"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/prefetch/mintinfo"
	"git.noncepad.com/pkg/solpipe-util/graph"
	bin "github.com/gagliardetto/binary"
	sgo "github.com/gagliardetto/solana-go"
	sgotkn "github.com/gagliardetto/solana-go/programs/token"
)

// walletBalanceQuietSlots is how many consecutive quiet slot-commits
// FetchWalletBalance waits for before considering a wallet's depth-2
// neighborhood (its own SOL account plus any owned SPL token accounts)
// fully delivered. More conservative than the fixed 3-slot window
// state.Client.QuerySingleShot used internally -- see FetchWalletBalance's
// doc comment for why this replaces it.
const walletBalanceQuietSlots = 10

// TokenBalance is one SPL token account found under a wallet.
type TokenBalance struct {
	Mint   sgo.PublicKey
	Amount uint64
	// Decimals is the mint's decimals, resolved via a separate FetchAccount
	// call per unique mint (a token account's own data has no decimals
	// field -- that lives on the mint account, which isn't a graph child
	// of the wallet's depth-2 walk below). DecimalsKnown is false when
	// that resolution failed (mint account not found, or an unexpected
	// data size) -- Decimals is meaningless then, not just "0 decimals".
	Decimals      uint8
	DecimalsKnown bool
}

// WalletBalance is a wallet pubkey's live SOL balance plus every SPL
// token account discovered under it.
type WalletBalance struct {
	Pubkey   sgo.PublicKey
	Slot     graph.Slot
	Lamports uint64
	// Found is false when the root account itself was never observed (no
	// funds, or never initialized) -- Slot/Lamports are meaningless then.
	Found  bool
	Tokens []TokenBalance
}

// FetchWalletBalance fetches a wallet's live SOL and SPL token balances
// via a direct graph.Hook subscription (state.Client.Hook +
// PendingSubscriptionStatus) -- the same pattern
// prefetch/{solend,kamino,marginfi,jet,drift}/event.go already use for
// bulk program crawling, applied here to a single wallet's depth-2
// neighborhood. Deliberately not state.Client.QuerySingleShot: that
// helper wrapped the identical underlying Subscribe/ack mechanism but
// with a fixed, apparently too-aggressive 3-slot quiet window, and was
// dropped from this codebase after repeated unreliable results in
// production use (cmd/balance.go, shell/tools.go and
// prefetch/prompttool.go's wallet-balance tools all used to call it
// directly).
func FetchWalletBalance(ctx context.Context, stateClient state.Client, pubkey sgo.PublicKey) (*WalletBalance, error) {
	hookCtx, cancel := context.WithCancelCause(ctx)
	handler := &walletBalanceHandler{ctx: hookCtx, root: pubkey}
	err := stateClient.Hook(handler)
	cancel(errors.New("wallet balance fetch complete"))
	if err != nil {
		return nil, fmt.Errorf("hook failed: %w", err)
	}
	em := handler.g.EdgeManager()
	result := &WalletBalance{Pubkey: pubkey}
	em.Lock()
	account := em.UnsafeAccount(pubkey)
	if account == nil {
		em.Unlock()
		return result, nil
	}
	header := account.Header()
	result.Found = true
	result.Slot = header.Slot
	result.Lamports = header.Lamports
	for tokenPubkey := range em.EdgeDown(pubkey) {
		tokenAccount := em.UnsafeAccount(tokenPubkey)
		if tokenAccount == nil {
			continue
		}
		h := tokenAccount.Header()
		if !h.Owner.Equals(sgo.TokenProgramID) {
			continue
		}
		data := tokenAccount.Data()
		if len(data) != 165 {
			continue
		}
		d := new(sgotkn.Account)
		if err := bin.UnmarshalBorsh(d, data); err == nil {
			result.Tokens = append(result.Tokens, TokenBalance{Mint: d.Mint, Amount: d.Amount})
		}
	}
	em.Unlock()

	resolveTokenDecimals(ctx, stateClient, result.Tokens)
	return result, nil
}

// resolveTokenDecimals fills in Decimals/DecimalsKnown on each of tokens
// in place, one FetchAccount call per unique mint (deduplicated -- a
// wallet commonly holds several accounts of the same mint, and multiple
// TokenBalance entries never happen for the same mint in one
// FetchWalletBalance result anyway, but dedup here keeps this correct if
// that ever changes). A mint that fails to resolve (not found, or an
// unexpected data size) is left with DecimalsKnown=false rather than
// aborting the whole balance fetch -- one bad mint shouldn't hide every
// other real balance.
func resolveTokenDecimals(ctx context.Context, stateClient state.Client, tokens []TokenBalance) {
	decimals := make(map[sgo.PublicKey]uint8)
	for i := range tokens {
		mint := tokens[i].Mint
		if _, done := decimals[mint]; done {
			continue
		}
		account, found, err := FetchAccount(ctx, stateClient, mint)
		if err != nil || !found {
			continue
		}
		data := account.Data()
		if len(data) != mintinfo.MintAccountSize {
			continue
		}
		d, err := mintinfo.DecodeDecimals(data)
		if err != nil {
			continue
		}
		decimals[mint] = d
	}
	for i := range tokens {
		if d, ok := decimals[tokens[i].Mint]; ok {
			tokens[i].Decimals = d
			tokens[i].DecimalsKnown = true
		}
	}
}

// walletBalanceHandler is a minimal graph.Hook: subscribe once to root at
// depth 2 (root + direct children -- its owned token accounts) in Init,
// then just wait for PendingSubscriptionStatus to report quiet.
// FetchWalletBalance reads the resulting state straight out of the
// EdgeManager afterward, so OnAccount itself doesn't need to do anything.
type walletBalanceHandler struct {
	ctx  context.Context
	root sgo.PublicKey
	g    graph.Graph
	pss  *PendingSubscriptionStatus
	slot graph.Slot
}

func (h *walletBalanceHandler) Ctx() context.Context { return h.ctx }

func (h *walletBalanceHandler) Init(g graph.Graph) error {
	h.g = g
	h.pss = CreatePendingSubscriptionList(h.ctx, g, 10, walletBalanceQuietSlots)
	h.pss.Subscribe(h.root, graph.WeightAll, 2)
	return nil
}

func (h *walletBalanceHandler) OnSlot(slot graph.Slot, status graph.SlotStatus) {}

func (h *walletBalanceHandler) CommitStart(slot graph.Slot) {
	h.slot = slot
}

func (h *walletBalanceHandler) OnAccount(a graph.Account, isNew bool) {}

func (h *walletBalanceHandler) CommitFinish() bool {
	return h.pss.Check(h.slot)
}
