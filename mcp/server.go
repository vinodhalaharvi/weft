package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/vinodhalaharvi/weft/weft"
)

// === ErasedTool — the boundary where types necessarily flatten ==============

// ErasedTool is the dispatch-table shape: name + schema + a closure that
// takes raw JSON in and returns raw JSON out.
//
// This is the controlled erasure point: outside this struct, every
// arrow in the framework keeps its full types; inside, JSON crosses
// the wire and types are recovered by Unmarshal. The erasure is
// projection only — the original Arrow[In, Out] is unaffected and
// remains usable elsewhere in the program with full type safety.
//
// Users do not construct ErasedTool directly. They produce one via
// ServeAsTool[In, Out](name, arrow), which captures In and Out at the
// type-level and produces the closure that bridges to JSON.
type ErasedTool struct {
	Info    ToolInfo
	Handler func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error)
}

// === ServeAsTool — lift a typed Arrow into an MCP tool entry ================

// ServeOption configures a tool entry as it's being constructed.
type ServeOption func(*ToolInfo)

// WithDescription sets the human-readable description of the tool.
// MCP clients use this to help LLMs decide whether to call the tool.
func WithDescription(desc string) ServeOption {
	return func(info *ToolInfo) { info.Description = desc }
}

// WithInputSchema sets the JSON Schema describing the tool's input.
// If unset, ServeAsTool generates a permissive default. Real production
// servers should supply a precise schema so LLMs can call the tool
// correctly without trial and error.
func WithInputSchema(schema json.RawMessage) ServeOption {
	return func(info *ToolInfo) { info.InputSchema = schema }
}

// ServeAsTool wraps a typed Arrow as an MCP tool entry. The resulting
// ErasedTool can be passed to Serve along with other entries.
//
// The shape is deliberately a function returning a struct, not a method
// on Arrow, because we want callers to see the conversion explicitly:
//
//	mcp.ServeAsTool[Input, Output]("tool_name", myArrow)
//
// The original arrow is unmodified; ServeAsTool produces a NEW value
// (the ErasedTool) that carries a closure over the original. You can
// keep using `myArrow` directly elsewhere with full types.
func ServeAsTool[In, Out any](
	name string,
	arrow weft.Arrow[In, Out],
	opts ...ServeOption,
) ErasedTool {
	info := ToolInfo{
		Name:        name,
		InputSchema: json.RawMessage(`{"type":"object"}`), // permissive default
	}
	for _, o := range opts {
		o(&info)
	}

	handler := func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
		var in In
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, fmt.Errorf("unmarshal %s input: %w", name, err)
			}
		}
		out, err := arrow(ctx, in)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	}

	return ErasedTool{Info: info, Handler: handler}
}

// === Server — bundle of tools, dispatched via Transport =====================

// Server holds a registry of erased tools and routes incoming requests
// to them. Servers don't speak any wire protocol themselves; they're
// dispatched by a Transport.
type Server struct {
	tools map[string]ErasedTool
}

// Serve constructs a Server from a list of erased tools.
//
// Duplicate tool names are a programming error and cause a panic at
// construction time. This is intentional: a duplicate name means the
// caller has a logic bug, and silently overwriting (or returning an
// error from a constructor) makes that bug harder to diagnose.
func Serve(entries ...ErasedTool) *Server {
	tools := make(map[string]ErasedTool, len(entries))
	for _, e := range entries {
		if _, dup := tools[e.Info.Name]; dup {
			panic(fmt.Sprintf("mcp.Serve: duplicate tool name %q", e.Info.Name))
		}
		tools[e.Info.Name] = e
	}
	return &Server{tools: tools}
}

// dispatch handles one MCP request and returns the response. Called by
// transports; not exported. Errors at the application level (tool not
// found, handler failure) come back as Response.Error so the wire shape
// is uniform.
func (s *Server) dispatch(ctx context.Context, req Request) Response {
	resp := Response{ID: req.ID}

	switch req.Method {
	case MethodToolsList:
		out := ListToolsResult{Tools: make([]ToolInfo, 0, len(s.tools))}
		for _, t := range s.tools {
			out.Tools = append(out.Tools, t.Info)
		}
		raw, err := json.Marshal(out)
		if err != nil {
			resp.Error = &RPCError{Code: ErrCodeInternal, Message: err.Error()}
			return resp
		}
		resp.Result = raw
		return resp

	case MethodToolsCall:
		var params CallToolParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			resp.Error = &RPCError{Code: ErrCodeInvalidParams, Message: err.Error()}
			return resp
		}
		tool, ok := s.tools[params.Name]
		if !ok {
			resp.Error = &RPCError{
				Code:    ErrCodeToolNotFound,
				Message: fmt.Sprintf("tool not found: %s", params.Name),
			}
			return resp
		}
		out, err := tool.Handler(ctx, params.Arguments)
		if err != nil {
			resp.Error = &RPCError{Code: ErrCodeInternal, Message: err.Error()}
			return resp
		}
		result := CallToolResult{Content: out}
		raw, err := json.Marshal(result)
		if err != nil {
			resp.Error = &RPCError{Code: ErrCodeInternal, Message: err.Error()}
			return resp
		}
		resp.Result = raw
		return resp

	default:
		resp.Error = &RPCError{
			Code:    ErrCodeMethodNotFound,
			Message: "method not implemented: " + req.Method,
		}
		return resp
	}
}
