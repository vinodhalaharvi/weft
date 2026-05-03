// Command mcp-stdio demonstrates weft's mcp.Stdio transport against
// any real MCP server speaking stdio.
//
// Usage:
//
//	# Default: connect to `claude mcp serve`, list its tools.
//	go run ./cmd/examples/mcp-stdio
//
//	# Call a specific tool on Claude Code with JSON arguments:
//	go run ./cmd/examples/mcp-stdio -tool Bash -args '{"command":"git log --oneline -5"}'
//
//	# Point at a different MCP server:
//	go run ./cmd/examples/mcp-stdio -- npx @modelcontextprotocol/server-filesystem /tmp
//
//	# Same, with a tool call:
//	go run ./cmd/examples/mcp-stdio -tool list_directory -args '{"path":"/tmp"}' \
//	    -- npx @modelcontextprotocol/server-filesystem /tmp
//
// What this demonstrates:
//
//   - mcp.Stdio is one transport that works for ANY stdio MCP server.
//     No per-server wrapper code needed.
//   - mcp.Tool[map[string]any, string] gives generic, type-erased
//     access to any tool. You pay zero code-generation cost.
//   - Once the tool is lifted as a weft.Arrow, it composes with
//     weft.Compose, weft.Par, weft.Traverse, weft.WithRetry, etc.
//     — same as in-process arrows.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/vinodhalaharvi/weft/mcp"
)

func main() {
	// Parse args: anything before `--` is a flag for us; anything
	// after is the MCP server command.
	var (
		toolName   string
		toolArgs   string
		timeoutSec int
	)

	command := "claude"
	cmdArgs := []string{"mcp", "serve"}

	consumed := false
	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "-h", "--help":
			usage()
			os.Exit(0)
		case "-tool":
			i++
			if i >= len(os.Args) {
				exit("error: -tool requires a value")
			}
			toolName = os.Args[i]
		case "-args":
			i++
			if i >= len(os.Args) {
				exit("error: -args requires a JSON value")
			}
			toolArgs = os.Args[i]
		case "-timeout":
			i++
			if i >= len(os.Args) {
				exit("error: -timeout requires seconds")
			}
			fmt.Sscanf(os.Args[i], "%d", &timeoutSec)
		case "--":
			rest := os.Args[i+1:]
			if len(rest) == 0 {
				exit("error: -- requires a command after it")
			}
			command = rest[0]
			cmdArgs = rest[1:]
			consumed = true
		default:
			if consumed {
				continue // already handled by --
			}
			fmt.Fprintf(os.Stderr, "unknown flag: %s\n", os.Args[i])
			usage()
			os.Exit(1)
		}
	}
	if timeoutSec == 0 {
		timeoutSec = 60
	}

	// === Connect ============================================================
	fmt.Printf("Connecting to: %s %s\n", command, strings.Join(cmdArgs, " "))
	fmt.Println(strings.Repeat("─", 70))

	transport, err := mcp.Stdio(command, cmdArgs...)
	if err != nil {
		exit(fmt.Sprintf("connect failed: %v", err))
	}

	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(timeoutSec)*time.Second)
	defer cancel()

	client, err := mcp.Connect(ctx, transport)
	if err != nil {
		exit(fmt.Sprintf("mcp.Connect failed: %v", err))
	}
	defer client.Close()

	// === List tools =========================================================
	tools := client.Tools()
	fmt.Printf("✓ Connected. %d tools available:\n", len(tools))
	for i, name := range tools {
		fmt.Printf("  %d. %s\n", i+1, name)
	}
	fmt.Println()

	// === If no -tool was passed, stop here ==================================
	if toolName == "" {
		fmt.Println("Pass -tool NAME -args '<json>' to actually call a tool.")
		fmt.Println("Example:")
		fmt.Println("  go run ./cmd/examples/mcp-stdio -tool Bash -args '{\"command\":\"date\"}'")
		return
	}

	// === Call the tool ======================================================
	// Lift the tool as a typed weft.Arrow. We use map[string]any for
	// inputs and string for outputs — the most generic shape that
	// works for any tool returning text. For tools returning JSON, you
	// could use a struct or another map[string]any as the output type.
	tool := mcp.Tool[map[string]any, string](client, toolName)

	// Decode the args JSON.
	var args map[string]any
	if toolArgs != "" {
		if err := json.Unmarshal([]byte(toolArgs), &args); err != nil {
			exit(fmt.Sprintf("invalid -args JSON: %v", err))
		}
	}

	fmt.Printf("Calling tool %q with args: %s\n", toolName, toolArgs)
	fmt.Println(strings.Repeat("─", 70))

	output, err := tool(ctx, args)
	if err != nil {
		// Print stderr from the subprocess if we can, since errors
		// often originate there.
		exit(fmt.Sprintf("tool call failed: %v", err))
	}

	fmt.Println(output)
}

func usage() {
	fmt.Println("usage: mcp-stdio [flags] [-- COMMAND ARGS...]")
	fmt.Println()
	fmt.Println("Connects to an MCP stdio server, lists its tools, and optionally")
	fmt.Println("calls one of them.")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  -tool NAME       Tool to call (default: just list tools)")
	fmt.Println("  -args JSON       JSON object of arguments for the tool")
	fmt.Println("  -timeout SECS    Overall timeout in seconds (default: 60)")
	fmt.Println()
	fmt.Println("Server selection:")
	fmt.Println("  Default: claude mcp serve")
	fmt.Println("  Override: -- COMMAND ARGS...")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  mcp-stdio")
	fmt.Println("  mcp-stdio -tool Bash -args '{\"command\":\"date\"}'")
	fmt.Println("  mcp-stdio -- npx @modelcontextprotocol/server-filesystem /tmp")
}

func exit(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
