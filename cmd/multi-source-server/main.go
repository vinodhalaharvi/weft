// Command multi-source-server demonstrates the full categorical story
// end-to-end across multiple processes:
//
//   1. Connects to TWO different MCP servers as a client:
//        - Claude Code (`claude mcp serve`) for shell access (Bash)
//        - npx @modelcontextprotocol/server-filesystem for files
//      Each is a separate subprocess speaking MCP over stdio.
//
//   2. Lifts specific tools from each source as typed weft.Arrows.
//
//   3. COMPOSES those arrows together — using weft.Pipe2, weft.Pure,
//      etc. — into NEW arrows that combine work from both sources.
//
//   4. Exposes the composed arrows as its own MCP server, also
//      speaking stdio. External MCP clients connect to THIS process
//      and call its tools, unaware that the work is being delegated
//      to two other subprocesses behind the scenes.
//
// The whole thing exercises the framework's lift-in / lift-out
// symmetry across heterogeneous external systems. If a client calls
// our `bash_then_save` tool, three subprocesses participate:
// claude mcp serve runs the shell command, server-filesystem writes
// the result to disk, our process orchestrates and reports back.
//
// Usage:
//
//	# Build, then point an MCP client at it. Most useful with the
//	# companion `test-multi-source` command in this repo:
//	go run ./cmd/multi-source-server /tmp
//
// The argument (/tmp here) is the directory that the filesystem
// server is allowed to read/write. Pick something safe.
//
// Tools exposed by this server:
//
//   - shell_run(command):
//       Runs the command via Claude Code's Bash tool. Pure pass-
//       through demonstrating mcp.ServeAsTool with a simple lifted
//       arrow.
//
//   - read_file(path):
//       Reads a file via the filesystem MCP server. Another simple
//       lift-out. Different source from shell_run, proving the
//       multi-source claim.
//
//   - bash_then_save(command, path):
//       Runs the command via Bash, writes the output to a file via
//       server-filesystem. The composition: two arrows from two
//       different MCP sources, joined into one tool. This is the
//       headline.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/vinodhalaharvi/weft/mcp"
	"github.com/vinodhalaharvi/weft/weft"
)

func main() {
	// === Setup ===========================================================
	// The single argument is the directory the filesystem server may
	// access. Default to /tmp if not given, which is enough for the
	// test-multi-source program to work.
	allowDir := "/tmp"
	if len(os.Args) > 1 {
		allowDir = os.Args[1]
	}

	// Logs go to stderr so they don't pollute the stdio MCP channel,
	// which uses stdout for protocol messages.
	logger := log.New(os.Stderr, "[multi-source-server] ", log.LstdFlags)
	logger.Printf("starting; filesystem allow dir = %s", allowDir)

	ctx := context.Background()

	// === Source A: Claude Code's Bash =====================================
	logger.Print("connecting to claude mcp serve...")
	claudeTransport, err := mcp.Stdio("claude", "mcp", "serve")
	if err != nil {
		logger.Fatalf("connect claude: %v", err)
	}
	claudeClient, err := mcp.Connect(ctx, claudeTransport)
	if err != nil {
		logger.Fatalf("init claude: %v", err)
	}
	defer claudeClient.Close()
	logger.Printf("✓ claude connected (%d tools)", len(claudeClient.Tools()))

	// === Source B: filesystem server ======================================
	logger.Print("connecting to filesystem MCP server...")
	fsTransport, err := mcp.Stdio("npx", "-y",
		"@modelcontextprotocol/server-filesystem", allowDir)
	if err != nil {
		logger.Fatalf("connect filesystem: %v", err)
	}
	fsClient, err := mcp.Connect(ctx, fsTransport)
	if err != nil {
		logger.Fatalf("init filesystem: %v", err)
	}
	defer fsClient.Close()
	logger.Printf("✓ filesystem connected (%d tools)", len(fsClient.Tools()))

	// === Lift tools as typed weft.Arrows ==================================
	// Inputs and outputs are kept generic — map[string]any for inputs
	// (any tool's args) and string for outputs (everything ends up as
	// text on the wire). Callers parse outputs as needed.
	bashRaw := mcp.Tool[map[string]any, string](claudeClient, "Bash")
	readRaw := mcp.Tool[map[string]any, string](fsClient, "read_text_file")
	writeRaw := mcp.Tool[map[string]any, string](fsClient, "write_file")

	// === Re-shape arrows as the tools we want to expose ===================
	//
	// The arrows above take map[string]any but our exposed tools take
	// flat domain types (RunIn, ReadIn, BashThenSaveIn). PreMap
	// translates one shape into the other so the composed arrow has
	// the type signature we want at the MCP boundary.

	shellRun := weft.PreMap(func(in RunIn) map[string]any {
		return map[string]any{"command": in.Command}
	}, bashRaw)

	readFile := weft.PreMap(func(in ReadIn) map[string]any {
		return map[string]any{"path": in.Path}
	}, readRaw)

	// The headline: run a shell command, then save the output to disk.
	// This is composition of two arrows backed by two different MCP
	// subprocesses, expressed as one weft pipeline.
	bashThenSave := func(ctx context.Context, in BashThenSaveIn) (BashThenSaveOut, error) {
		out, err := bashRaw(ctx, map[string]any{"command": in.Command})
		if err != nil {
			return BashThenSaveOut{}, fmt.Errorf("bash: %w", err)
		}
		// out is the JSON-shaped output Bash returns: {"stdout":...,...}.
		// Extract just stdout for saving.
		var bashResult struct {
			Stdout string `json:"stdout"`
		}
		_ = json.Unmarshal([]byte(out), &bashResult)
		toSave := bashResult.Stdout
		if toSave == "" {
			// Fallback: write the raw output if the structured shape
			// isn't what we expect.
			toSave = out
		}

		_, err = writeRaw(ctx, map[string]any{
			"path":    in.Path,
			"content": toSave,
		})
		if err != nil {
			return BashThenSaveOut{}, fmt.Errorf("write: %w", err)
		}
		return BashThenSaveOut{
			Saved: in.Path,
			Bytes: len(toSave),
		}, nil
	}

	// === Build OUR server, exposing the composed arrows ===================
	server := mcp.Serve(
		mcp.ServeAsTool("shell_run", shellRun,
			mcp.WithDescription("Run a shell command via Claude Code's Bash tool"),
			mcp.WithInputSchema(json.RawMessage(`{
				"type": "object",
				"properties": {
					"command": {"type": "string", "description": "Shell command to execute"}
				},
				"required": ["command"]
			}`)),
		),
		mcp.ServeAsTool("read_file", readFile,
			mcp.WithDescription("Read a text file via the filesystem MCP server"),
			mcp.WithInputSchema(json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Absolute path to file"}
				},
				"required": ["path"]
			}`)),
		),
		mcp.ServeAsTool("bash_then_save", weft.ArrowFunc(bashThenSave),
			mcp.WithDescription("Run a shell command and save its stdout to a file. "+
				"Composes Bash (Claude Code) with write_file (filesystem MCP)."),
			mcp.WithInputSchema(json.RawMessage(`{
				"type": "object",
				"properties": {
					"command": {"type": "string", "description": "Shell command to execute"},
					"path": {"type": "string", "description": "Where to save stdout"}
				},
				"required": ["command", "path"]
			}`)),
		),
	)

	logger.Print("server ready, listening on stdio...")

	// === Run the server over stdio ========================================
	// This blocks until the parent client disconnects.
	if err := mcp.RunStdioServer(server,
		mcp.WithServerInfo("weft-multi-source", "0.1.0"),
	); err != nil {
		logger.Fatalf("serve stdio: %v", err)
	}
}

// === Domain types for the exposed tools =================================

// RunIn is the input shape for the shell_run tool.
type RunIn struct {
	Command string `json:"command"`
}

// ReadIn is the input shape for the read_file tool.
type ReadIn struct {
	Path string `json:"path"`
}

// BashThenSaveIn is the input shape for the headline composed tool.
type BashThenSaveIn struct {
	Command string `json:"command"`
	Path    string `json:"path"`
}

// BashThenSaveOut reports what was saved.
type BashThenSaveOut struct {
	Saved string `json:"saved"`
	Bytes int    `json:"bytes"`
}
