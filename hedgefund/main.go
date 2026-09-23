// Command hedgefund is the multi-agent hedge-fund system built on top of
// optimizer/harness. All four agent objects are wired up: risk (risk.go),
// P&L (pnl.go), and research (research.go) each run standalone as a
// react.NewAgent ReAct loop; fund manager (manager.go) is the
// compose.Workflow that fans all three into one synthesized FundDecision
// -- a proposal only, never a real trigger (see manager.go's own top doc
// comment for why this package deliberately never gets real trigger
// access at all, unlike gitlab.noncepad.com/eflam/wiki/client/hedgefund's
// gated-but-present trigger tools).
//
// Usage:
//
//	go run . -node risk     -prompt "What's our real risk exposure right now?"
//	go run . -node pnl      -prompt "How has our value changed over the last week?"
//	go run . -node research -prompt "Any new model ideas in the papers directory?"
//	go run . -node manager  -prompt "Should we do anything right now?"
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.noncepad.com/pkg/optimizer/harness"
	"git.noncepad.com/pkg/optimizer/store"
	"github.com/cloudwego/eino-ext/components/model/ollama"
	sgo "github.com/gagliardetto/solana-go"
)

func main() {
	node := flag.String("node", "risk", "which agent to run standalone: \"risk\", \"pnl\", \"research\", or \"manager\"")
	prompt := flag.String("prompt", "", "question to send the agent (defaults to a sensible one per -node if left blank)")
	ollamaModel := flag.String("model", "qwen3-coder:30b", "Ollama model to use (must support tool calling)")
	ollamaURL := flag.String("ollama-url", "http://localhost:21434", "Ollama server base URL")
	stateURL := flag.String("state-url", "", "catscope state address the risk agent's wallet tools read from (tcp://ip:port or unix:///path) -- required for -node risk/manager, same internal gRPC/geyser-backed graph the live trading bot itself reads through")
	dbPath := flag.String("db", filepath.Join(os.Getenv("HOME"), ".optimizer", "prefetch.db"), "path to prefetch.db, read by the pnl agent")
	papersDir := flag.String("papers-dir", filepath.Join(os.Getenv("HOME"), ".optimizer", "research-papers"), "directory of .pdf/.txt/.md files the research agent reads")
	wallet := flag.String("wallet", harness.DefaultWallet, "base58 wallet address the agents inspect (public key only -- read-only)")
	timeout := flag.Duration("timeout", 2*time.Minute, "time budget for the whole run")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	owner, err := sgo.PublicKeyFromBase58(*wallet)
	if err != nil {
		log.Fatalf("hedgefund: invalid -wallet %q: %v", *wallet, err)
	}

	chatModel, err := ollama.NewChatModel(ctx, &ollama.ChatModelConfig{
		BaseURL: *ollamaURL,
		Model:   *ollamaModel,
	})
	if err != nil {
		log.Fatalf("hedgefund: create ollama chat model: %v", err)
	}

	var (
		question string
		summary  string
	)
	switch *node {
	case "risk":
		question = *prompt
		if question == "" {
			question = "What's our real current risk exposure -- any live borrows, and is the wallet concentrated in anything?"
		}
		stateClient, err := harness.NewStateClient(ctx, *stateURL)
		if err != nil {
			log.Fatalf("hedgefund: %v", err)
		}
		agent, err := NewRiskAgent(ctx, chatModel, stateClient, owner)
		if err != nil {
			log.Fatalf("hedgefund: %v", err)
		}
		assessment, err := RunRiskAssessment(ctx, agent, question)
		if err != nil {
			log.Fatalf("hedgefund: %v", err)
		}
		summary = assessment.Summary

	case "pnl":
		question = *prompt
		if question == "" {
			question = "How has our recorded value changed over the last 7 days?"
		}
		db, err := store.Open(*dbPath)
		if err != nil {
			log.Fatalf("hedgefund: open prefetch db %s: %v", *dbPath, err)
		}
		defer func() { _ = db.Close() }()
		agent, err := NewPnlAgent(ctx, chatModel, db, owner)
		if err != nil {
			log.Fatalf("hedgefund: %v", err)
		}
		report, err := RunPnlReport(ctx, agent, question)
		if err != nil {
			log.Fatalf("hedgefund: %v", err)
		}
		summary = report.Summary

	case "research":
		question = *prompt
		if question == "" {
			question = "What new candidate trading models, if any, are worth proposing from the papers directory?"
		}
		agent, err := NewResearchAgent(ctx, chatModel, *papersDir)
		if err != nil {
			log.Fatalf("hedgefund: %v", err)
		}
		findings, err := RunResearch(ctx, agent, question)
		if err != nil {
			log.Fatalf("hedgefund: %v", err)
		}
		summary = findings.Summary

	case "manager":
		question = *prompt
		if question == "" {
			question = "Should we do anything right now, and why?"
		}
		db, err := store.Open(*dbPath)
		if err != nil {
			log.Fatalf("hedgefund: open prefetch db %s: %v", *dbPath, err)
		}
		defer func() { _ = db.Close() }()
		stateClient, err := harness.NewStateClient(ctx, *stateURL)
		if err != nil {
			log.Fatalf("hedgefund: %v", err)
		}
		wf, err := BuildDefaultFundManagerWorkflow(ctx, chatModel, stateClient, owner, db, *papersDir)
		if err != nil {
			log.Fatalf("hedgefund: %v", err)
		}
		decision, err := RunFundManagerWorkflow(ctx, wf, question)
		if err != nil {
			log.Fatalf("hedgefund: %v", err)
		}
		summary = formatFundDecision(decision)

	default:
		log.Fatalf("hedgefund: unknown -node %q (want \"risk\", \"pnl\", \"research\", or \"manager\")", *node)
	}

	fmt.Printf("wallet: %s\n> %s\n\n", owner, question)
	fmt.Println(summary)
}

// formatFundDecision renders a FundDecision for the CLI -- always leads
// with the PROPOSAL ONLY banner so nobody skimming the output mistakes
// this for a report of something that actually happened.
func formatFundDecision(d FundDecision) string {
	var sb strings.Builder
	sb.WriteString("=== PROPOSAL ONLY -- no real transaction has been sent ===\n\n")
	fmt.Fprintf(&sb, "Rationale: %s\n\n", d.Rationale)

	type flag struct {
		name string
		on   bool
		note string
	}
	flags := []flag{
		{"enable_pair_trading", d.EnablePairTrading, ""},
		{"enable_directional_trading", d.EnableDirectionalTrading, d.DirectionalMint},
		{"close_directional_position", d.CloseDirectionalPosition, ""},
		{"enable_dispersion_trading", d.EnableDispersionTrading, ""},
		{"close_dispersion_position", d.CloseDispersionPosition, ""},
		{"enable_hawkes_trading", d.EnableHawkesTrading, ""},
		{"close_hawkes_position", d.CloseHawkesPosition, ""},
	}
	any := false
	for _, f := range flags {
		if !f.on {
			continue
		}
		any = true
		if f.note != "" {
			fmt.Fprintf(&sb, "  PROPOSED: %s (target %s)\n", f.name, f.note)
		} else {
			fmt.Fprintf(&sb, "  PROPOSED: %s\n", f.name)
		}
	}
	if !any {
		sb.WriteString("  (no action proposed)\n")
	}
	return sb.String()
}
