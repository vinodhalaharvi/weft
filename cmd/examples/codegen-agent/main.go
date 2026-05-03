// Command codegen-agent demonstrates an agent doing real codegen via
// MCP tools. Unlike cmd/examples/codegen-llm — which runs a fixed
// pipeline (one LLM call per file) — this is open-ended: Claude
// decides which files to look at, what changes to make, and applies
// them itself via Edit and Write.
//
// This is essentially "Claude Code in 200 lines, on top of weft."
// The framework does the glue (lift MCP tools as arrows, wrap Claude
// in a Loop, compose with WithTap for observability); Claude does
// the intelligence.
//
// What it does on a typical run:
//   1. Sets up a sandbox at /tmp/codegen-demo with three Go files
//      that have undocumented exported functions
//   2. Connects to Claude Code's MCP server, lifts six tools
//      (Glob, Read, Edit, Write, Grep, Bash)
//   3. Wraps llm.Claude with llm.Loop, binds the lifted tools
//   4. Runs a prompt asking Claude to add docstrings
//   5. You read the diff via `git diff` or by inspecting the files
//
// Tool budget: usually 5-15 tool calls, 3-7 LLM iterations, ~5-20
// cents in API costs. MaxIter caps at 25 — generous because real
// codegen often takes more turns than a simple Q&A.
//
// IMPORTANT: This tool is given Edit and Write permissions on a
// directory. By default that's /tmp/codegen-demo, which we create
// and own. If you point it elsewhere, make sure you can git-revert
// any unwanted changes.
//
// Usage:
//
//	export ANTHROPIC_API_KEY=sk-ant-...
//	go run ./cmd/examples/codegen-agent
//	    # uses the default /tmp/codegen-demo sandbox
//
//	go run ./cmd/examples/codegen-agent -dir /path/to/your/code -task "..."
//	    # pointed at your own directory; CAREFUL.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vinodhalaharvi/weft/llm"
	"github.com/vinodhalaharvi/weft/mcp"
	"github.com/vinodhalaharvi/weft/weft"
)

const model = "claude-sonnet-4-5-20250929"

const defaultTask = `Look at all .go files in this directory. For every exported function (capitalized name) that does not have a doc comment immediately above it, add a one-line godoc-style comment. Follow Go conventions: the comment must start with the function's name, be one sentence, and end with a period. Don't modify functions that already have doc comments. Use Edit to make the changes — never overwrite a whole file with Write unless absolutely necessary.`

func main() {
	dir := flag.String("dir", "/tmp/codegen-demo", "directory to operate on")
	task := flag.String("task", defaultTask, "high-level instruction for the agent")
	skipSetup := flag.Bool("skip-setup", false, "don't recreate the demo files")
	flag.Parse()

	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Fatal("ANTHROPIC_API_KEY not set")
	}

	logger := log.New(os.Stderr, "[codegen-agent] ", log.LstdFlags)

	// === Sandbox setup ==================================================
	// If we're running on the default demo dir, create the seed files.
	// If the user pointed at their own dir, leave it alone.
	if *dir == "/tmp/codegen-demo" && !*skipSetup {
		if err := setupDemo(*dir); err != nil {
			logger.Fatalf("setup demo: %v", err)
		}
		logger.Printf("✓ created sandbox at %s", *dir)
	}

	// Resolve to canonical path (handles macOS /tmp → /private/tmp).
	resolvedDir, err := filepath.EvalSymlinks(*dir)
	if err == nil {
		*dir = resolvedDir
	}

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

	// === Lift tools as ToolBindings =====================================
	// The set of tools Claude can call. These cover the codegen need:
	// find files (Glob), read them (Read), edit/write them (Edit, Write),
	// search content (Grep), run commands (Bash, e.g. for `gofmt`).
	tools := []llm.ToolBinding{
		bindTool(mcpClient, "Glob",
			"Find files matching a pattern like '**/*.go'.",
			`{"type":"object",
			  "properties":{
			    "pattern":{"type":"string","description":"glob pattern"},
			    "path":{"type":"string","description":"directory to search; default cwd"}
			  },
			  "required":["pattern"]}`),

		bindTool(mcpClient, "Read",
			"Read a file. Returns its contents with line numbers.",
			`{"type":"object",
			  "properties":{
			    "file_path":{"type":"string","description":"absolute path to file"}
			  },
			  "required":["file_path"]}`),

		bindTool(mcpClient, "Edit",
			"Replace an exact string in a file with a new string. The old_string must appear exactly once in the file (or set replace_all=true). Use this for targeted changes.",
			`{"type":"object",
			  "properties":{
			    "file_path":{"type":"string","description":"absolute path"},
			    "old_string":{"type":"string","description":"exact text to find"},
			    "new_string":{"type":"string","description":"replacement text"},
			    "replace_all":{"type":"boolean","description":"replace all occurrences"}
			  },
			  "required":["file_path","old_string","new_string"]}`),

		bindTool(mcpClient, "Write",
			"Write a file, creating it or overwriting if it exists. Prefer Edit for targeted changes.",
			`{"type":"object",
			  "properties":{
			    "file_path":{"type":"string","description":"absolute path"},
			    "content":{"type":"string","description":"file contents"}
			  },
			  "required":["file_path","content"]}`),

		bindTool(mcpClient, "Grep",
			"Search file contents using ripgrep.",
			`{"type":"object",
			  "properties":{
			    "pattern":{"type":"string","description":"regex pattern"},
			    "path":{"type":"string","description":"path to search"},
			    "glob":{"type":"string","description":"optional glob filter"}
			  },
			  "required":["pattern"]}`),

		bindTool(mcpClient, "Bash",
			"Execute a shell command. Useful for running gofmt, go vet, git diff, etc.",
			`{"type":"object",
			  "properties":{
			    "command":{"type":"string"},
			    "description":{"type":"string"}
			  },
			  "required":["command"]}`),
	}

	// === Build the agent =================================================
	agent := llm.Loop(
		llm.Claude(model),
		tools,
		llm.WithMaxIter(25),
		llm.WithIterCallback(func(iter int, resp llm.Response) {
			calls := resp.ToolCalls()
			names := make([]string, len(calls))
			for i, c := range calls {
				names[i] = c.ToolName
			}
			if len(calls) > 0 {
				logger.Printf("iter %d: tools=%v", iter, names)
			} else {
				logger.Printf("iter %d: final response (%d chars)",
					iter, len(resp.Text()))
			}
		}),
	)

	// Outer tap for total usage.
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
	systemPrompt := fmt.Sprintf(
		`You are a careful Go programmer working in the directory %s. `+
			`You have read/write/edit access to that directory only. `+
			`When making changes, prefer Edit (targeted string replacement) over `+
			`Write (full overwrite). Verify changes by re-reading files when in `+
			`doubt. Be concise — make the requested changes and stop. Don't `+
			`refactor things that weren't asked for.`,
		*dir)

	userPrompt := fmt.Sprintf(
		"Working directory: %s\n\nTask: %s",
		*dir, *task)

	logger.Printf("starting agent in %s", *dir)
	logger.Printf("task: %s", *task)
	fmt.Println(strings.Repeat("─", 70))

	resp, err := agent(ctx, llm.Prompt{
		System:    systemPrompt,
		Messages:  []llm.Message{llm.UserText(userPrompt)},
		MaxTokens: 8192,
	})
	if err != nil {
		logger.Fatalf("agent: %v", err)
	}

	fmt.Println(strings.Repeat("─", 70))
	fmt.Println("Agent's final report:")
	fmt.Println(strings.Repeat("─", 70))
	fmt.Println(resp.Text())
	fmt.Println()
	fmt.Println(strings.Repeat("─", 70))
	fmt.Printf("Inspect changes:  ls -la %s/  &&  cat %s/*.go\n", *dir, *dir)
	fmt.Println(strings.Repeat("─", 70))
}

// bindTool packages an MCP tool as an llm.ToolBinding the loop can dispatch.
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

// setupDemo creates a sandbox directory with three Go files that have
// undocumented exported functions. Used by default so the demo is
// self-contained — no need to manually prepare files.
func setupDemo(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clean dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	files := map[string]string{
		"strings.go": `package demo

import "strings"

func Reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

func WordCount(text string) int {
	return len(strings.Fields(text))
}

// helper is internal — should not be touched.
func helper(s string) string {
	return strings.TrimSpace(s)
}
`,

		"math.go": `package demo

func Fibonacci(n int) int {
	if n < 2 {
		return n
	}
	return Fibonacci(n-1) + Fibonacci(n-2)
}

func Sum(xs []int) int {
	total := 0
	for _, x := range xs {
		total += x
	}
	return total
}

// IsEven returns true if n is divisible by 2.
func IsEven(n int) bool {
	return n%2 == 0
}
`,

		"set.go": `package demo

type StringSet struct {
	items map[string]struct{}
}

func NewStringSet() *StringSet {
	return &StringSet{items: make(map[string]struct{})}
}

func (s *StringSet) Add(item string) {
	s.items[item] = struct{}{}
}

func (s *StringSet) Contains(item string) bool {
	_, ok := s.items[item]
	return ok
}

func (s *StringSet) Size() int {
	return len(s.items)
}
`,
	}

	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}
