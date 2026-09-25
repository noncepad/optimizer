package marketwatchv1

import (
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/brain"
)

// Evaluate is brain.Brain's other required method -- arbv1's own
// Evaluate drains latency reports to a log file and (disabled) tops up
// the child wallet's SOL; this bot has neither a latency-report queue
// nor a wallet, so there is nothing to do here. All of this bot's real
// work (parsing MarketStats and recording it into Store) happens in
// loopInstance, driven by the bot's own stdout stream, not by
// mothership's solpipe-state tick.
func (hs *eventHook) Evaluate(solpipeState brain.SolpipeState, bidderState brain.BidderState) error {
	return nil
}
