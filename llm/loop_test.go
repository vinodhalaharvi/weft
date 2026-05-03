package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vinodhalaharvi/weft/llm"
	"github.com/vinodhalaharvi/weft/weft"
)

// scriptedLLM returns a fixed sequence of Responses, one per call. It
// captures the prompts it received so tests can assert what the loop
// fed back. Used in place of a real LLM for deterministic tests.
//
// If the script is exhausted, the next call errors — that catches
// "loop ran more iterations than expected" without needing a timeout.
type scriptedLLM struct {
	script   []llm.Response
	calls    atomic.Int32
	captured []llm.Prompt
}

func (s *scriptedLLM) arrow() weft.Arrow[llm.Prompt, llm.Response] {
	return func(ctx context.Context, p llm.Prompt) (llm.Response, error) {
		i := int(s.calls.Add(1)) - 1
		if i >= len(s.script) {
			return llm.Response{}, errors.New("scripted LLM exhausted")
		}
		s.captured = append(s.captured, p)
		return s.script[i], nil
	}
}

// === Test 1: no tool calls — loop returns immediately =======================

func TestLoop_NoToolCalls(t *testing.T) {
	final := llm.Response{
		Messages:   []llm.Message{llm.AssistantText("Paris.")},
		StopReason: llm.StopEndTurn,
		Usage:      llm.Usage{InputTokens: 10, OutputTokens: 2},
	}
	mock := &scriptedLLM{script: []llm.Response{final}}

	agent := llm.Loop(mock.arrow(), nil)
	resp, err := agent(context.Background(), llm.Prompt{
		Messages: []llm.Message{llm.UserText("capital of France?")},
	})
	if err != nil {
		t.Fatalf("agent: %v", err)
	}
	if got := resp.Text(); got != "Paris." {
		t.Errorf("text: got %q, want Paris.", got)
	}
	if mock.calls.Load() != 1 {
		t.Errorf("calls: got %d, want 1", mock.calls.Load())
	}
}

// === Test 2: one tool call, then text =======================================

func TestLoop_OneToolCall(t *testing.T) {
	mock := &scriptedLLM{script: []llm.Response{
		{
			Messages: []llm.Message{{
				Role: llm.RoleAssistant,
				Content: []llm.Block{{
					Kind:      llm.BlockToolUse,
					ToolUseID: "call_1",
					ToolName:  "get_weather",
					ToolInput: json.RawMessage(`{"city":"Paris"}`),
				}},
			}},
			StopReason: llm.StopToolUse,
			Usage:      llm.Usage{InputTokens: 10, OutputTokens: 5},
		},
		{
			Messages:   []llm.Message{llm.AssistantText("It is sunny in Paris.")},
			StopReason: llm.StopEndTurn,
			Usage:      llm.Usage{InputTokens: 25, OutputTokens: 7},
		},
	}}

	weatherCalled := atomic.Int32{}
	weatherTool := llm.ToolBinding{
		Spec: llm.ToolSpec{
			Name:        "get_weather",
			Description: "Look up weather",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			weatherCalled.Add(1)
			var in struct {
				City string `json:"city"`
			}
			_ = json.Unmarshal(args, &in)
			return "sunny, " + in.City, nil
		},
	}

	agent := llm.Loop(mock.arrow(), []llm.ToolBinding{weatherTool})
	resp, err := agent(context.Background(), llm.Prompt{
		Messages: []llm.Message{llm.UserText("weather in Paris?")},
	})
	if err != nil {
		t.Fatalf("agent: %v", err)
	}

	if got := resp.Text(); got != "It is sunny in Paris." {
		t.Errorf("text: got %q", got)
	}
	if weatherCalled.Load() != 1 {
		t.Errorf("weather called %d times, want 1", weatherCalled.Load())
	}
	if mock.calls.Load() != 2 {
		t.Errorf("LLM called %d times, want 2", mock.calls.Load())
	}

	// Usage should be accumulated across both calls.
	wantTokens := 10 + 5 + 25 + 7
	gotTokens := resp.Usage.InputTokens + resp.Usage.OutputTokens
	if gotTokens != wantTokens {
		t.Errorf("usage: got %d total tokens, want %d", gotTokens, wantTokens)
	}
}

// === Test 3: tool spec is propagated into the prompt ========================

func TestLoop_AdvertisesToolSpecs(t *testing.T) {
	mock := &scriptedLLM{script: []llm.Response{
		{
			Messages:   []llm.Message{llm.AssistantText("ok")},
			StopReason: llm.StopEndTurn,
		},
	}}

	binding := llm.ToolBinding{
		Spec: llm.ToolSpec{
			Name:        "my_tool",
			Description: "does a thing",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "", nil
		},
	}

	agent := llm.Loop(mock.arrow(), []llm.ToolBinding{binding})
	_, err := agent(context.Background(), llm.Prompt{})
	if err != nil {
		t.Fatalf("agent: %v", err)
	}

	if len(mock.captured) != 1 {
		t.Fatalf("captured: got %d prompts, want 1", len(mock.captured))
	}
	tools := mock.captured[0].Tools
	if len(tools) != 1 {
		t.Fatalf("tools in prompt: got %d, want 1", len(tools))
	}
	if tools[0].Name != "my_tool" {
		t.Errorf("tool name: got %q", tools[0].Name)
	}
}

// === Test 4: caller's existing tools are preserved ==========================

func TestLoop_PreservesCallerTools(t *testing.T) {
	mock := &scriptedLLM{script: []llm.Response{
		{
			Messages:   []llm.Message{llm.AssistantText("ok")},
			StopReason: llm.StopEndTurn,
		},
	}}

	binding := llm.ToolBinding{
		Spec: llm.ToolSpec{Name: "bound", InputSchema: json.RawMessage(`{}`)},
	}

	agent := llm.Loop(mock.arrow(), []llm.ToolBinding{binding})
	_, err := agent(context.Background(), llm.Prompt{
		Tools: []llm.ToolSpec{{Name: "callerOwn", InputSchema: json.RawMessage(`{}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}

	tools := mock.captured[0].Tools
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2 (callerOwn + bound)", len(tools))
	}
	if tools[0].Name != "callerOwn" || tools[1].Name != "bound" {
		t.Errorf("tools: got [%s %s], want [callerOwn bound]",
			tools[0].Name, tools[1].Name)
	}
}

// === Test 5: tool error fed back as tool_result by default ==================

func TestLoop_ToolErrorFedBack(t *testing.T) {
	mock := &scriptedLLM{script: []llm.Response{
		{
			Messages: []llm.Message{{
				Role: llm.RoleAssistant,
				Content: []llm.Block{{
					Kind:      llm.BlockToolUse,
					ToolUseID: "call_1",
					ToolName:  "broken",
					ToolInput: json.RawMessage(`{}`),
				}},
			}},
			StopReason: llm.StopToolUse,
		},
		{
			Messages:   []llm.Message{llm.AssistantText("I tried but it failed.")},
			StopReason: llm.StopEndTurn,
		},
	}}

	binding := llm.ToolBinding{
		Spec: llm.ToolSpec{Name: "broken"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "", errors.New("simulated failure")
		},
	}

	agent := llm.Loop(mock.arrow(), []llm.ToolBinding{binding})
	resp, err := agent(context.Background(), llm.Prompt{})
	if err != nil {
		t.Fatalf("loop should have continued past tool error, got %v", err)
	}
	if !strings.Contains(resp.Text(), "tried but it failed") {
		t.Errorf("text: got %q", resp.Text())
	}

	// Verify the tool error appears as a tool_result block in the
	// second LLM call's prompt.
	if len(mock.captured) != 2 {
		t.Fatalf("captured: %d, want 2", len(mock.captured))
	}
	secondCall := mock.captured[1]
	found := false
	for _, msg := range secondCall.Messages {
		for _, b := range msg.Content {
			if b.Kind == llm.BlockToolResult && strings.Contains(b.ToolResult, "simulated failure") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("error not fed back as tool_result")
	}
}

// === Test 6: hallucinated tool name fed back as tool_result =================

func TestLoop_HallucinatedToolNameFedBack(t *testing.T) {
	mock := &scriptedLLM{script: []llm.Response{
		{
			Messages: []llm.Message{{
				Role: llm.RoleAssistant,
				Content: []llm.Block{{
					Kind:      llm.BlockToolUse,
					ToolUseID: "call_1",
					ToolName:  "nonexistent",
					ToolInput: json.RawMessage(`{}`),
				}},
			}},
			StopReason: llm.StopToolUse,
		},
		{
			Messages:   []llm.Message{llm.AssistantText("apologies, I don't have that tool")},
			StopReason: llm.StopEndTurn,
		},
	}}

	binding := llm.ToolBinding{
		Spec: llm.ToolSpec{Name: "real_tool"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	}

	agent := llm.Loop(mock.arrow(), []llm.ToolBinding{binding})
	_, err := agent(context.Background(), llm.Prompt{})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}

	// The second call's prompt should include a tool_result block
	// mentioning the missing tool name, so the LLM can recover.
	secondCall := mock.captured[1]
	resultMsg := secondCall.Messages[len(secondCall.Messages)-1]
	if resultMsg.Role != llm.RoleUser || len(resultMsg.Content) != 1 {
		t.Fatalf("expected one user message with one tool_result block")
	}
	if !strings.Contains(resultMsg.Content[0].ToolResult, "nonexistent") {
		t.Errorf("missing tool name not in result: %q",
			resultMsg.Content[0].ToolResult)
	}
}

// === Test 7: MaxIter caps runaway loops =====================================

func TestLoop_MaxIterCap(t *testing.T) {
	const N = 5
	script := make([]llm.Response, N)
	for i := range script {
		script[i] = llm.Response{
			Messages: []llm.Message{{
				Role: llm.RoleAssistant,
				Content: []llm.Block{{
					Kind:      llm.BlockToolUse,
					ToolUseID: "call",
					ToolName:  "noop",
					ToolInput: json.RawMessage(`{}`),
				}},
			}},
			StopReason: llm.StopToolUse,
		}
	}
	mock := &scriptedLLM{script: script}

	binding := llm.ToolBinding{
		Spec: llm.ToolSpec{Name: "noop"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	}

	agent := llm.Loop(mock.arrow(), []llm.ToolBinding{binding}, llm.WithMaxIter(3))
	_, err := agent(context.Background(), llm.Prompt{})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if got := mock.calls.Load(); got != 3 {
		t.Errorf("calls: got %d, want 3 (capped)", got)
	}
}

// === Test 8: callback fires per iteration ===================================

func TestLoop_IterCallback(t *testing.T) {
	mock := &scriptedLLM{script: []llm.Response{
		{
			Messages: []llm.Message{{
				Role: llm.RoleAssistant,
				Content: []llm.Block{{
					Kind:      llm.BlockToolUse,
					ToolUseID: "c1",
					ToolName:  "noop",
					ToolInput: json.RawMessage(`{}`),
				}},
			}},
			StopReason: llm.StopToolUse,
		},
		{
			Messages:   []llm.Message{llm.AssistantText("done")},
			StopReason: llm.StopEndTurn,
		},
	}}

	binding := llm.ToolBinding{
		Spec: llm.ToolSpec{Name: "noop"},
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "ok", nil
		},
	}

	var iters atomic.Int32
	agent := llm.Loop(mock.arrow(), []llm.ToolBinding{binding},
		llm.WithIterCallback(func(_ int, _ llm.Response) {
			iters.Add(1)
		}),
	)
	_, err := agent(context.Background(), llm.Prompt{})
	if err != nil {
		t.Fatal(err)
	}
	if iters.Load() != 2 {
		t.Errorf("callback fired %d times, want 2", iters.Load())
	}
}

// === Test 9: duplicate binding name panics ==================================

func TestLoop_DuplicateBindingPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on duplicate name")
		}
	}()

	dup := llm.ToolBinding{Spec: llm.ToolSpec{Name: "dup"}}
	llm.Loop(nil, []llm.ToolBinding{dup, dup})
}

// === Test 10: composes with other weft combinators ==========================

func TestLoop_ComposesAsArrow(t *testing.T) {
	// The loop is just an Arrow[Prompt, Response]. Verify it slots
	// into weft.WithTap and weft.WithTimeout without complaint.
	mock := &scriptedLLM{script: []llm.Response{
		{
			Messages:   []llm.Message{llm.AssistantText("hello")},
			StopReason: llm.StopEndTurn,
		},
	}}

	agent := llm.Loop(mock.arrow(), nil)

	tapCount := atomic.Int32{}
	tapped := weft.WithTap[llm.Prompt, llm.Response](
		func(_ llm.Prompt, _ llm.Response, _ error) { tapCount.Add(1) },
	)(agent)

	// Generous timeout so the test isn't time-sensitive.
	timed := weft.WithTimeout[llm.Prompt, llm.Response](time.Second)(tapped)

	resp, err := timed(context.Background(), llm.Prompt{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text() != "hello" {
		t.Errorf("text: got %q", resp.Text())
	}
	if tapCount.Load() != 1 {
		t.Errorf("tap fired %d times, want 1", tapCount.Load())
	}
}
