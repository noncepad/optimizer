// Real, live-confirmed gap this file closes (2026-09-08): Evaluate fires
// on every solpipe state update but never looked at the trading child's
// own token balances or Solend/Kamino obligation state -- token balances
// were technically reachable via SolpipeState.TokenByOwner but nothing
// called it, and obligation account state (deposits/borrows) had no path
// into this package at all. This wires both in: obligations get a
// persistent subscription on the same graph.Graph the wallet itself
// already subscribes through (see initWallet), so reading them here is a
// pure in-memory lookup -- no new RPC round trip, no new subscription per
// call, same EdgeManager-read pattern cmd/watchobligations.go uses.
package multimodelv1

import (
	"fmt"
	"reflect"

	"git.noncepad.com/pkg/optimizer/prefetch/obligation"
	"git.noncepad.com/pkg/solpipe-util/graph"
	sgo "github.com/gagliardetto/solana-go"
)

// subscribeObligations registers a persistent, depth-0 subscription to
// every tracked obligation account (obligation.TrackedObligations) for
// the trading child key -- the same six deterministic addresses
// watch-obligations tracks. Depth 0 because each address is already the
// exact account wanted, not a root to expand from (same reasoning as
// obligationsHandler in cmd/watchobligations.go). Called once from
// initWallet, alongside the wallet's own subscribe.
func (hs *eventHook) subscribeObligations() {
	owner := hs.childKey.PublicKey()
	for _, t := range obligation.TrackedObligations {
		addr, err := obligation.Address(owner, t.Protocol, t.ID)
		if err != nil {
			hs.logger.Error(fmt.Sprintf("multimodelv1: derive %s/%s obligation address: %s", t.Protocol, t.TradeType, err))
			continue
		}
		_ = hs.graph.Subscribe(hs.ctx, addr, graph.WeightAll, 0)
	}
}

// obligationSnapshot reads every tracked obligation's current real
// deposit/borrow state straight out of the graph's own EdgeManager -- no
// new RPC call, just parsing whatever the persistent subscription (see
// subscribeObligations) has already delivered. A missing or empty entry
// means "not created yet" (same convention
// cmd/watchobligations.go's pollObligationsOnce uses), not an error; such
// entries are simply omitted from the returned map, keyed by
// "protocol/tradeType" (e.g. "solend/hawkes").
func (hs *eventHook) obligationSnapshot() map[string]*obligation.Parsed {
	owner := hs.childKey.PublicKey()
	em := hs.graph.EdgeManager()
	em.Lock()
	defer em.Unlock()
	out := make(map[string]*obligation.Parsed, len(obligation.TrackedObligations))
	for _, t := range obligation.TrackedObligations {
		addr, err := obligation.Address(owner, t.Protocol, t.ID)
		if err != nil {
			continue
		}
		account := em.UnsafeAccount(addr)
		if account == nil {
			continue
		}
		data := account.Data()
		if len(data) == 0 {
			continue
		}
		var parsed *obligation.Parsed
		switch t.Protocol {
		case obligation.ProtocolSolend:
			parsed, err = obligation.ParseSolend(data)
		case obligation.ProtocolKamino:
			parsed, err = obligation.ParseKamino(data)
		}
		if err != nil || parsed == nil {
			continue
		}
		if len(parsed.Deposits) == 0 && len(parsed.Borrows) == 0 {
			continue
		}
		out[fmt.Sprintf("%s/%s", t.Protocol, t.TradeType)] = parsed
	}
	return out
}

// evaluatePositions is Evaluate's per-call position-visibility check --
// recomputes the trading child's current token balances and obligation
// state and logs only what actually changed since the last call (same
// "silence unless something moved" discipline as
// obligation.RecordIfChanged), so this is safe to call on every single
// solpipe state update without spamming the log. Read-only: it never
// sends anything to the bot or signs a transaction, so any future
// decision logic built on top of this can be added without touching the
// visibility plumbing itself.
func (hs *eventHook) evaluatePositions(solpipeState solpipeStateReader) {
	owner := hs.childKey.PublicKey()
	tokens := solpipeState.TokenByOwner(owner)
	if !reflect.DeepEqual(tokens, hs.lastTokenSnapshot) {
		for mint, amount := range tokens {
			if hs.lastTokenSnapshot[mint] != amount {
				hs.logger.Info(fmt.Sprintf("multimodelv1: token balance changed: mint=%s amount=%d", mint, amount))
			}
		}
		hs.lastTokenSnapshot = tokens
	}

	obligations := hs.obligationSnapshot()
	if !reflect.DeepEqual(obligations, hs.lastObligationSnapshot) {
		for key, parsed := range obligations {
			if reflect.DeepEqual(hs.lastObligationSnapshot[key], parsed) {
				continue
			}
			for _, d := range parsed.Deposits {
				hs.logger.Info(fmt.Sprintf("multimodelv1: obligation %s deposit changed: reserve=%s amount=%d", key, d.Reserve, d.Amount))
			}
			for _, b := range parsed.Borrows {
				hs.logger.Info(fmt.Sprintf("multimodelv1: obligation %s borrow changed: reserve=%s amount=%d", key, b.Reserve, b.Amount))
			}
		}
		hs.lastObligationSnapshot = obligations
	}
}

// solpipeStateReader is the slice of brain.SolpipeState evaluatePositions
// actually needs -- narrowed so this file doesn't have to import brain
// just for the one method used here.
type solpipeStateReader interface {
	TokenByOwner(owner sgo.PublicKey) map[sgo.PublicKey]uint64
}
