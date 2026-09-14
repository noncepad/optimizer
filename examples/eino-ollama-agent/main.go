// Command eino-ollama-agent is a minimal example of building a tool-calling
// agent with github.com/cloudwego/eino.
//
// It wires up:
//   - a chat model - main() talks to a local OpenAI-API-compatible server
//     (github.com/cloudwego/eino-ext/components/model/openai); main2() is a
//     parallel example against Ollama itself
//     (github.com/cloudwego/eino-ext/components/model/ollama)
//   - two demo tools (get_time, calculate) defined with eino's InferTool,
//     which derives each tool's JSON schema from a Go struct
//   - eino's prebuilt ReAct agent (github.com/cloudwego/eino/flow/agent/react),
//     which loops: call the model -> if it asked for a tool, run the tool and
//     feed the result back -> repeat until the model answers without a tool call
//
// Not every model works here: the ReAct loop above depends on native
// tool-calling support (the model must be able to emit a "tool_calls"
// field), which rules out e.g. deepseek-r1.
//
// Usage:
//
//	go run ./examples/eino-ollama-agent -prompt "What time is it in Tokyo, and what is 17*23?"
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/cloudwego/eino-ext/components/model/ollama"
	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
)

func main() {
	if true {
		mainV2()
	} else {
		mainV1()
	}
}

func mainV1() {
	prompt := flag.String("prompt", "What time is it in Tokyo, and what is 17*23?", "message to send the agent")
	timeout := flag.Duration("timeout", 5*time.Minute, "time budget for the whole run")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	// Local glm server, OpenAI-API-compatible - see main2/run for the
	// Ollama-backed equivalent.
	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		BaseURL: "http://127.0.0.1:21434/v1",
		APIKey:  "abc123",
		Model:   "glm-5.2", // matches the server's own /v1/models id exactly
	})
	if err != nil {
		log.Fatalf("eino-ollama-agent: create chat model: %v", err)
	}

	if err := run(ctx, chatModel, *prompt); err != nil {
		log.Fatalf("eino-ollama-agent: %v", err)
	}
}

func mainV2() {
	prompt := flag.String("prompt", "What time is it in Tokyo, and what is 17*23?", "message to send the agent")
	model := flag.String("model", "qwen2.5:7b", "Ollama model to use (must support tool calling)")
	baseURL := flag.String("ollama-url", "http://localhost:11434", "Ollama server base URL")
	timeout := flag.Duration("timeout", 2*time.Minute, "time budget for the whole run")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	chatModel, err := ollama.NewChatModel(ctx, &ollama.ChatModelConfig{
		BaseURL: *baseURL,
		Model:   *model,
	})
	if err != nil {
		log.Fatalf("eino-ollama-agent: create ollama chat model: %v", err)
	}

	if err := run(ctx, chatModel, *prompt); err != nil {
		log.Fatalf("eino-ollama-agent: %v", err)
	}
}

// run is the harness shared by main/main2: it wraps chatModel in a ReAct
// agent (see package doc comment) with demoTools available, sends prompt
// as the user's message, and prints whatever the agent ultimately answers
// once its tool-calling loop settles on a final response.
func run(ctx context.Context, chatModel model.ToolCallingChatModel, prompt string) error {
	tools, err := demoTools()
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

	fmt.Printf("> %s\n\n", prompt)

	msg, err := agent.Generate(ctx, []*schema.Message{schema.UserMessage(prompt)})
	if err != nil {
		return fmt.Errorf("agent.Generate: %w", err)
	}

	fmt.Println(msg.Content)
	return nil
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
