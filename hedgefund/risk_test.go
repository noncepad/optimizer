package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/harness"
	sgo "github.com/gagliardetto/solana-go"
)

// testOwner is a fixed, deterministic pubkey for tests that need a
// wallet address but never make a real RPC call -- risk agent
// construction itself never touches the network (see
// TestNewRiskAgentBuildsWithoutNetwork), so this doesn't need to be a
// real, funded wallet.
var testOwner = sgo.MustPublicKeyFromBase58(harness.DefaultWallet)

// testStateClient builds a state.Client against an address nothing is
// listening on. state.New itself never dials synchronously (it starts a
// background reconnect-retry loop and returns immediately -- see
// bot/state/state.go's own doc comment), so this is safe to use in tests
// that only build tools/agents and never actually invoke one (a real
// invocation would block/retry against this address forever, same as
// the old "http://127.0.0.1:1" RPC placeholder this replaces).
func testStateClient(t *testing.T) state.Client {
	t.Helper()
	addr, err := bidder.ParseAddress("tcp://127.0.0.1:1")
	if err != nil {
		t.Fatalf("bidder.ParseAddress: %v", err)
	}
	return state.New(context.Background(), state.DefaultDialer(addr), time.Minute)
}

// TestRiskAgentToolSet confirms harness.WalletTools -- the exact tool
// set NewRiskAgent wires in -- produces the 4 tools riskPersona's own
// text describes by name (check_obligations, get_wallet_balances,
// get_token_price_usd, get_portfolio_value_usd). Calling WalletTools
// itself never makes a network call (only invoking a built tool does),
// so this needs no fake RPC endpoint -- a real-looking one is enough.
func TestRiskAgentToolSet(t *testing.T) {
	ctx := context.Background()
	tools, err := harness.WalletTools(testStateClient(t), testOwner)
	if err != nil {
		t.Fatalf("harness.WalletTools: %v", err)
	}
	want := map[string]bool{
		"check_obligations":       false,
		"get_wallet_balances":     false,
		"get_token_price_usd":     false,
		"get_portfolio_value_usd": false,
	}
	if len(tools) != len(want) {
		t.Fatalf("expected %d tools, got %d", len(want), len(tools))
	}
	for _, tl := range tools {
		info, err := tl.Info(ctx)
		if err != nil {
			t.Fatalf("Info: %v", err)
		}
		if _, ok := want[info.Name]; !ok {
			t.Errorf("unexpected tool %q", info.Name)
			continue
		}
		want[info.Name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("expected tool %q to be present", name)
		}
	}
}

// TestNewRiskAgentBuildsWithoutNetwork confirms agent construction
// itself succeeds even against an unreachable RPC endpoint -- building
// the tool wrappers (harness.WalletTools) never invokes them, so there
// is nothing here that should ever need a real network call.
func TestNewRiskAgentBuildsWithoutNetwork(t *testing.T) {
	ctx := context.Background()
	agent, err := NewRiskAgent(ctx, &fakeToolCallingModel{response: "ok"}, testStateClient(t), testOwner)
	if err != nil {
		t.Fatalf("NewRiskAgent: %v", err)
	}
	if agent == nil {
		t.Fatal("expected a non-nil agent")
	}
}

// TestRunRiskAssessmentRoundTrip confirms a model's final answer comes
// back unchanged in RiskAssessment.Summary, and that the question
// actually reaches the model.
func TestRunRiskAssessmentRoundTrip(t *testing.T) {
	ctx := context.Background()
	cm := &fakeToolCallingModel{response: "no live borrows, wallet is fine"}
	agent, err := NewRiskAgent(ctx, cm, testStateClient(t), testOwner)
	if err != nil {
		t.Fatalf("NewRiskAgent: %v", err)
	}

	assessment, err := RunRiskAssessment(ctx, agent, "any live borrows right now?")
	if err != nil {
		t.Fatalf("RunRiskAssessment: %v", err)
	}
	if assessment.Summary != "no live borrows, wallet is fine" {
		t.Errorf("unexpected summary: %q", assessment.Summary)
	}
	if cm.calls != 1 {
		t.Errorf("expected the model to be called once, got %d", cm.calls)
	}
	// react.NewPersonaModifier (see NewRiskAgent's MessageModifier)
	// prepends a system message carrying riskPersona ahead of the
	// user's own question -- so the model sees [system, user], not just
	// the bare question. Confirming both are present (not just the
	// question) is itself useful: it's live proof the persona is really
	// being injected, not silently dropped.
	if len(cm.captured) != 2 {
		t.Fatalf("expected 2 messages (persona + question), got %d: %+v", len(cm.captured), cm.captured)
	}
	if !strings.Contains(cm.captured[0].Content, "risk-analysis agent") {
		t.Errorf("expected the first message to carry riskPersona, got %q", cm.captured[0].Content)
	}
	if cm.captured[1].Content != "any live borrows right now?" {
		t.Errorf("expected the question to reach the model unchanged as the last message, got %q", cm.captured[1].Content)
	}
}

// TestRunRiskAssessmentError confirms a model error surfaces to the
// caller rather than being swallowed into an empty-but-successful
// RiskAssessment.
func TestRunRiskAssessmentError(t *testing.T) {
	ctx := context.Background()
	cm := &fakeToolCallingModel{err: errors.New("simulated model failure")}
	agent, err := NewRiskAgent(ctx, cm, testStateClient(t), testOwner)
	if err != nil {
		t.Fatalf("NewRiskAgent: %v", err)
	}
	if _, err := RunRiskAssessment(ctx, agent, "anything"); err == nil {
		t.Fatal("expected an error from RunRiskAssessment")
	}
}
