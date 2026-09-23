package brain

import (
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/brain"
)

// Evaluate is a no-op: this package has no trading-strategy state of its
// own to react to. It exists only to satisfy brain.Brain so an eventHook
// can be handed to mothership.Create -- Init/dispatchUploads (upload.go)
// are what actually do this package's real work, driven by Request
// calls rather than by graph.Graph state changes.
func (hs *eventHook) Evaluate(solpipeState brain.SolpipeState, bidderState brain.BidderState) error {
	return nil
}
