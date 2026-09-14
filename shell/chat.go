package shell

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudwego/eino-ext/components/model/ollama"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
)

type ChatBuilder interface {
	Create(context.Context) (ChatPrompter, error)
}

func CreateExampleChat(
	ctx context.Context,
	model string,
	baseURL string,
) (ChatBuilder, error) {
	ecb := new(exampleChatBuilder)
	ecb.model = model
	ecb.baseURL = baseURL
	return ecb, nil
}

type ChatPrompter interface {
	Prompt(string) (string, error)
}

type exampleChatBuilder struct {
	ctx     context.Context
	model   string
	baseURL string
}
type examplePrompter struct {
	ctx   context.Context
	agent *react.Agent
}

func (ep *examplePrompter) Prompt(prompt string) (string, error) {
	msg, err := ep.agent.Generate(ep.ctx, []*schema.Message{schema.UserMessage(prompt)})
	if err != nil {
		return "", fmt.Errorf("agent.Generate: %w", err)
	}
	return msg.Content, nil
}

func (ecb *exampleChatBuilder) Create(ctx context.Context) (ChatPrompter, error) {
	var err error
	ep := new(examplePrompter)
	ep.ctx = ctx
	var tools []tool.BaseTool
	tools, err = demoTools()
	if err != nil {
		return nil, fmt.Errorf("tooling failed: %s", err)
	}
	var chat *ollama.ChatModel
	chat, err = ollama.NewChatModel(ctx, &ollama.ChatModelConfig{
		BaseURL: ecb.baseURL,
		Model:   ecb.model,
	})
	if err != nil {
		return nil, fmt.Errorf("eino-ollama-agent: create ollama chat model: %v", err)
	}
	ep.agent, err = react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: chat,
		ToolsConfig:      compose.ToolsNodeConfig{Tools: tools},
	})
	if err != nil {
		return nil, fmt.Errorf("create react agent: %w", err)
	}
	return ep, nil
}

// timeInput/calcInput are the argument shapes InferTool reflects into a JSON
// schema for the model to fill in when it decides to call each tool.
type timeInput struct {
	TimeZone string `json:"time_zone" jsonschema:"description=IANA time zone name (e.g. America/New_York, Asia/Tokyo, UTC)"`
}

type calcInput struct {
	A  float64 `json:"a" jsonschema:"description=left-hand operand"`
	B  float64 `json:"b" jsonschema:"description=right-hand operand"`
	Op string  `json:"op" jsonschema:"description=one of + - * /,enum=+,enum=-,enum=*,enum=/"`
}

func demoTools() ([]tool.BaseTool, error) {
	getTime, err := utils.InferTool("get_time", "Get the current time in a given IANA time zone.",
		func(_ context.Context, in timeInput) (string, error) {
			loc, err := time.LoadLocation(in.TimeZone)
			if err != nil {
				return "", fmt.Errorf("unknown time zone %q: %w", in.TimeZone, err)
			}
			return time.Now().In(loc).Format(time.RFC1123), nil
		})
	if err != nil {
		return nil, err
	}

	calculate, err := utils.InferTool("calculate", "Perform one arithmetic operation on two numbers.",
		func(_ context.Context, in calcInput) (float64, error) {
			switch in.Op {
			case "+":
				return in.A + in.B, nil
			case "-":
				return in.A - in.B, nil
			case "*":
				return in.A * in.B, nil
			case "/":
				if in.B == 0 {
					return 0, fmt.Errorf("division by zero")
				}
				return in.A / in.B, nil
			default:
				return 0, fmt.Errorf("unsupported op %q", in.Op)
			}
		})
	if err != nil {
		return nil, err
	}

	return []tool.BaseTool{getTime, calculate}, nil
}
