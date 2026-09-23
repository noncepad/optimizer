// Package main (hedgefund) is the real trading wallet's risk-
// analysis agent -- the first of the four agent objects from the
// hedge-fund design (fund manager, risk, P&L, research), built as a
// compose.Workflow node per that design's sketch. It is deliberately
// READ-ONLY: it reuses harness.WalletTools verbatim (see
// optimizer/harness/tools.go) rather than inventing its own on-chain
// queries, so this agent sees exactly what a human checking the wallet
// by hand would see -- the same check_obligations/get_wallet_balances/
// get_portfolio_value_usd tools proven live against this exact wallet
// during the session this design was distilled from.
//
// The persona below encodes the real risk mechanics of catscope-rust-
// bot's multimodelv1 mode (see optimizer/cmd/multimodel.go's own doc
// comment): pair, directional, and hawkes trade types all really borrow
// on Kamino/Solend for their short legs (real liquidation exposure);
// dispersion instead shorts via a real Phoenix SOL-PERP position (real
// funding-rate/margin exposure, not a lending-protocol borrow). A risk
// assessment that doesn't know this distinction can't reason about
// what's actually at stake in what it's looking at.
package main

import (
	"context"
	"fmt"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/harness"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	sgo "github.com/gagliardetto/solana-go"
)

// riskPersona is the risk agent's role -- deliberately narrow: assess and
// report, never recommend a specific trade or say a position should be
// opened. The fund_manager node (not yet built) is the one place that
// weighs risk against opportunity; keeping this agent's job to "what's
// the real exposure right now" keeps its answers checkable against the
// tool output it actually called, the same discipline
// [[project_hawkes_manual_unwind]]'s "always verify on-chain, not just
// from logs" memory pushes for humans.
const riskPersona = `You are the risk-analysis agent for a live Solana trading wallet.
Your only job is to assess and report real, current risk exposure -- you never
recommend opening, closing, or sizing a position; that decision belongs to the
fund manager.

Real mechanics you must reason about correctly, specific to this wallet's
multimodelv1 trading bot:
  - Trade types "pair", "directional", and "hawkes" each borrow on Kamino or
    Solend for their short leg -- real liquidation risk if posted USDC
    collateral's value falls relative to the borrowed asset's value. Use
    check_obligations to see real, current deposit/borrow amounts per
    obligation; a non-empty "borrow" line is real, live leverage.
  - Trade type "dispersion" instead shorts via a real Phoenix SOL-PERP
    position, not a lending-protocol borrow -- check_obligations won't show
    this exposure at all; note that gap explicitly rather than reporting
    "no risk" when you simply have no visibility into it.
  - get_wallet_balances/get_portfolio_value_usd show the wallet's own token
    balances -- concentration risk (how much value sits in one asset) and
    whether native SOL is thin (gas exhaustion risk, not a trading risk, but
    a real operational one) both come from here.

Always call the tools rather than assume state -- on-chain state changes
between calls, and a stale assumption is worse than a slow, verified answer.
When you report, be concrete: name the actual obligation/trade type and the
actual amounts you observed, not a vague "moderate risk" without evidence.`

// RiskAssessment is this node's structured output once it's wired into
// compose.Workflow's fan-in to fund_manager -- for now (v1, standalone),
// RunRiskAssessment returns the agent's raw prose answer in Summary;
// promoting Concerns/OK to real structured fields (parsed the same
// fenced-JSON-tolerant way workflow.go's decodeModelJSON already handles
// for the SQL-generation node) is the natural next step once this needs
// to feed a typed field mapping instead of a human reading stdout.
type RiskAssessment struct {
	Summary string
}

// NewRiskAgent builds the risk-analysis agent: a ReAct loop (see
// go-wiki/examples/eino-ollama-agent's reference shape) over
// harness.WalletTools, with riskPersona injected via
// react.NewPersonaModifier so every call reasons within the real
// mechanics above instead of generic "crypto is risky" hand-waving.
func NewRiskAgent(ctx context.Context, cm model.ToolCallingChatModel, stateClient state.Client, owner sgo.PublicKey) (*react.Agent, error) {
	tools, err := harness.WalletTools(stateClient, owner)
	if err != nil {
		return nil, fmt.Errorf("hedgefund: build wallet tools for risk agent: %w", err)
	}

	agent, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: cm,
		ToolsConfig:      compose.ToolsNodeConfig{Tools: tools},
		MessageModifier:  react.NewPersonaModifier(riskPersona),
	})
	if err != nil {
		return nil, fmt.Errorf("hedgefund: create risk agent: %w", err)
	}
	return agent, nil
}

// RunRiskAssessment sends question to the risk agent and returns its
// final answer once its tool-calling loop settles -- the function this
// package's compose.Workflow risk node (once built) wraps directly in an
// InvokableLambda, same shape as workflow.go's generate_sql node wrapping
// a single cm.Generate call.
func RunRiskAssessment(ctx context.Context, agent *react.Agent, question string) (RiskAssessment, error) {
	msg, err := agent.Generate(ctx, []*schema.Message{schema.UserMessage(question)})
	if err != nil {
		return RiskAssessment{}, fmt.Errorf("hedgefund: risk agent generate: %w", err)
	}
	return RiskAssessment{Summary: msg.Content}, nil
}
