package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/vinodhalaharvi/weft/weft"
)

// Client is a connection to an MCP server via some Transport. It caches
// the server's tool list (fetched on Connect) so Tool[In, Out] can
// validate that the requested tool exists before producing an arrow.
type Client struct {
	transport Transport
	tools     map[string]ToolInfo
}

// Connect opens a client over the given transport and fetches the
// server's tool list. The resulting Client holds the cached list and
// produces typed arrows via Tool.
//
// The fetch happens here rather than lazily on first Tool call because
// schema validation at lift-time (when you write `mcp.Tool[In, Out](...)`)
// gives much better error messages than at first call (when the agent
// might be in the middle of a long pipeline).
func Connect(ctx context.Context, transport Transport) (*Client, error) {
	c := &Client{
		transport: transport,
		tools:     make(map[string]ToolInfo),
	}

	resp, err := c.transport.Roundtrip(ctx, Request{
		ID:     newID(),
		Method: MethodToolsList,
	})
	if err != nil {
		return nil, fmt.Errorf("mcp connect: list tools: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("mcp connect: %w", resp.Error)
	}

	var list ListToolsResult
	if err := json.Unmarshal(resp.Result, &list); err != nil {
		return nil, fmt.Errorf("mcp connect: decode tool list: %w", err)
	}
	for _, t := range list.Tools {
		c.tools[t.Name] = t
	}
	return c, nil
}

// Close releases the underlying transport.
func (c *Client) Close() error {
	return c.transport.Close()
}

// Tools returns the names of all tools published by the server. Useful
// for diagnostics and for MCP-aware agents that want to enumerate
// available capabilities.
func (c *Client) Tools() []string {
	out := make([]string, 0, len(c.tools))
	for name := range c.tools {
		out = append(out, name)
	}
	return out
}

// Tool lifts a remote MCP tool into a typed weft.Arrow.
//
// The In and Out type parameters describe the Go types the caller wants
// to use; they're marshaled to JSON for transport and unmarshaled back
// on response. If the server's tool doesn't actually accept In or
// produce Out, the failure surfaces at call time as a marshal error.
//
// Tool returns a normal Arrow[In, Out]. It composes with weft.Compose,
// weft.Par, weft.Sum, weft.Traverse — every combinator in the framework —
// because it IS a normal arrow. The fact that it's backed by a remote
// MCP call is purely an implementation detail of the closure.
//
// If the tool name doesn't exist on the connected server, Tool still
// returns an arrow, but calling it will fail with ErrCodeToolNotFound.
// We could panic at lift time instead; the choice is to fail at use,
// because some workflows construct arrow references before the server
// has had a chance to register tools.
func Tool[In, Out any](c *Client, name string) weft.Arrow[In, Out] {
	return func(ctx context.Context, in In) (Out, error) {
		var zero Out

		args, err := json.Marshal(in)
		if err != nil {
			return zero, &weft.ArrowError{
				Class: weft.ClassPermanent,
				Op:    "mcp.Tool[" + name + "]",
				Cause: fmt.Errorf("marshal input: %w", err),
			}
		}

		params, err := json.Marshal(CallToolParams{
			Name:      name,
			Arguments: args,
		})
		if err != nil {
			return zero, &weft.ArrowError{
				Class: weft.ClassPermanent,
				Op:    "mcp.Tool[" + name + "]",
				Cause: fmt.Errorf("marshal params: %w", err),
			}
		}

		resp, err := c.transport.Roundtrip(ctx, Request{
			ID:     newID(),
			Method: MethodToolsCall,
			Params: params,
		})
		if err != nil {
			class := weft.ClassTransient
			if ctx.Err() != nil {
				class = weft.ClassUserCancelled
			}
			return zero, &weft.ArrowError{
				Class: class,
				Op:    "mcp.Tool[" + name + "]",
				Cause: err,
			}
		}
		if resp.Error != nil {
			// Application-level errors are usually permanent (bad
			// args, tool not found, handler bug). The exception is
			// internal errors which might be transient.
			class := weft.ClassPermanent
			if resp.Error.Code == ErrCodeInternal {
				class = weft.ClassTransient
			}
			return zero, &weft.ArrowError{
				Class:    class,
				Op:       "mcp.Tool[" + name + "]",
				Cause:    resp.Error,
				Metadata: map[string]any{"rpc_code": resp.Error.Code},
			}
		}

		// Server returns CallToolResult; we unwrap its Content into Out.
		var result CallToolResult
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			return zero, &weft.ArrowError{
				Class: weft.ClassPermanent,
				Op:    "mcp.Tool[" + name + "]",
				Cause: fmt.Errorf("decode result envelope: %w", err),
			}
		}
		if result.IsError {
			return zero, &weft.ArrowError{
				Class: weft.ClassPermanent,
				Op:    "mcp.Tool[" + name + "]",
				Cause: fmt.Errorf("tool reported error: %s", string(result.Content)),
			}
		}

		var out Out
		if err := json.Unmarshal(result.Content, &out); err != nil {
			return zero, &weft.ArrowError{
				Class: weft.ClassPermanent,
				Op:    "mcp.Tool[" + name + "]",
				Cause: fmt.Errorf("decode output: %w", err),
			}
		}
		return out, nil
	}
}

// newID generates an opaque request ID. We don't need cryptographic
// strength; we need uniqueness within a single client session.
func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
