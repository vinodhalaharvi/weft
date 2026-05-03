package mcp

// Stdio transport: lets weft's mcp.Connect speak to any MCP server running
// as a stdio subprocess (e.g., `claude mcp serve`).
//
// Implementation strategy: rather than implement MCP's wire protocol
// ourselves (framing, multiplexing, handshake, lifecycle), we wrap
// mark3labs/mcp-go which already does all that correctly. Our job is
// translation: convert weft's lightweight Request/Response envelope to
// mcp-go's typed method calls, then translate the response back into
// weft's envelope shape so the existing mcp.Connect / mcp.Tool code
// works unchanged.
//
// What this means for users:
//
//	client, _ := mcp.Connect(ctx, mcp.Stdio("claude", "mcp", "serve"))
//	defer client.Close()
//	bash := mcp.Tool[map[string]any, string](client, "Bash")
//	out, _ := bash(ctx, map[string]any{"command": "git status"})
//
// One transport function, works against any MCP server speaking stdio.
//
// Two design notes worth understanding:
//
// 1. Output translation. Real MCP servers return tool results as a list
//    of typed content blocks (TextContent, ImageContent, ...). Our
//    mcp.Tool[In, Out] expects to json.Unmarshal a single value into Out.
//    The stdio transport concatenates text from text-content blocks and
//    always returns it as a JSON string. Callers use Tool[..., string]
//    to receive the raw text, then json.Unmarshal it themselves if the
//    text happens to be JSON they want to decode into a struct.
//
//    This is deliberately simple: a previous version tried to detect
//    JSON output and pass it through directly, but that forced callers
//    to know in advance whether a tool's output was valid JSON and to
//    pick a matching Out type. The leaky heuristic produced confusing
//    decode errors. Treating output as opaque text is honest and lets
//    callers decide the parsing strategy.
//
// 2. Initialize is performed automatically inside Stdio() when the
//    transport opens. If your MCP server needs custom client capabilities
//    or a specific protocol version, use StdioWithOptions.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpgoclient "github.com/mark3labs/mcp-go/client"
)

// StdioOption configures a Stdio transport before initialization.
type StdioOption func(*stdioConfig)

type stdioConfig struct {
	env             []string
	clientName      string
	clientVersion   string
	protocolVersion string
}

// WithStdioEnv sets the environment variables passed to the subprocess.
// Default is to inherit nothing (mcp-go's default).
func WithStdioEnv(env []string) StdioOption {
	return func(c *stdioConfig) { c.env = env }
}

// WithClientInfo overrides the client identification sent during the
// MCP handshake. Most servers don't care; some log it.
func WithClientInfo(name, version string) StdioOption {
	return func(c *stdioConfig) {
		c.clientName = name
		c.clientVersion = version
	}
}

// WithProtocolVersion pins a specific MCP protocol version for the
// handshake. Default uses mcp-go's LATEST_PROTOCOL_VERSION constant,
// which the library keeps in sync with current spec.
func WithProtocolVersion(v string) StdioOption {
	return func(c *stdioConfig) { c.protocolVersion = v }
}

// Stdio constructs a Transport that spawns the given command as an MCP
// stdio server, performs the MCP initialize handshake, and routes
// requests through it.
//
// The subprocess is spawned eagerly (during Stdio itself), so a missing
// command or failed handshake surfaces immediately rather than at the
// first tool call. Lifecycle is tied to Close: the subprocess receives
// EOF on stdin, then a brief grace period to exit cleanly.
//
// Typical use:
//
//	transport, err := mcp.Stdio("claude", "mcp", "serve")
//	if err != nil { return err }
//	client, err := mcp.Connect(ctx, transport)
//	defer client.Close()
func Stdio(command string, args ...string) (Transport, error) {
	return StdioWithOptions(command, args, nil)
}

// StdioWithOptions is Stdio with extra configuration knobs.
func StdioWithOptions(command string, args []string, opts []StdioOption) (Transport, error) {
	cfg := stdioConfig{
		clientName:      "weft",
		clientVersion:   "0.1.0",
		protocolVersion: mcpgo.LATEST_PROTOCOL_VERSION,
	}
	for _, o := range opts {
		o(&cfg)
	}

	// Spawn the subprocess and set up stdio pipes. mcp-go handles
	// framing, multiplexing, and lifecycle.
	c, err := mcpgoclient.NewStdioMCPClient(command, cfg.env, args...)
	if err != nil {
		return nil, fmt.Errorf("mcp.Stdio: spawn %s: %w", command, err)
	}

	// Run the MCP handshake. We do this inside Stdio (not in Connect)
	// because mcp-go's high-level methods require initialization first
	// and we want failures to surface here, where the user just typed
	// the command name and can fix it easily.
	ctx := context.Background()
	initReq := mcpgo.InitializeRequest{}
	initReq.Params.ProtocolVersion = cfg.protocolVersion
	initReq.Params.ClientInfo = mcpgo.Implementation{
		Name:    cfg.clientName,
		Version: cfg.clientVersion,
	}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("mcp.Stdio: initialize: %w", err)
	}

	return &stdioTransport{client: c}, nil
}

// stdioTransport satisfies the weft Transport interface by translating
// envelope-shaped Requests into mcp-go's typed calls.
type stdioTransport struct {
	client *mcpgoclient.Client
	closed bool
}

func (t *stdioTransport) Roundtrip(ctx context.Context, req Request) (Response, error) {
	if t.closed {
		return Response{}, fmt.Errorf("mcp: transport closed")
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}

	resp := Response{ID: req.ID}

	switch req.Method {
	case MethodToolsList:
		// Translate: call mcp-go's ListTools, repackage as our
		// envelope shape so the existing mcp.Connect decoder works.
		listReq := mcpgo.ListToolsRequest{}
		out, err := t.client.ListTools(ctx, listReq)
		if err != nil {
			return Response{}, err
		}

		// Build our ListToolsResult shape from mcp-go's typed result.
		ours := ListToolsResult{
			Tools: make([]ToolInfo, 0, len(out.Tools)),
		}
		for _, tool := range out.Tools {
			schemaJSON, _ := json.Marshal(tool.InputSchema)
			ours.Tools = append(ours.Tools, ToolInfo{
				Name:        tool.Name,
				Description: tool.Description,
				InputSchema: schemaJSON,
			})
		}
		raw, err := json.Marshal(ours)
		if err != nil {
			return Response{}, fmt.Errorf("encode ListToolsResult: %w", err)
		}
		resp.Result = raw
		return resp, nil

	case MethodToolsCall:
		// Decode our envelope params into mcp-go's request shape.
		var ourParams CallToolParams
		if err := json.Unmarshal(req.Params, &ourParams); err != nil {
			return Response{}, fmt.Errorf("decode CallToolParams: %w", err)
		}
		var args map[string]any
		if len(ourParams.Arguments) > 0 {
			if err := json.Unmarshal(ourParams.Arguments, &args); err != nil {
				return Response{}, fmt.Errorf("decode tool arguments: %w", err)
			}
		}

		callReq := mcpgo.CallToolRequest{}
		callReq.Params.Name = ourParams.Name
		callReq.Params.Arguments = args

		out, err := t.client.CallTool(ctx, callReq)
		if err != nil {
			return Response{}, err
		}

		// Real MCP servers return Content as a list of typed content
		// blocks. Extract text from the first text block (the common
		// case for stdio-based tools). If the tool reported an error
		// via IsError, propagate that.
		text := extractTextContent(out)

		// Always encode the text as a JSON string. Callers receive
		// the raw text and can json.Unmarshal it themselves if they
		// want structured data. The previous "pass JSON through
		// unchanged" heuristic was leaky — it forced callers to know
		// in advance whether a tool's output was valid JSON, and
		// picking the wrong Out type produced confusing decode errors.
		// Treating tool output as opaque text uniformly is simpler
		// and lets the caller decide the parsing strategy.
		contentRaw, err := json.Marshal(text)
		if err != nil {
			return Response{}, fmt.Errorf("encode tool output text: %w", err)
		}

		ourResult := CallToolResult{
			Content: contentRaw,
			IsError: out.IsError,
		}
		raw, err := json.Marshal(ourResult)
		if err != nil {
			return Response{}, fmt.Errorf("encode CallToolResult: %w", err)
		}
		resp.Result = raw
		return resp, nil

	default:
		// Methods we don't translate get reported as method-not-found,
		// matching the in-memory transport's behavior.
		resp.Error = &RPCError{
			Code:    ErrCodeMethodNotFound,
			Message: "method not implemented in stdio transport: " + req.Method,
		}
		return resp, nil
	}
}

func (t *stdioTransport) Close() error {
	if t.closed {
		return nil
	}
	t.closed = true
	return t.client.Close()
}

// extractTextContent pulls text from an mcp-go CallToolResult.
//
// Real MCP servers return Content as a list of polymorphic blocks
// (TextContent, ImageContent, AudioContent, etc.). Most tool results
// have exactly one text block; some have several (e.g., a verbose tool
// returning multiple text blocks for different sections). We
// concatenate text blocks with newlines and ignore non-text blocks.
//
// Non-text blocks are intentionally dropped: this transport's job is
// to make string/JSON tool outputs ergonomic. If you need image or
// audio output, you'd reach for a different Tool variant — but those
// aren't implemented yet, and most stdio tools return text.
func extractTextContent(r *mcpgo.CallToolResult) string {
	if r == nil {
		return ""
	}
	var parts []string
	for _, c := range r.Content {
		if tc, ok := c.(mcpgo.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}
