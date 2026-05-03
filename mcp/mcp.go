// Package mcp provides categorical wrappers around the Model Context
// Protocol (MCP). It does two things:
//
//  1. Lifts MCP server tools into typed weft.Arrows (the "lift in"
//     direction), so a remote tool call composes with everything else
//     in the framework as a normal Arrow[In, Out].
//
//  2. Exposes any weft.Arrow back as an MCP tool (the "lift out"
//     direction), so a composition can be served as an MCP entity that
//     other clients (Claude Desktop, agents, etc.) can call.
//
// These two operations are inverse functors. A tool exposed via
// ServeAsTool and re-imported via Tool round-trips behaviorally
// (modulo JSON marshaling); see mcp_test.go for the round-trip
// property tests.
//
// The package is deliberately transport-agnostic. The Transport
// interface abstracts over how MCP messages move (stdio, HTTP+SSE,
// in-process). An InMemory transport is provided for tests and
// in-process examples; production code typically wires this to a
// real MCP library like mark3labs/mcp-go.
//
// What's NOT here yet:
//   - Real stdio transport (subprocess management)
//   - HTTP+SSE transport
//   - MCP resources and prompts (just tools for now)
//
// Each of those can be added without changing the Tool / ServeAsTool /
// Serve / Connect surface — they only need new Transport implementations
// and (for resources/prompts) parallel constructors with the same shape.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// === Wire types ==============================================================

// Request is the MCP/JSON-RPC request envelope. We model it minimally:
// only the fields this package's wire protocol actually uses.
type Request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is the JSON-RPC response envelope.
type Response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("mcp rpc error %d: %s", e.Code, e.Message)
}

// Standard JSON-RPC error codes used by this package. We use a small
// subset; full MCP spec defines more.
const (
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternal       = -32603
	ErrCodeToolNotFound   = -32000 // application error: tool name unknown
)

// === Tool descriptions =======================================================

// ToolInfo is what a server publishes about each tool it exposes.
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// === MCP method names ========================================================
//
// We use a tiny subset of the MCP spec: list tools, call a tool. These
// are sufficient for the lift-in / lift-out functor. More methods
// (resources, prompts, sampling) can be added here without disturbing
// the Tool / ServeAsTool surface.

const (
	MethodToolsList = "tools/list"
	MethodToolsCall = "tools/call"
)

// ListToolsResult is the response shape for tools/list.
type ListToolsResult struct {
	Tools []ToolInfo `json:"tools"`
}

// CallToolParams is the params shape for tools/call.
type CallToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// CallToolResult is the response shape for tools/call. We model only
// the fields this package needs; real MCP responses also carry content
// blocks and structured output.
type CallToolResult struct {
	Content json.RawMessage `json:"content"`
	IsError bool            `json:"isError,omitempty"`
}

// === Transport ===============================================================

// Transport carries MCP requests and responses between client and server.
//
// The interface is small on purpose: any implementation that can do
// request/response with cancellation can serve as a transport. The
// InMemory transport (in this package) does in-process delivery for
// tests; future stdio/HTTP transports plug in here without touching the
// rest of the package.
type Transport interface {
	// Roundtrip sends a request and returns the response. It must honor
	// ctx cancellation and propagate any transport-level error as a
	// non-nil error. Application errors come back as Response.Error and
	// are not transport errors.
	Roundtrip(ctx context.Context, req Request) (Response, error)

	// Close releases any resources held by the transport. Must be safe
	// to call multiple times.
	Close() error
}

// === In-memory transport =====================================================

// InMemory returns a Transport that delivers messages in-process to the
// given Server. Useful for tests and for round-trip examples that don't
// need real IPC.
//
// All requests are dispatched synchronously through the server's
// dispatch method. There's no goroutine, no channels — just a function
// call wrapped to satisfy the Transport interface.
func InMemory(server *Server) Transport {
	return &inMemoryTransport{server: server}
}

type inMemoryTransport struct {
	server *Server
	closed bool
}

func (t *inMemoryTransport) Roundtrip(ctx context.Context, req Request) (Response, error) {
	if t.closed {
		return Response{}, fmt.Errorf("mcp: transport closed")
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	return t.server.dispatch(ctx, req), nil
}

func (t *inMemoryTransport) Close() error {
	t.closed = true
	return nil
}
