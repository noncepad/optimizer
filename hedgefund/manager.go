// manager.go is the fourth and last of the four hedge-fund agent
// objects: the fund manager. Unlike risk.go/pnl.go/research.go (each a
// standalone react.NewAgent ReAct loop), this is the compose.Workflow
// this whole package's design was originally scoped around: research,
// risk, and pnl run as independent, concurrent nodes fanning into one
// fund_manager node that weighs all three and produces a decision --
// same fan-in shape go-wiki/client/hedgefund's own workflow.go uses
// (AddLambdaNode + AddInputWithOptions field mappings, wf.End()).
//
// Real, deliberate design decision, consistent with this whole package's
// posture (see README/design notes this was scoped from): the fund
// manager here NEVER gets trigger access. FundDecision is a plain data
// structure mirroring optimizer/cmd/multimodel.go's own MultiModelCmd
// flags -- a proposal for a human to read and decide whether to act on,
// not something wired to a real multimodelv1.Hook at all. There is no
// gate to bypass here because there is no trigger tool in this package,
// full stop -- a stronger guarantee than "gated," since there's no code
// path to a real transaction to gate in the first place. (Compare
// go-wiki/client/hedgefund's own fund manager, which does hold gated
// trigger tools behind a *TriggerGate -- see that package's README for
// why the two hedge-fund builds took different approaches here.)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	sgo "github.com/gagliardetto/solana-go"
)

// FundManagerInput is BuildFundManagerWorkflow's input -- Question feeds
// all three advisory nodes (research/risk/pnl) as context for what to
// look into, then feeds the fund_manager node's own final synthesis.
type FundManagerInput struct {
	Question string `json:"question"`
}

// FundDecision is BuildFundManagerWorkflow's output -- a PROPOSAL only.
// The boolean/mint fields mirror optimizer/cmd/multimodel.go's own
// MultiModelCmd flags one-for-one, so a human reading this can translate
// it directly into the real CLI invocation they'd run by hand (or not)
// -- this package never runs that command or sends a real trigger
// itself. See this file's own top doc comment for why that's a
// deliberate design choice, not a missing feature.
type FundDecision struct {
	Rationale string `json:"rationale"`

	EnablePairTrading bool `json:"enable_pair_trading"`

	EnableDirectionalTrading bool   `json:"enable_directional_trading"`
	DirectionalMint          string `json:"directional_mint,omitempty"` // base58 mint, required if EnableDirectionalTrading
	CloseDirectionalPosition bool   `json:"close_directional_position"`

	EnableDispersionTrading bool `json:"enable_dispersion_trading"`
	CloseDispersionPosition bool `json:"close_dispersion_position"`

	EnableHawkesTrading bool `json:"enable_hawkes_trading"`
	CloseHawkesPosition bool `json:"close_hawkes_position"`
}

// fundManagerSynthesisPrompt is the fund_manager node's own instruction
// -- unlike risk/pnl/research (each a persona-driven ReAct agent), this
// node is a single cm.Generate call synthesizing the other three nodes'
// already-gathered findings into one structured decision, same shape as
// go-wiki/client/hedgefund's generate_sql node synthesizing a schema +
// question into one query.
const fundManagerSynthesisPrompt = `You are the fund manager for a crypto/Solana trading fund. Three advisory
reports are below: a risk assessment, a P&L report, and a research
summary. Weigh all three and decide what, if anything, to propose.

USER QUESTION:

%s

RISK ASSESSMENT:

%s

P&L REPORT:

%s

RESEARCH FINDINGS:

%s

Rules:
1. This is a PROPOSAL only -- nothing you output here sends a real
   transaction. Say so in your rationale.
2. If risk looks elevated (large unrealized loss, stale/missing data,
   high leverage already reported), do not propose enabling a new
   position -- proposing to close an existing one is the only
   enable/close action appropriate in that case.
3. A research finding alone is never sufficient grounds to propose
   opening a position -- only propose enabling a trade type if the risk
   and P&L reports also support it.
4. If you propose enable_directional_trading, directional_mint MUST be a
   real base58 mint pubkey you found evidence for above -- never invent
   one. If you have no real target, leave enable_directional_trading
   false.
5. When in doubt, propose nothing (leave every enable_*/close_* field
   false) and explain why in rationale -- a proposal to do nothing is a
   completely valid, often correct, decision.

Return JSON in exactly this format, with every field present:

{
  "rationale": "...",
  "enable_pair_trading": false,
  "enable_directional_trading": false,
  "directional_mint": "",
  "close_directional_position": false,
  "enable_dispersion_trading": false,
  "close_dispersion_position": false,
  "enable_hawkes_trading": false,
  "close_hawkes_position": false
}

Do not include markdown.`

// fundManagerSynthesisInput is the fund_manager node's own input shape --
// fanned in from research/risk/pnl (each mapped to its own field) plus
// the original Question from compose.START, same fan-in pattern
// go-wiki/client/hedgefund's generate_sql node uses for its Schema +
// Question inputs.
type fundManagerSynthesisInput struct {
	Question string
	Risk     RiskAssessment
	Pnl      PnlReport
	Research ResearchFindings
}

// BuildDefaultFundManagerWorkflow builds real risk/pnl/research agents
// (live state.Client + Jupiter for risk, a real prefetch.db for pnl, a
// real local directory for research -- see NewRiskAgent/NewPnlAgent/
// NewResearchAgent) and wires them via BuildFundManagerWorkflow. This is
// what main.go's "manager" -node actually runs; BuildFundManagerWorkflow
// itself takes pre-built agents precisely so the graph-wiring logic
// (fan-out from compose.START, fan-in to fund_manager, JSON decode) can
// be unit-tested with stub agents instead of live infra -- see
// manager_test.go.
//
// cm must be a real model.ToolCallingChatModel (risk/pnl/research's own
// ReAct loops need tool-calling support); the fund_manager node itself
// only calls cm.Generate directly, which ToolCallingChatModel provides
// via its embedded model.BaseChatModel.
func BuildDefaultFundManagerWorkflow(
	ctx context.Context,
	cm model.ToolCallingChatModel,
	stateClient state.Client,
	owner sgo.PublicKey,
	db *store.DB,
	papersDir string,
) (*compose.Workflow[FundManagerInput, FundDecision], error) {
	riskAgent, err := NewRiskAgent(ctx, cm, stateClient, owner)
	if err != nil {
		return nil, fmt.Errorf("hedgefund: build risk agent for fund manager: %w", err)
	}
	pnlAgent, err := NewPnlAgent(ctx, cm, db, owner)
	if err != nil {
		return nil, fmt.Errorf("hedgefund: build pnl agent for fund manager: %w", err)
	}
	researchAgent, err := NewResearchAgent(ctx, cm, papersDir)
	if err != nil {
		return nil, fmt.Errorf("hedgefund: build research agent for fund manager: %w", err)
	}
	return BuildFundManagerWorkflow(ctx, cm, riskAgent, pnlAgent, researchAgent)
}

// BuildFundManagerWorkflow wires three already-built agents (risk.go's
// NewRiskAgent, pnl.go's NewPnlAgent, research.go's NewResearchAgent, or
// a test double standing in for any of them) into one
// compose.Workflow[FundManagerInput, FundDecision]: risk, pnl, and
// research each run as an independent node fed directly from
// compose.START (so they run concurrently -- none of the three depends
// on either of the others), all three fan into fund_manager, which
// synthesizes a single FundDecision via cm directly.
//
// Taking pre-built *react.Agent values (rather than the raw stateClient/db/
// papersDir BuildDefaultFundManagerWorkflow needs) is what makes this
// function's own wiring logic testable without live RPC/DB/filesystem
// access -- manager_test.go passes minimal stub agents (fixed-response
// fake models, zero real tools) to exercise the fan-out/fan-in/decode
// logic in isolation.
func BuildFundManagerWorkflow(
	ctx context.Context,
	cm model.ToolCallingChatModel,
	riskAgent *react.Agent,
	pnlAgent *react.Agent,
	researchAgent *react.Agent,
) (*compose.Workflow[FundManagerInput, FundDecision], error) {
	wf := compose.NewWorkflow[FundManagerInput, FundDecision]()

	wf.AddLambdaNode(
		"risk",
		compose.InvokableLambda(func(ctx context.Context, in FundManagerInput) (RiskAssessment, error) {
			return RunRiskAssessment(ctx, riskAgent, "For this question, assess our real current risk exposure: "+in.Question)
		}),
	).AddInput(compose.START)

	wf.AddLambdaNode(
		"pnl",
		compose.InvokableLambda(func(ctx context.Context, in FundManagerInput) (PnlReport, error) {
			return RunPnlReport(ctx, pnlAgent, "For this question, report our recent P&L: "+in.Question)
		}),
	).AddInput(compose.START)

	wf.AddLambdaNode(
		"research",
		compose.InvokableLambda(func(ctx context.Context, in FundManagerInput) (ResearchFindings, error) {
			return RunResearch(ctx, researchAgent, "For this question, surface any relevant model ideas: "+in.Question)
		}),
	).AddInput(compose.START)

	wf.AddLambdaNode(
		"fund_manager",
		compose.InvokableLambda(func(ctx context.Context, in fundManagerSynthesisInput) (FundDecision, error) {
			prompt := fmt.Sprintf(fundManagerSynthesisPrompt, in.Question, in.Risk.Summary, in.Pnl.Summary, in.Research.Summary)
			resp, err := cm.Generate(ctx, []*schema.Message{schema.UserMessage(prompt)})
			if err != nil {
				return FundDecision{}, err
			}
			var decision FundDecision
			if err := decodeModelJSON(resp.Content, &decision); err != nil {
				return FundDecision{}, fmt.Errorf("invalid model JSON: %w", err)
			}
			return decision, nil
		}),
	).
		AddInputWithOptions(compose.START, []*compose.FieldMapping{compose.MapFields("Question", "Question")}, compose.WithNoDirectDependency()).
		AddInputWithOptions("risk", []*compose.FieldMapping{compose.ToField("Risk")}, compose.WithNoDirectDependency()).
		AddInputWithOptions("pnl", []*compose.FieldMapping{compose.ToField("Pnl")}, compose.WithNoDirectDependency()).
		AddInputWithOptions("research", []*compose.FieldMapping{compose.ToField("Research")}, compose.WithNoDirectDependency())

	wf.End().AddInput("fund_manager")

	return wf, nil
}

// RunFundManagerWorkflow compiles wf (compose.Workflow itself is a
// builder, not directly invokable -- same two-step shape go-wiki/client/
// hedgefund's own sql_workflow_test.go uses: Compile(ctx) then Invoke)
// and runs it once against question.
func RunFundManagerWorkflow(ctx context.Context, wf *compose.Workflow[FundManagerInput, FundDecision], question string) (FundDecision, error) {
	runnable, err := wf.Compile(ctx)
	if err != nil {
		return FundDecision{}, fmt.Errorf("hedgefund: compile fund manager workflow: %w", err)
	}
	return runnable.Invoke(ctx, FundManagerInput{Question: question})
}

// decodeModelJSON decodes the first JSON value out of a model response,
// tolerating a leading ``` / ```json fence some models add despite being
// told to return only JSON -- same trick go-wiki/client/hedgefund's own
// workflow.go uses (json.Unmarshal rejects both a fence and trailing
// content; a Decoder only reads the one value it needs).
func decodeModelJSON(content string, v any) error {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	return json.NewDecoder(strings.NewReader(content)).Decode(v)
}
