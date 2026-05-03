// Command agent demonstrates llm.Loop tying together a real LLM and
// real MCP tools.
//
// What it does:
//   1. Connects to `claude mcp serve` and lifts a few of its tools as
//      weft.Arrows (via mcp.Tool[...]).
//   2. Wraps llm.Claude with llm.Loop, binding the lifted tools so
//      the model can call them mid-conversation.
//   3. Runs the resulting Arrow[Prompt, Response] against your prompt.
//      The model decides on its own which tools to call and when.
//
// The model can:
//   - Run shell commands  (Bash)
//   - Read files          (Read)
//   - Search files        (Grep)
//   - Find files          (Glob)
//
// Usage:
//
//	export ANTHROPIC_API_KEY=sk-ant-...
//	go run ./cmd/examples/agent "How many .go files are in this repo? Use any tools you need."
//
// Per-iteration progress logs go to stderr so you can watch the loop think.
//
// Cost: a few cents per run depending on how much the model explores.
// MaxIter caps runaway loops at 12 iterations.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/vinodhalaharvi/weft/llm"
	"github.com/vinodhalaharvi/weft/mcp"
	"github.com/vinodhalaharvi/weft/weft"
)

const model = "claude-sonnet-4-5-20250929"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: agent <prompt>")
		fmt.Fprintln(os.Stderr, "example:")
		fmt.Fprintln(os.Stderr, `  agent "how many Go files in this repo?"`)
		os.Exit(1)
	}
	userPrompt := strings.Join(os.Args[1:], " ")

	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Fatal("ANTHROPIC_API_KEY not set")
	}

	logger := log.New(os.Stderr, "[agent] ", log.LstdFlags)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// === Connect to Claude Code's MCP server ============================
	logger.Print("connecting to claude mcp serve...")
	transport, err := mcp.Stdio("claude", "mcp", "serve")
	if err != nil {
		logger.Fatalf("spawn claude: %v", err)
	}
	mcpClient, err := mcp.Connect(ctx, transport)
	if err != nil {
		logger.Fatalf("init claude: %v", err)
	}
	defer mcpClient.Close()
	logger.Printf("✓ connected, %d tools available", len(mcpClient.Tools()))

	// === Lift the tools we want as weft.Arrows ==========================
	// Claude Code exposes 16 tools, but the loop only needs to know
	// about the ones we're willing to expose. Less attack surface,
	// less noise in the prompt.
	tools := []llm.ToolBinding{
		bindTool(mcpClient, "Bash",
			"Execute a shell command. Returns stdout and stderr.",
			`{"type":"object",
			  "properties":{
			    "command":{"type":"string","description":"shell command"},
			    "description":{"type":"string","description":"5-10 word summary"}
			  },
			  "required":["command"]}`),

		bindTool(mcpClient, "Read",
			"Read a file from disk. Returns its contents as text.",
			`{"type":"object",
			  "properties":{
			    "file_path":{"type":"string","description":"absolute path to file"}
			  },
			  "required":["file_path"]}`),

		bindTool(mcpClient, "Glob",
			"Find files matching a glob pattern like '**/*.go'.",
			`{"type":"object",
			  "properties":{
			    "pattern":{"type":"string"}
			  },
			  "required":["pattern"]}`),

		bindTool(mcpClient, "Grep",
			"Search file contents using ripgrep. Returns matching lines.",
			`{"type":"object",
			  "properties":{
			    "pattern":{"type":"string","description":"regex pattern"},
			    "path":{"type":"string","description":"path to search"}
			  },
			  "required":["pattern"]}`),
	}

	// === Build the agent =================================================
	// llm.Loop returns a normal weft.Arrow[Prompt, Response]. It
	// composes with anything else — here we wrap with WithTap to
	// observe the final usage from outside the loop.
	agent := llm.Loop(
		llm.Claude(model),
		tools,
		llm.WithMaxIter(12),
		llm.WithIterCallback(func(iter int, resp llm.Response) {
			logger.Printf("iter %d: stop=%v, %d tool call(s)",
				iter, resp.StopReason, len(resp.ToolCalls()))
		}),
	)

	// Outer tap shows the loop is just an arrow — wrap it however.
	agent = weft.WithTap[llm.Prompt, llm.Response](
		func(_ llm.Prompt, resp llm.Response, err error) {
			if err == nil {
				logger.Printf("total usage: in=%d out=%d cache_r=%d",
					resp.Usage.InputTokens,
					resp.Usage.OutputTokens,
					resp.Usage.CacheReadTokens)
			}
		},
	)(agent)

	// === Run =============================================================
	prompt := llm.Prompt{
		System: "You are a careful assistant with access to filesystem and " +
			"shell tools. Use them to answer the user's question accurately. " +
			"Only call tools when necessary.",
		Messages:  []llm.Message{llm.UserText(userPrompt)},
		MaxTokens: 4096,
	}

	resp, err := agent(ctx, prompt)
	if err != nil {
		logger.Fatalf("agent: %v", err)
	}

	fmt.Println()
	fmt.Println(strings.Repeat("─", 70))
	fmt.Println("Final answer:")
	fmt.Println(strings.Repeat("─", 70))
	fmt.Println(resp.Text())
}

// bindTool lifts an MCP tool by name and packages it as an
// llm.ToolBinding the loop can dispatch. The handler decodes the
// LLM's raw JSON args into a map[string]any, then calls the lifted
// arrow. The string return (a JSON-shaped tool output) flows back
// into the conversation as a tool_result block.
func bindTool(client *mcp.Client, name, desc, inputSchema string) llm.ToolBinding {
	tool := mcp.Tool[map[string]any, string](client, name)
	return llm.ToolBinding{
		Spec: llm.ToolSpec{
			Name:        name,
			Description: desc,
			InputSchema: json.RawMessage(inputSchema),
		},
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			var argMap map[string]any
			if err := json.Unmarshal(args, &argMap); err != nil {
				return "", fmt.Errorf("decode args: %w", err)
			}
			return tool(ctx, argMap)
		},
	}
}
