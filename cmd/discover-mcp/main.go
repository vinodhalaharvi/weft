// Command discover-mcp connects to any MCP stdio server and prints its
// tool surface — names, descriptions, input schemas. Run this against
// `claude mcp serve` (or any other MCP server) before building a real
// integration, so you know what tools exist and what their schemas look
// like.
//
// This is a deliberately standalone program. It uses mark3labs/mcp-go
// directly rather than weft's mcp package, because we use it to inform
// the design of weft's stdio transport — the chicken-and-egg problem.
//
// Usage:
//
//	# Default: try `claude mcp serve` (available if Claude Code is installed)
//	go run ./cmd/discover-mcp
//
//	# Any other MCP server:
//	go run ./cmd/discover-mcp -- npx @modelcontextprotocol/server-filesystem /tmp
//	go run ./cmd/discover-mcp -- ./my-mcp-server
//
// What you'll see:
//
//   - Server name and version (from the MCP handshake)
//   - List of tools with their descriptions
//   - The input JSON schema for each tool
//   - Any error from any stage (subprocess spawn, handshake, list)
//
// What to do with the output:
//
//   - Confirms whether your MCP server is reachable from Go
//   - Tells us which tool names to use in subsequent demos
//   - Shows the input shapes — useful for designing typed Go shapes
//     to round-trip through mcp.Tool[In, Out] later
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

func main() {
	// Parse args. Everything after `--` is the MCP server command.
	command := "claude"
	args := []string{"mcp", "serve"}

	for i, a := range os.Args[1:] {
		if a == "--" {
			rest := os.Args[i+2:]
			if len(rest) == 0 {
				fmt.Fprintln(os.Stderr, "error: -- requires a command after it")
				os.Exit(1)
			}
			command = rest[0]
			args = rest[1:]
			break
		}
		if a == "-h" || a == "--help" {
			usage()
			os.Exit(0)
		}
	}

	fmt.Printf("Connecting to MCP server: %s %s\n", command, strings.Join(args, " "))
	fmt.Println(strings.Repeat("─", 70))

	// Spawn the subprocess and set up stdio pipes. The mcp-go client
	// library does the framing, multiplexing, and lifecycle.
	c, err := client.NewStdioMCPClient(command, nil, args...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: failed to start MCP server subprocess: %v\n", err)
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Common causes:")
		fmt.Fprintln(os.Stderr, "  - Command not found on PATH")
		fmt.Fprintln(os.Stderr, "  - Claude Code not installed (try: npm i -g @anthropic-ai/claude-code)")
		os.Exit(1)
	}
	defer c.Close()

	// MCP requires an initialize handshake before any other call works.
	// 30 seconds should be ample; if your server takes longer to boot,
	// it has bigger problems.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "weft-discover-mcp",
		Version: "0.1.0",
	}

	initResp, err := c.Initialize(ctx, initReq)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: handshake failed: %v\n", err)
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Common causes:")
		fmt.Fprintln(os.Stderr, "  - Server doesn't speak MCP")
		fmt.Fprintln(os.Stderr, "  - Server crashed before handshake; check stderr below")
		printStderr(c)
		os.Exit(1)
	}

	fmt.Printf("✓ Connected\n")
	fmt.Printf("  Server:           %s %s\n",
		initResp.ServerInfo.Name, initResp.ServerInfo.Version)
	fmt.Printf("  Protocol version: %s\n", initResp.ProtocolVersion)
	if initResp.Capabilities.Tools != nil {
		fmt.Printf("  Tools capability: present\n")
	}
	if initResp.Capabilities.Resources != nil {
		fmt.Printf("  Resources cap:    present\n")
	}
	if initResp.Capabilities.Prompts != nil {
		fmt.Printf("  Prompts cap:      present\n")
	}
	fmt.Println()

	// Now list tools. This is the headline output — what tools does
	// the server expose?
	listResp, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: list tools failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Tools (%d total):\n", len(listResp.Tools))
	fmt.Println(strings.Repeat("─", 70))

	for i, tool := range listResp.Tools {
		fmt.Printf("\n[%d] %s\n", i+1, tool.Name)
		if desc := strings.TrimSpace(tool.Description); desc != "" {
			fmt.Printf("    Description: %s\n", truncateMultiline(desc, 200))
		}

		// Input schema — pretty-print so it's readable.
		schemaJSON, err := json.MarshalIndent(tool.InputSchema, "    ", "  ")
		if err == nil && len(schemaJSON) > 2 {
			fmt.Printf("    Input schema:\n    %s\n", schemaJSON)
		}
	}

	fmt.Println()
	fmt.Println(strings.Repeat("─", 70))
	fmt.Printf("Discovery complete. %d tools found.\n", len(listResp.Tools))
	fmt.Println()
	fmt.Println("Next step: pick one or two tools above and tell weft what shape")
	fmt.Println("to expect, so we can wire them as typed weft.Arrows in a real demo.")
}

func usage() {
	fmt.Println("usage: discover-mcp [-- COMMAND ARGS...]")
	fmt.Println()
	fmt.Println("Connects to an MCP stdio server and prints its tool surface.")
	fmt.Println()
	fmt.Println("Default command: claude mcp serve")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  discover-mcp")
	fmt.Println("  discover-mcp -- npx @modelcontextprotocol/server-filesystem /tmp")
	fmt.Println("  discover-mcp -- /path/to/your/mcp-server --some-flag")
}

// truncateMultiline shows long descriptions concisely. MCP descriptions
// are sometimes paragraphs; we just want enough to identify the tool.
func truncateMultiline(s string, n int) string {
	// Collapse newlines to spaces so the description is one line.
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// printStderr drains the subprocess's stderr if available and shows it
// to the user. Servers often log useful diagnostic info there.
func printStderr(c *client.Client) {
	stderr, ok := client.GetStderr(c)
	if !ok {
		return
	}
	buf := make([]byte, 4096)
	n, _ := stderr.Read(buf)
	if n > 0 {
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Subprocess stderr:")
		fmt.Fprintln(os.Stderr, strings.Repeat("─", 70))
		fmt.Fprint(os.Stderr, string(buf[:n]))
	}
}
