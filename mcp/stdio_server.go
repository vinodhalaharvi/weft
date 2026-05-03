package mcp

// Stdio server transport: runs a weft *Server over stdio so external
// MCP clients (Claude Desktop, other agents, the test-multi-source
// command in this repo, etc.) can talk to it.
//
// This is the inverse of mcp.Stdio() — that consumes a remote MCP
// stdio server; RunStdioServer exposes a weft *Server as one.
//
// Implementation strategy: same as the client-side stdio transport,
// we wrap mark3labs/mcp-go rather than implement the wire protocol.
// We translate each ErasedTool registered in our weft *Server into
// an mcp-go tool with a thin handler that bridges between the two
// shapes (typed mcp-go calls ↔ our raw-JSON ErasedTool.Handler).
//
// Together with mcp.ServeAsTool, this completes the lift-out side
// of the framework's symmetry:
//
//	server := mcp.Serve(
//	    mcp.ServeAsTool("greet", greetArrow),
//	    mcp.ServeAsTool("classify", classifyArrow),
//	)
//	if err := mcp.RunStdioServer(server); err != nil {
//	    log.Fatal(err)
//	}
//
// External MCP clients can now connect via stdio and call the tools
// as if they came from any other MCP server.

import (
	"context"
	"encoding/json"
	"fmt"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpgoserver "github.com/mark3labs/mcp-go/server"
)

// StdioServerOption configures the mcp-go server before it starts.
type StdioServerOption func(*stdioServerConfig)

type stdioServerConfig struct {
	name    string
	version string
}

// WithServerInfo sets the server name and version published in the
// MCP initialize handshake.
func WithServerInfo(name, version string) StdioServerOption {
	return func(c *stdioServerConfig) {
		c.name = name
		c.version = version
	}
}

// RunStdioServer runs a weft *Server over stdio. This call blocks
// until stdin closes (the client disconnects). Returns any error from
// the underlying transport.
//
// Each ErasedTool in the server gets registered as an mcp-go tool.
// The input schema travels through unchanged (we already store it as
// raw JSON in ToolInfo.InputSchema). The handler shim decodes
// mcp-go's typed CallToolRequest into the raw-JSON arguments our
// ErasedTool.Handler expects, calls it, then wraps the result in
// mcp-go's CallToolResult shape.
//
// Tools that return JSON-shaped output get returned as text content
// — that's the universal MCP output format. Clients that want
// structured data can json.Unmarshal the text themselves, mirroring
// the convention on the client side.
func RunStdioServer(server *Server, opts ...StdioServerOption) error {
	cfg := stdioServerConfig{
		name:    "weft-server",
		version: "0.1.0",
	}
	for _, o := range opts {
		o(&cfg)
	}

	mcpServer := mcpgoserver.NewMCPServer(
		cfg.name, cfg.version,
		mcpgoserver.WithToolCapabilities(false),
	)

	// Register every weft tool as an mcp-go tool.
	for _, et := range server.tools {
		// Capture loop variable for the closure.
		entry := et

		// Build the mcp-go tool. We use NewToolWithRawSchema because
		// our ErasedTool already carries the input schema as raw
		// JSON. (mcp-go warns that NewTool + post-hoc RawInputSchema
		// assignment is unsupported; the right factory is the one
		// designed for this case.)
		schema := entry.Info.InputSchema
		if len(schema) == 0 {
			// Fall back to a permissive object schema so mcp-go has
			// something valid to publish.
			schema = []byte(`{"type":"object"}`)
		}
		tool := mcpgo.NewToolWithRawSchema(
			entry.Info.Name,
			entry.Info.Description,
			schema,
		)

		handler := buildHandler(entry)
		mcpServer.AddTool(tool, handler)
	}

	return mcpgoserver.ServeStdio(mcpServer)
}

// buildHandler creates an mcp-go tool handler that bridges to one
// ErasedTool. Pulled out so the closure has a focused scope.
func buildHandler(entry ErasedTool) mcpgoserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		// mcp-go gives us arguments as map[string]any; we need them
		// as raw JSON for ErasedTool.Handler.
		args := req.GetArguments()
		var rawArgs json.RawMessage
		if len(args) > 0 {
			b, err := json.Marshal(args)
			if err != nil {
				return mcpgo.NewToolResultError(
					fmt.Sprintf("re-encode arguments: %v", err)), nil
			}
			rawArgs = b
		}

		// Call the wrapped weft arrow.
		out, err := entry.Handler(ctx, rawArgs)
		if err != nil {
			// Application-level errors get reported as tool errors,
			// not protocol errors, per MCP spec.
			return mcpgo.NewToolResultError(err.Error()), nil
		}

		// out is a json.RawMessage from ServeAsTool — JSON-encoded
		// representation of whatever the arrow returned. The wire
		// expects text content, so we need to render it as a string.
		//
		// If the arrow's output type was Go's `string`, ServeAsTool
		// wrapped it as a JSON string like `"hello"`. We need to
		// unwrap that so the text content is `hello`, not `"hello"`.
		// Otherwise downstream MCP clients see escaped quotes around
		// every text result.
		//
		// For structured outputs (struct, map, slice) the JSON form
		// IS the text we want to send — pass it through verbatim.
		var asString string
		if err := json.Unmarshal(out, &asString); err == nil {
			// Successfully decoded as a JSON string → pass the
			// unwrapped text.
			return mcpgo.NewToolResultText(asString), nil
		}
		// Not a JSON-encoded string → pass the raw JSON as text.
		return mcpgo.NewToolResultText(string(out)), nil
	}
}
