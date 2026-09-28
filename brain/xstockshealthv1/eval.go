package xstockshealthv1

import (
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/brain"
	"git.noncepad.com/pkg/solpipe-util/common"
	"git.noncepad.com/pkg/solpipe-util/graph"
)

// initWallet derives a child key and sends it to the bot -- kept for
// harness-lifecycle consistency with every other bot mode (this mode's
// Rust side expects a Wallet message during handshake), but unlike
// testperpv1's initWallet there's no parent->child SOL transfer: this
// mode never signs or sends a transaction, so the child key never needs
// real funds. See xstockshealthv1.go's package doc for why.
func (hs *eventHook) initWallet() error {
	hs.childKey = common.DeriveChildKeyFromIndex(hs.parentKey, 1)
	_ = hs.graph.Subscribe(hs.ctx, hs.childKey.PublicKey(), graph.WeightAll, 2)
	hs.builder.AppendKey(hs.parentKey)
	hs.builder.AppendKey(hs.childKey)
	return hs.instance.CustomStdin(DoWallet(hs.childKey))
}

// Evaluate fires on every solpipe state update. A true no-op: this mode
// is a passive watcher with no boot transfer, no trigger to send, and no
// target-allocation loop -- everything it does is driven by the Rust
// side's own CommitHook/on_account handling of live Kamino account
// updates, not anything pushed from here.
func (hs *eventHook) Evaluate(solpipeState brain.SolpipeState, bidderState brain.BidderState) error {
	return nil
}
