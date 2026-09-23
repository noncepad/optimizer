// pnl.go is the second of the four hedge-fund agent objects: the P&L
// agent. Unlike risk.go (which reads live on-chain state via
// harness.WalletTools), this agent reads *recorded history* --
// prefetch.db's pnl_position_snapshot table, the same data source this
// session's own "how much did I start with last week" investigation used
// by hand, now wrapped as a proper tool via the already-real
// prefetch/pnl package (pnl.PositionsBetween) rather than hand-rolled
// SQL. See optimizer/cmd/pnlbetween.go for the CLI equivalent this
// tool's formatting is adapted from.
package main

import (
	"context"
	"fmt"
	"time"

	"git.noncepad.com/pkg/optimizer/prefetch/pnl"
	"git.noncepad.com/pkg/optimizer/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	sgo "github.com/gagliardetto/solana-go"
)

// pnlPersona is the P&L agent's role -- like risk.go's persona, narrow
// and evidence-bound: report what the recorded data actually shows,
// don't editorialize about whether that performance is "good."
//
// The real gap called out below is not hypothetical: this session found
// by hand that pnl_position_snapshot only tracks the wallet's own raw
// token balances -- it has no visibility into value sitting as Kamino/
// Solend obligation collateral, so a PnL read taken while real capital
// was deployed into lending positions will understate the wallet's true
// value at that moment. A P&L agent that doesn't know this will
// misreport an "everything got smaller" story that's actually "money
// moved into a place this table can't see."
const pnlPersona = `You are the profit-and-loss agent for a live Solana trading wallet.
Your job is to report how the wallet's recorded value has actually changed over
a requested window, using get_pnl_over_days -- never editorialize about
whether a number is "good" or "bad," just report it with its real caveats.

Real, load-bearing caveat: the data this tool reads (prefetch.db's
pnl_position_snapshot table) only tracks the wallet's own directly-held SPL
token balances and native SOL. It has NO visibility into value sitting as
Kamino/Solend lending-obligation collateral -- if real capital was deposited
into a lending position during the requested window, this tool's numbers will
understate the wallet's true value at that point, and a naive read looks like
a loss that didn't actually happen (the money moved, it wasn't lost). Always
say so explicitly when reporting a window that might have overlapped real
obligation activity, rather than presenting an unqualified "wallet lost value"
conclusion.

Also flag plainly: any mint the tool reports as unpriced at one end of the
window (no known USD price at that timestamp) is excluded from the USD
totals, not treated as zero -- the true total could be higher than what you
report.`

// PnlReport is this node's structured output -- v1 returns the agent's
// prose answer in Summary, same incremental-scope choice risk.go's
// RiskAssessment made; promoting this to real typed fields (TotalStart,
// TotalEnd, DeltaUSD, per-mint breakdown) is the natural next step once
// this feeds a compose.Workflow field mapping into fund_manager instead
// of a human reading stdout.
type PnlReport struct {
	Summary string
}

type pnlWindowInput struct {
	DaysAgo int `json:"days_ago" jsonschema:"description=How many days back the PnL window should start. The window always ends now. Use 1 for a 24-hour read, 7 for a week-over-week read."`
}

// pnlWindowTimeLayout matches optimizer/cmd/pnlbetween.go's own format --
// kept identical so this tool's output reads the same as that CLI's, for
// anyone cross-checking one against the other.
const pnlWindowTimeLayout = "2006-01-02 15:04:05"

// getPnlOverDaysImpl mirrors PnlBetweenCmd.Run's formatting almost
// exactly (per-mint start/end/delta lines, then a USD total, then an
// explicit count of unpriced mints excluded from that total) -- the only
// difference is the window is computed from "N days ago" instead of two
// caller-supplied absolute timestamps, since an agent tool is easier to
// call correctly with a relative window than with two exact datetimes.
func getPnlOverDaysImpl(ctx context.Context, db *store.DB, owner sgo.PublicKey, daysAgo int) (string, error) {
	if daysAgo <= 0 {
		return "", fmt.Errorf("days_ago must be positive, got %d", daysAgo)
	}
	end := time.Now()
	start := end.AddDate(0, 0, -daysAgo)

	positions, err := pnl.PositionsBetween(db.Raw(), owner, start, end)
	if err != nil {
		return "", fmt.Errorf("compute PnL: %w", err)
	}
	if len(positions) == 0 {
		return fmt.Sprintf("no positions recorded for %s at or before both %s and %s", owner, start.Format(pnlWindowTimeLayout), end.Format(pnlWindowTimeLayout)), nil
	}

	out := fmt.Sprintf("PnL for %s\n%s -> %s\n\n", owner, start.Format(pnlWindowTimeLayout), end.Format(pnlWindowTimeLayout))
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
		out += fmt.Sprintf("%s: %s (%s) [%s] -> %s (%s) [%s]  delta %s\n",
			p.Mint,
			formatRawAmount(p.StartBalance, p.StartDecimals), startUSD, p.StartTime.Format(pnlWindowTimeLayout),
			formatRawAmount(p.LatestBalance, p.LatestDecimals), endUSD, p.LatestTime.Format(pnlWindowTimeLayout),
			deltaStr,
		)
	}
	if haveStart && haveEnd {
		out += fmt.Sprintf("\nTOTAL: $%.2f -> $%.2f  delta %+.2f\n", totalStart, totalEnd, totalEnd-totalStart)
	} else {
		out += "\nTOTAL: not enough price data to sum\n"
	}
	if unpriced > 0 {
		out += fmt.Sprintf("(%d mint(s) missing a price at one end of the window -- excluded from the total above, not counted as zero)\n", unpriced)
	}
	return out, nil
}

func formatRawAmount(raw uint64, decimals *uint8) string {
	if decimals == nil {
		return fmt.Sprintf("%d (raw)", raw)
	}
	whole := float64(raw)
	for i := uint8(0); i < *decimals; i++ {
		whole /= 10
	}
	return fmt.Sprintf("%.6g", whole)
}

// pnlTools builds this agent's one tool -- kept separate from
// harness.WalletTools deliberately (see pnlPersona's own doc comment):
// this agent's whole value is reading *recorded* history from
// prefetch.db, not live chain state, so it gets its own narrow tool
// rather than reusing the risk agent's live-on-chain toolkit.
func pnlTools(db *store.DB, owner sgo.PublicKey) ([]tool.BaseTool, error) {
	getPnlOverDays, err := utils.InferTool(
		"get_pnl_over_days",
		"Report the wallet's recorded profit/loss over the last N days, per mint and total, using locally persisted balance/price history (no network calls). Mints with no known price at one end of the window are reported separately as unpriced, excluded from the USD total.",
		func(ctx context.Context, in pnlWindowInput) (string, error) {
			return getPnlOverDaysImpl(ctx, db, owner, in.DaysAgo)
		})
	if err != nil {
		return nil, fmt.Errorf("infer get_pnl_over_days: %w", err)
	}
	return []tool.BaseTool{getPnlOverDays}, nil
}

// NewPnlAgent builds the P&L agent: a ReAct loop over pnlTools, with
// pnlPersona injected via react.NewPersonaModifier, same shape as
// risk.go's NewRiskAgent.
func NewPnlAgent(ctx context.Context, cm model.ToolCallingChatModel, db *store.DB, owner sgo.PublicKey) (*react.Agent, error) {
	tools, err := pnlTools(db, owner)
	if err != nil {
		return nil, fmt.Errorf("hedgefund: build pnl tools: %w", err)
	}

	agent, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: cm,
		ToolsConfig:      compose.ToolsNodeConfig{Tools: tools},
		MessageModifier:  react.NewPersonaModifier(pnlPersona),
	})
	if err != nil {
		return nil, fmt.Errorf("hedgefund: create pnl agent: %w", err)
	}
	return agent, nil
}

// RunPnlReport sends question to the P&L agent and returns its final
// answer once its tool-calling loop settles -- same shape as risk.go's
// RunRiskAssessment.
func RunPnlReport(ctx context.Context, agent *react.Agent, question string) (PnlReport, error) {
	msg, err := agent.Generate(ctx, []*schema.Message{schema.UserMessage(question)})
	if err != nil {
		return PnlReport{}, fmt.Errorf("hedgefund: pnl agent generate: %w", err)
	}
	return PnlReport{Summary: msg.Content}, nil
}
