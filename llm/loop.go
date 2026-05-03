package llm

// The agent loop combinator.
//
// llm.Loop wraps an Arrow[Prompt, Response] (any LLM-shaped arrow —
// Claude, OpenAI, Ollama, future providers, or any composed arrow that
// looks like one) into a NEW Arrow[Prompt, Response] that:
//
//  1. Calls the underlying LLM with the prompt
//  2. If the response contains tool_use blocks, dispatches each one
//     through the provided ToolBindings
//  3. Appends the assistant message and the tool_result message to
//     the conversation, then calls the LLM again
//  4. Stops when the LLM returns a response with no tool_use blocks,
//     or when MaxIter is hit
//
// The loop is itself just an arrow — the rest of weft can compose,
// retry, timeout, parallelize, or traverse it like anything else. The
// "agent" abstraction collapses to "an arrow whose behavior happens
// to involve a feedback loop." That collapse is the design point.
//
// Usage with real Claude Code MCP tools:
//
//	mcpClient, _ := mcp.Connect(ctx, mcp.Stdio("claude", "mcp", "serve"))
//	bash := mcp.Tool[map[string]any, string](mcpClient, "Bash")
//
//	bashBinding := llm.ToolBinding{
//	    Spec: llm.ToolSpec{Name: "Bash", ...},
//	    Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
//	        var m map[string]any
//	        json.Unmarshal(args, &m)
//	        return bash(ctx, m)
//	    },
//	}
//
//	agent := llm.Loop(
//	    llm.Claude("claude-sonnet-4-5-20250929"),
//	    []llm.ToolBinding{bashBinding},
//	    llm.WithMaxIter(8),
//	)
//
//	// agent is now Arrow[Prompt, Response] — use it however.
//	resp, err := agent(ctx, llm.Prompt{
//	    Messages: []llm.Message{llm.UserText("count Go files in this repo")},
//	})

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/vinodhalaharvi/weft/weft"
)

// ToolBinding pairs a tool's spec (advertised to the LLM via the
// outgoing prompt) with a handler that executes it. The handler
// receives the raw JSON arguments the LLM produced and returns a
// string result that gets fed back as a tool_result block.
//
// String return type is intentional: tool results in MCP and in
// Claude's API are both text-shaped on the wire. If your tool produces
// structured output, marshal it to a string before returning. Callers
// who want richer shapes can json.Marshal a struct.
type ToolBinding struct {
	Spec    ToolSpec
	Handler func(ctx context.Context, args json.RawMessage) (string, error)
}

// LoopOption configures the agent loop's behavior.
type LoopOption func(*loopConfig)

type loopConfig struct {
	maxIter   int
	onIter    func(iter int, resp Response)
	onToolErr func(name string, err error) string
}

// WithMaxIter caps the number of LLM calls the loop will make. Default
// is 16. Hitting the cap returns the most recent Response — partial
// work, but a defined exit. The caller can detect this by checking
// StopReason against StopToolUse on the returned Response.
func WithMaxIter(n int) LoopOption {
	return func(c *loopConfig) { c.maxIter = n }
}

// WithIterCallback runs after each LLM call, receiving the iteration
// number (0-indexed) and the Response from that call. Useful for
// streaming progress, logs, or accumulating metrics outside the loop.
func WithIterCallback(f func(iter int, resp Response)) LoopOption {
	return func(c *loopConfig) { c.onIter = f }
}

// WithToolErrorHandler customizes what gets fed back to the LLM when
// a tool handler returns an error. Default behavior wraps the error
// as a tool_result so the LLM can self-correct (it almost always
// gracefully reports the failure to the user). Override if you want
// to abort the loop on tool errors instead — return "" from the
// handler and the loop will surface the error to the caller.
func WithToolErrorHandler(f func(name string, err error) string) LoopOption {
	return func(c *loopConfig) { c.onToolErr = f }
}

// Loop wraps an LLM arrow with an agent loop. The bindings define the
// tools the LLM may call; their Spec fields are appended to
// Prompt.Tools so the LLM knows what's available. Tools the caller
// already specified in Prompt.Tools are preserved and stacked
// alongside the bound ones.
//
// The returned arrow has the same Arrow[Prompt, Response] shape as
// the wrapped llm — composable with everything else in weft.
func Loop(
	llm weft.Arrow[Prompt, Response],
	bindings []ToolBinding,
	opts ...LoopOption,
) weft.Arrow[Prompt, Response] {
	cfg := loopConfig{
		maxIter:   16,
		onToolErr: defaultToolErrorHandler,
	}
	for _, o := range opts {
		o(&cfg)
	}

	// Index bindings by tool name for O(1) dispatch.
	byName := make(map[string]ToolBinding, len(bindings))
	for _, b := range bindings {
		if _, dup := byName[b.Spec.Name]; dup {
			panic(fmt.Sprintf("llm.Loop: duplicate tool name %q", b.Spec.Name))
		}
		byName[b.Spec.Name] = b
	}

	// Pre-build the slice of tool specs to inject into each prompt.
	specs := make([]ToolSpec, 0, len(bindings))
	for _, b := range bindings {
		specs = append(specs, b.Spec)
	}

	return func(ctx context.Context, p Prompt) (Response, error) {
		// Augment the prompt with the bound tools, once, before the
		// loop. We reuse the augmented prompt across iterations,
		// appending messages as the conversation evolves. Tools the
		// caller already specified are preserved alongside ours.
		augmented := p
		augmented.Tools = append(append([]ToolSpec(nil), p.Tools...), specs...)

		// Accumulate usage across all iterations so the final
		// Response reflects the full cost of the loop, not just the
		// last call. The caller's usage tracking sees one number.
		var totalUsage Usage
		var last Response

		for iter := 0; iter < cfg.maxIter; iter++ {
			resp, err := llm(ctx, augmented)
			if err != nil {
				return Response{}, fmt.Errorf("llm.Loop iter %d: %w", iter, err)
			}
			last = resp

			if cfg.onIter != nil {
				cfg.onIter(iter, resp)
			}

			totalUsage = totalUsage.Add(resp.Usage)

			toolCalls := resp.ToolCalls()
			if len(toolCalls) == 0 {
				// Final answer — model didn't request more tools.
				// Replace per-call usage with the accumulated total.
				resp.Usage = totalUsage
				return resp, nil
			}

			// Append the assistant message containing the tool calls,
			// and a user message containing the tool results.
			// Providers (Claude, OpenAI) require both for the next
			// turn to have valid context.
			augmented.Messages = append(augmented.Messages, resp.Messages...)

			toolResults := make([]Block, 0, len(toolCalls))
			for _, call := range toolCalls {
				result, err := dispatchTool(ctx, byName, call, cfg.onToolErr)
				if err != nil {
					return Response{}, fmt.Errorf(
						"llm.Loop iter %d tool %q: %w",
						iter, call.ToolName, err)
				}
				toolResults = append(toolResults, Block{
					Kind:         BlockToolResult,
					ToolResultID: call.ToolUseID,
					ToolResult:   result,
				})
			}
			augmented.Messages = append(augmented.Messages, Message{
				Role:    RoleUser,
				Content: toolResults,
			})
		}

		// Hit MaxIter without convergence. Return last response with
		// accumulated usage. StopReason stays StopToolUse so the
		// caller can detect the cap was hit.
		last.Usage = totalUsage
		return last, nil
	}
}

// dispatchTool runs one tool call. Hallucinated tool names (LLM
// called something we didn't bind) are fed back as a recoverable
// tool_result so the LLM can adapt. Genuine handler errors go through
// the configured error handler.
func dispatchTool(
	ctx context.Context,
	byName map[string]ToolBinding,
	call Block,
	onErr func(name string, err error) string,
) (string, error) {
	binding, ok := byName[call.ToolName]
	if !ok {
		// LLM hallucinated a tool name. Feed available names back so
		// it can self-correct rather than treating this as a hard
		// error.
		available := make([]string, 0, len(byName))
		for name := range byName {
			available = append(available, name)
		}
		return fmt.Sprintf(
			"error: tool %q not available. tools you may call: %v",
			call.ToolName, available,
		), nil
	}

	result, err := binding.Handler(ctx, call.ToolInput)
	if err != nil {
		fed := onErr(call.ToolName, err)
		if fed == "" {
			// Empty return means "abort the loop with this error"
			return "", err
		}
		return fed, nil
	}
	return result, nil
}

// defaultToolErrorHandler turns a tool error into a string the LLM
// can read and respond to. The LLM almost always handles this
// gracefully — if it asked to read /tmp/missing.txt and got "no such
// file" back, it explains the problem to the user rather than
// looping forever.
func defaultToolErrorHandler(name string, err error) string {
	return fmt.Sprintf("error from %s: %s", name, err.Error())
}
