package main

import (
	"fmt"
	"time"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/chainstate"
	"git.noncepad.com/pkg/optimizer/prefetch/pnl"
	"git.noncepad.com/pkg/solpipe-util/common"
	sgo "github.com/gagliardetto/solana-go"
)

// pnlBetweenTimeLayout is the accepted --start/--end format: local time,
// no timezone offset needed since this only ever runs on the same
// machine that wrote the snapshots being queried.
const pnlBetweenTimeLayout = "2006-01-02 15:04:05"

// PnlBetweenCmd prints per-mint (and total) mark-to-market PnL for the
// trading wallet between two arbitrary timestamps, using whatever
// `optimizer watch-pnl` has already recorded into prefetch.db's
// pnl_position_snapshot table. Unlike watch-pnl's own dashboard view
// (unconditional earliest-vs-latest ever recorded), this answers "what
// changed between these two specific moments" -- no network calls are
// made, it only reads the local database.
type PnlBetweenCmd struct {
	ParentKey string `arg:"fee-payer" help:"the file path to the fee payer (not bidder proxy fee payer) -- used only to derive the trading wallet, no network calls are made."`
	Start     string `arg:"start" help:"window start, local time, format \"2006-01-02 15:04:05\"."`
	End       string `arg:"end" help:"window end, local time, format \"2006-01-02 15:04:05\"."`
}

func (r *PnlBetweenCmd) Run(rc *RunConfig) error {
	parentKey, err := sgo.PrivateKeyFromSolanaKeygenFile(r.ParentKey)
	if err != nil {
		return fmt.Errorf("failed to load authorizer: %s", err)
	}
	start, err := time.ParseInLocation(pnlBetweenTimeLayout, r.Start, time.Local)
	if err != nil {
		return fmt.Errorf("invalid start %q (want %q): %w", r.Start, pnlBetweenTimeLayout, err)
	}
	end, err := time.ParseInLocation(pnlBetweenTimeLayout, r.End, time.Local)
	if err != nil {
		return fmt.Errorf("invalid end %q (want %q): %w", r.End, pnlBetweenTimeLayout, err)
	}
	if !end.After(start) {
		return fmt.Errorf("end (%s) must be after start (%s)", end, start)
	}

	wallet := common.DeriveChildKeyFromIndex(parentKey, tradingChildKeyIndex).PublicKey()

	chainState, err := chainstate.Create(rc.Ctx, getDBFilePath(), state.Client{})
	if err != nil {
		return fmt.Errorf("failed to open prefetch db: %s", err)
	}
	defer func() {
		_ = chainState.Close()
	}()

	positions, err := pnl.PositionsBetween(chainState.Database().Raw(), wallet, start, end)
	if err != nil {
		return fmt.Errorf("compute PnL: %w", err)
	}
	if len(positions) == 0 {
		fmt.Printf("no positions recorded for %s at or before both %s and %s\n", wallet, start.Format(pnlBetweenTimeLayout), end.Format(pnlBetweenTimeLayout))
		return nil
	}

	fmt.Printf("PnL for %s\n%s -> %s\n\n", wallet, start.Format(pnlBetweenTimeLayout), end.Format(pnlBetweenTimeLayout))
	var totalStart, totalEnd float64
	haveStart, haveEnd := false, false
	unpriced := 0
	for _, p := range positions {
		startUSD, endUSD, deltaStr := "unknown", "unknown", "unknown"
		if p.StartUSDValue != nil {
			startUSD = fmt.Sprintf("$%.2f", *p.StartUSDValue)
			totalStart += *p.StartUSDValue
			haveStart = true
		}
		if p.LatestUSDValue != nil {
			endUSD = fmt.Sprintf("$%.2f", *p.LatestUSDValue)
			totalEnd += *p.LatestUSDValue
			haveEnd = true
		}
		if p.DeltaUSDValue != nil {
			deltaStr = fmt.Sprintf("%+.2f", *p.DeltaUSDValue)
		} else {
			unpriced++
		}
		fmt.Printf("%s: %s (%s) [%s] -> %s (%s) [%s]  delta %s\n",
			p.Mint,
			formatAmount(p.StartBalance, p.StartDecimals), startUSD, p.StartTime.Format(pnlBetweenTimeLayout),
			formatAmount(p.LatestBalance, p.LatestDecimals), endUSD, p.LatestTime.Format(pnlBetweenTimeLayout),
			deltaStr,
		)
	}
	if haveStart && haveEnd {
		fmt.Printf("\nTOTAL: $%.2f -> $%.2f  delta %+.2f\n", totalStart, totalEnd, totalEnd-totalStart)
	} else {
		fmt.Println("\nTOTAL: not enough price data to sum")
	}
	if unpriced > 0 {
		fmt.Printf("(%d mint(s) missing a price at one end of the window)\n", unpriced)
	}
	return nil
}
