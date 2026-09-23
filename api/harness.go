// Command harness is a minimal tool-calling agent, built the same way as
// go-wiki/examples/eino-ollama-agent/main.go (github.com/cloudwego/eino's
// prebuilt ReAct agent: call the model -> if it asked for a tool, run the
// tool and feed the result back -> repeat until the model answers without
// a tool call), but with walletTools() (see tools.go) in place of that
// example's demoTools().
//
// walletTools() gives the agent read-only visibility into this repo's
// real trading wallet: its Solend/Kamino lending obligations, its raw
// token balances, live Jupiter USD prices, and total portfolio value --
// the exact queries run by hand, script by script, over the course of a
// live session unwinding real positions in this wallet. It does not
// expose any of that session's mutating actions (repay/withdraw/sweep);
// see walletTools' own doc comment for why.
//
// Usage:
//
//	go run . -prompt "What obligations are currently open, and what's the wallet worth right now?"
package api

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/state"
	"github.com/cloudwego/eino-ext/components/model/ollama"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	sgo "github.com/gagliardetto/solana-go"
)

// DefaultWallet is the trading child wallet every WalletTools() query
// targets by default -- the same real wallet
// (common.DeriveChildKeyFromIndex(parentKey, 1)) this session's manual
// scripts (checkob, manualrepay, manualwithdraw, sweeptousdc) all
// operated on. Override with -wallet to point this harness at a
// different address without needing the fee-payer keypair at all --
// every tool here is read-only, so only the public key is ever needed.
// Exported so other packages building on this one (e.g.
// hedgefund's agents) share the same default without
// duplicating the literal.
const DefaultWallet = "Hg2p3cfmg3dratEVy94JArTVM7KzEywgNdmFnrfhroh9"

func main_run() {
	prompt := flag.String("prompt", "What Solend/Kamino obligations are currently open, and what is the wallet's total value in USD right now?", "message to send the agent")
	ollamaModel := flag.String("model", "qwen3-coder:30b", "Ollama model to use (must support tool calling)")
	ollamaURL := flag.String("ollama-url", "http://localhost:21434", "Ollama server base URL")
	stateURL := flag.String("state-url", "", "catscope state address the wallet tools read from (tcp://ip:port or unix:///path) -- same internal gRPC/geyser-backed graph the live trading bot itself reads through, not the public Solana RPC endpoint")
	wallet := flag.String("wallet", DefaultWallet, "base58 wallet address the wallet tools operate on (public key only -- no private key needed, every tool is read-only)")
	timeout := flag.Duration("timeout", 2*time.Minute, "time budget for the whole run")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	owner, err := sgo.PublicKeyFromBase58(*wallet)
	if err != nil {
		log.Fatalf("harness: invalid -wallet %q: %v", *wallet, err)
	}

	stateClient, err := NewStateClient(ctx, *stateURL)
	if err != nil {
		log.Fatalf("harness: %v", err)
	}

	chatModel, err := ollama.NewChatModel(ctx, &ollama.ChatModelConfig{
		BaseURL: *ollamaURL,
		Model:   *ollamaModel,
	})
	if err != nil {
		log.Fatalf("harness: create ollama chat model: %v", err)
	}

	if err := run(ctx, chatModel, stateClient, owner, *prompt); err != nil {
		log.Fatalf("harness: %v", err)
	}
}

// NewStateClient dials the same internal state.Client/gRPC graph
// endpoint every other read in this codebase uses (see
// cmd/multimodel.go's identical stateAddr/DefaultDialer construction),
// given a raw -state-url flag value. Exported so other standalone
// entrypoints building on this package (e.g. optimizer/hedgefund's CLI)
// can dial the same way without duplicating this parsing.
func NewStateClient(ctx context.Context, stateURL string) (state.Client, error) {
	if len(stateURL) == 0 {
		return state.Client{}, fmt.Errorf("-state-url is required (tcp://ip:port or unix:///path to a catscope state endpoint)")
	}
	stateAddr, err := bidder.ParseAddress(stateURL)
	if err != nil {
		return state.Client{}, fmt.Errorf("parse -state-url %q: %w", stateURL, err)
	}
	return state.New(ctx, state.DefaultDialer(stateAddr), 30*time.Second), nil
}

// run is the harness itself: wrap chatModel in a ReAct agent with
// walletTools available, send prompt as the user's message, and print
// whatever the agent ultimately answers once its tool-calling loop
// settles on a final response -- structurally identical to the reference
// example's run(), just with walletTools(stateClient, owner) standing in
// for demoTools().
func run(ctx context.Context, chatModel model.ToolCallingChatModel, stateClient state.Client, owner sgo.PublicKey, prompt string) error {
	tools, err := WalletTools(stateClient, owner)
	if err != nil {
		return fmt.Errorf("build tools: %w", err)
	}

	agent, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: chatModel,
		ToolsConfig:      compose.ToolsNodeConfig{Tools: tools},
	})
	if err != nil {
		return fmt.Errorf("create react agent: %w", err)
	}

	fmt.Printf("wallet: %s\n> %s\n\n", owner, prompt)

	msg, err := agent.Generate(ctx, []*schema.Message{schema.UserMessage(prompt)})
	if err != nil {
		return fmt.Errorf("agent.Generate: %w", err)
	}

	fmt.Println(msg.Content)
	return nil
}

type Base interface {
	Ctx() context.Context
	CloseSignal() <-chan error
	Close() error
}
