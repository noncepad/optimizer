package main

import (
	"context"
	"errors"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// fakeToolCallingModel is a minimal model.ToolCallingChatModel test
// double: it returns a fixed response (or error) regardless of input and
// records the last input it was called with -- unlike go-wiki/client/
// hedgefund's own fakeChatModel (model.BaseChatModel only), this
// additionally implements WithTools so it satisfies what react.NewAgent
// requires for risk.go/pnl.go/research.go-shaped ReAct agents. Since
// Generate always returns a final answer message (no tool_calls), the
// ReAct loop terminates after exactly one model call -- no tool
// invocation ever happens, so an agent built on this never touches real
// RPC/DB/filesystem, only whatever fake tools (if any) are wired in
// alongside it.
type fakeToolCallingModel struct {
	response string
	err      error
	calls    int
	// captured is the last Generate call's input messages -- lets a test
	// assert what a node actually sent it (e.g. that fund_manager's
	// prompt really contains each sub-agent's own distinct output),
	// rather than just that some value was produced.
	captured []*schema.Message
}

func (f *fakeToolCallingModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	f.calls++
	f.captured = input
	if f.err != nil {
		return nil, f.err
	}
	return schema.AssistantMessage(f.response, nil), nil
}

func (f *fakeToolCallingModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("fakeToolCallingModel: Stream not implemented")
}

// WithTools is a no-op returning the same fake -- Generate never emits a
// tool call, so nothing ever inspects which tools were bound.
func (f *fakeToolCallingModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return f, nil
}
