package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
)

// stubAgent builds a minimal *react.Agent with zero real tools, backed
// by cm -- a stand-in for risk.go/pnl.go/research.go's own NewRiskAgent/
// NewPnlAgent/NewResearchAgent in tests that only care about
// BuildFundManagerWorkflow's own graph wiring (fan-out from
// compose.START, fan-in to fund_manager, JSON decode of the final
// decision), not each sub-agent's real tool-calling behavior -- that's
// already covered by this session's own live verification of risk.go/
// pnl.go/research.go individually.
func stubAgent(t *testing.T, cm model.ToolCallingChatModel) *react.Agent {
	t.Helper()
	agent, err := react.NewAgent(context.Background(), &react.AgentConfig{
		ToolCallingModel: cm,
		ToolsConfig:      compose.ToolsNodeConfig{},
	})
	if err != nil {
		t.Fatalf("react.NewAgent (stub): %v", err)
	}
	return agent
}

// promptOf returns the single captured prompt string a fakeToolCallingModel
// was last called with -- BuildFundManagerWorkflow's fund_manager node
// sends exactly one schema.UserMessage(prompt) per call (see manager.go),
// so index 0 is always the whole synthesis prompt.
func promptOf(t *testing.T, fm *fakeToolCallingModel) string {
	t.Helper()
	if len(fm.captured) != 1 {
		t.Fatalf("expected exactly 1 captured message, got %d", len(fm.captured))
	}
	return fm.captured[0].Content
}

// TestBuildFundManagerWorkflowFanIn confirms the core wiring property:
// each of risk/pnl/research's own distinct output lands in the right
// field of the fund_manager node's prompt -- not dropped, not swapped
// with another node's output. Each stub agent returns a unique marker
// string; the fund_manager fake model's captured prompt is checked both
// for presence of all three markers and for their relative order
// matching fundManagerSynthesisPrompt's own Risk/Pnl/Research section
// order, which a field-mapping mixup (e.g. Pnl's AddInputWithOptions
// accidentally wired to ToField("Research")) would break.
func TestBuildFundManagerWorkflowFanIn(t *testing.T) {
	ctx := context.Background()

	const riskMarker = "RISK-MARKER-3f8a"
	const pnlMarker = "PNL-MARKER-9c2d"
	const researchMarker = "RESEARCH-MARKER-71be"

	riskAgent := stubAgent(t, &fakeToolCallingModel{response: riskMarker})
	pnlAgent := stubAgent(t, &fakeToolCallingModel{response: pnlMarker})
	researchAgent := stubAgent(t, &fakeToolCallingModel{response: researchMarker})

	fundManagerModel := &fakeToolCallingModel{response: `{
		"rationale": "test rationale",
		"enable_pair_trading": false,
		"enable_directional_trading": false,
		"directional_mint": "",
		"close_directional_position": false,
		"enable_dispersion_trading": false,
		"close_dispersion_position": false,
		"enable_hawkes_trading": false,
		"close_hawkes_position": false
	}`}

	wf, err := BuildFundManagerWorkflow(ctx, fundManagerModel, riskAgent, pnlAgent, researchAgent)
	if err != nil {
		t.Fatalf("BuildFundManagerWorkflow: %v", err)
	}

	decision, err := RunFundManagerWorkflow(ctx, wf, "should we do anything?")
	if err != nil {
		t.Fatalf("RunFundManagerWorkflow: %v", err)
	}
	if decision.Rationale != "test rationale" {
		t.Errorf("expected rationale to round-trip, got %q", decision.Rationale)
	}

	prompt := promptOf(t, fundManagerModel)
	riskIdx := strings.Index(prompt, riskMarker)
	pnlIdx := strings.Index(prompt, pnlMarker)
	researchIdx := strings.Index(prompt, researchMarker)
	if riskIdx == -1 {
		t.Error("expected the risk agent's marker to appear in the fund_manager prompt")
	}
	if pnlIdx == -1 {
		t.Error("expected the pnl agent's marker to appear in the fund_manager prompt")
	}
	if researchIdx == -1 {
		t.Error("expected the research agent's marker to appear in the fund_manager prompt")
	}
	if !(riskIdx < pnlIdx && pnlIdx < researchIdx) {
		t.Errorf("expected marker order risk < pnl < research (fundManagerSynthesisPrompt's own section order), got indices risk=%d pnl=%d research=%d", riskIdx, pnlIdx, researchIdx)
	}
	if !strings.Contains(prompt, "should we do anything?") {
		t.Error("expected the original Question to also reach the fund_manager prompt")
	}
}

// TestBuildFundManagerWorkflowDecisionRoundTrip confirms every
// FundDecision field -- not just Rationale -- survives the model-JSON ->
// decodeModelJSON -> FundDecision round trip, including the directional
// mint.
func TestBuildFundManagerWorkflowDecisionRoundTrip(t *testing.T) {
	ctx := context.Background()

	riskAgent := stubAgent(t, &fakeToolCallingModel{response: "risk ok"})
	pnlAgent := stubAgent(t, &fakeToolCallingModel{response: "pnl ok"})
	researchAgent := stubAgent(t, &fakeToolCallingModel{response: "research ok"})

	const mint = "So11111111111111111111111111111111111111112"
	fundManagerModel := &fakeToolCallingModel{response: `{
		"rationale": "directional looks good",
		"enable_pair_trading": true,
		"enable_directional_trading": true,
		"directional_mint": "` + mint + `",
		"close_directional_position": false,
		"enable_dispersion_trading": false,
		"close_dispersion_position": true,
		"enable_hawkes_trading": true,
		"close_hawkes_position": false
	}`}

	wf, err := BuildFundManagerWorkflow(ctx, fundManagerModel, riskAgent, pnlAgent, researchAgent)
	if err != nil {
		t.Fatalf("BuildFundManagerWorkflow: %v", err)
	}
	decision, err := RunFundManagerWorkflow(ctx, wf, "any question")
	if err != nil {
		t.Fatalf("RunFundManagerWorkflow: %v", err)
	}

	want := FundDecision{
		Rationale:                "directional looks good",
		EnablePairTrading:        true,
		EnableDirectionalTrading: true,
		DirectionalMint:          mint,
		CloseDirectionalPosition: false,
		EnableDispersionTrading:  false,
		CloseDispersionPosition:  true,
		EnableHawkesTrading:      true,
		CloseHawkesPosition:      false,
	}
	if decision != want {
		t.Errorf("decision mismatch:\n got:  %+v\n want: %+v", decision, want)
	}
}

// TestBuildFundManagerWorkflowInvalidJSON confirms a fund_manager model
// response that isn't the expected FundDecision JSON shape fails
// cleanly (decodeModelJSON's error path) rather than returning a
// zero-value decision that silently proposes nothing while looking
// successful.
func TestBuildFundManagerWorkflowInvalidJSON(t *testing.T) {
	ctx := context.Background()

	riskAgent := stubAgent(t, &fakeToolCallingModel{response: "risk ok"})
	pnlAgent := stubAgent(t, &fakeToolCallingModel{response: "pnl ok"})
	researchAgent := stubAgent(t, &fakeToolCallingModel{response: "research ok"})
	fundManagerModel := &fakeToolCallingModel{response: "not json at all"}

	wf, err := BuildFundManagerWorkflow(ctx, fundManagerModel, riskAgent, pnlAgent, researchAgent)
	if err != nil {
		t.Fatalf("BuildFundManagerWorkflow: %v", err)
	}
	if _, err := RunFundManagerWorkflow(ctx, wf, "any question"); err == nil {
		t.Fatal("expected an error for a non-JSON fund_manager response")
	}
}

// TestBuildFundManagerWorkflowSubAgentErrorPropagates confirms an error
// from any one of the three fanned-in nodes reaches RunFundManagerWorkflow's
// caller -- the fund_manager node must not run (or silently succeed)
// against incomplete/zero-value input when an upstream node failed.
func TestBuildFundManagerWorkflowSubAgentErrorPropagates(t *testing.T) {
	ctx := context.Background()

	riskAgent := stubAgent(t, &fakeToolCallingModel{err: errors.New("simulated risk agent failure")})
	pnlAgent := stubAgent(t, &fakeToolCallingModel{response: "pnl ok"})
	researchAgent := stubAgent(t, &fakeToolCallingModel{response: "research ok"})
	// fund_manager's own model should never even be reached; a nil
	// response would panic Generate if it were, which is itself a
	// useful failure signal if this assumption is ever wrong.
	fundManagerModel := &fakeToolCallingModel{response: `{"rationale":"should not get here"}`}

	wf, err := BuildFundManagerWorkflow(ctx, fundManagerModel, riskAgent, pnlAgent, researchAgent)
	if err != nil {
		t.Fatalf("BuildFundManagerWorkflow: %v", err)
	}
	if _, err := RunFundManagerWorkflow(ctx, wf, "any question"); err == nil {
		t.Fatal("expected the risk agent's error to propagate out of RunFundManagerWorkflow")
	}
	if fundManagerModel.calls != 0 {
		t.Errorf("expected fund_manager's model never to be called when a fanned-in node failed, got %d calls", fundManagerModel.calls)
	}
}
