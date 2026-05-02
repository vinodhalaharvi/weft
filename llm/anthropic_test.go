package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vinodhalaharvi/weft/weft"
)

// === Test helpers =============================================================

// mockServer creates an httptest.Server that responds with the given handler
// and returns a Claude arrow pointed at it. The test gets fine-grained
// control over the server's behavior.
func mockServer(t *testing.T, handler http.HandlerFunc) (weft.Arrow[Prompt, Response], func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	arrow := Claude("claude-test",
		WithAPIKey("test-key"),
		WithAPIBase(srv.URL),
		WithHTTPClient(srv.Client()),
	)
	return arrow, srv.Close
}

// validResponseBody returns a minimal valid Anthropic response body.
func validResponseBody(text string) string {
	return `{
		"id": "msg_test_123",
		"type": "message",
		"role": "assistant",
		"model": "claude-test",
		"content": [{"type": "text", "text": "` + text + `"}],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`
}

// === Happy path ==============================================================

func TestClaude_BasicTextResponse(t *testing.T) {
	arrow, cleanup := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Verify the request shape
		if r.Method != "POST" {
			t.Errorf("method: got %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path: got %s, want /v1/messages", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("api key header missing")
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Errorf("anthropic-version header missing")
		}

		// Verify the body parses as expected JSON
		body, _ := io.ReadAll(r.Body)
		var req anthropicRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("body did not parse as anthropicRequest: %v", err)
		}
		if req.Model != "claude-test" {
			t.Errorf("model: got %s, want claude-test", req.Model)
		}
		if len(req.Messages) != 1 || req.Messages[0].Role != "user" {
			t.Errorf("messages: %+v", req.Messages)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(validResponseBody("Hello, world!")))
	})
	defer cleanup()

	resp, err := arrow(context.Background(), Prompt{
		Messages: []Message{UserText("hi")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Text() != "Hello, world!" {
		t.Errorf("text: got %q, want %q", resp.Text(), "Hello, world!")
	}
	if resp.StopReason != StopEndTurn {
		t.Errorf("stop reason: got %v, want StopEndTurn", resp.StopReason)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 {
		t.Errorf("usage: got %+v, want {10, 5}", resp.Usage)
	}
	if resp.Model != "claude-test" {
		t.Errorf("model: got %s", resp.Model)
	}
	if resp.RawID != "msg_test_123" {
		t.Errorf("raw id: got %s", resp.RawID)
	}
}

func TestClaude_SystemPromptInTopLevelField(t *testing.T) {
	// System messages should NOT appear in the messages array; they should
	// be set on the top-level System field.
	arrow, cleanup := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req anthropicRequest
		json.Unmarshal(body, &req)

		if req.System != "you are a helpful assistant" {
			t.Errorf("system: got %q", req.System)
		}
		// Messages should only contain the user message, not the system one.
		for _, m := range req.Messages {
			if m.Role == "system" {
				t.Errorf("system role leaked into messages array")
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(validResponseBody("ok")))
	})
	defer cleanup()

	_, err := arrow(context.Background(), Prompt{
		System:   "you are a helpful assistant",
		Messages: []Message{UserText("hello")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClaude_ToolUseResponse(t *testing.T) {
	arrow, cleanup := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id": "msg_tool",
			"type": "message",
			"role": "assistant",
			"model": "claude-test",
			"content": [
				{"type": "text", "text": "I'll search for that."},
				{"type": "tool_use", "id": "tu_1", "name": "search", "input": {"query": "go generics"}}
			],
			"stop_reason": "tool_use",
			"usage": {"input_tokens": 20, "output_tokens": 30}
		}`))
	})
	defer cleanup()

	resp, err := arrow(context.Background(), Prompt{
		Messages: []Message{UserText("look it up")},
		Tools: []ToolSpec{{
			Name:        "search",
			Description: "search the web",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StopReason != StopToolUse {
		t.Errorf("stop reason: got %v, want StopToolUse", resp.StopReason)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].ToolName != "search" {
		t.Errorf("tool name: got %s", calls[0].ToolName)
	}
	if calls[0].ToolUseID != "tu_1" {
		t.Errorf("tool use id: got %s", calls[0].ToolUseID)
	}
	// Text and tool_use both present
	if !strings.Contains(resp.Text(), "search for that") {
		t.Errorf("expected text alongside tool use, got %q", resp.Text())
	}
}

// === Error classification ====================================================

func TestClaude_RateLimitIsTransient(t *testing.T) {
	arrow, cleanup := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		w.Write([]byte(`{"error": {"type": "rate_limit", "message": "slow down"}}`))
	})
	defer cleanup()

	_, err := arrow(context.Background(), Prompt{Messages: []Message{UserText("x")}})
	if err == nil {
		t.Fatal("expected error")
	}
	var ae *weft.ArrowError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *weft.ArrowError, got %T", err)
	}
	if ae.Class != weft.ClassTransient {
		t.Errorf("rate limit should be ClassTransient, got %v", ae.Class)
	}
}

func TestClaude_AuthErrorIsPermanent(t *testing.T) {
	arrow, cleanup := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error": {"type": "authentication_error"}}`))
	})
	defer cleanup()

	_, err := arrow(context.Background(), Prompt{Messages: []Message{UserText("x")}})
	if err == nil {
		t.Fatal("expected error")
	}
	var ae *weft.ArrowError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *weft.ArrowError, got %T", err)
	}
	if ae.Class != weft.ClassPermanent {
		t.Errorf("auth error should be ClassPermanent, got %v", ae.Class)
	}
}

func TestClaude_ServerErrorIsTransient(t *testing.T) {
	arrow, cleanup := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error": {"type": "internal_error"}}`))
	})
	defer cleanup()

	_, err := arrow(context.Background(), Prompt{Messages: []Message{UserText("x")}})
	var ae *weft.ArrowError
	if !errors.As(err, &ae) || ae.Class != weft.ClassTransient {
		t.Errorf("5xx should be ClassTransient, got %v / %v", ae, err)
	}
}

func TestClaude_NoAPIKeyIsPermanent(t *testing.T) {
	// Make sure no key is in the environment for this test, regardless of
	// what the developer's shell has. t.Setenv restores the prior value
	// when the test completes.
	t.Setenv("ANTHROPIC_API_KEY", "")

	arrow := Claude("claude-test", WithAPIBase("http://nope"))
	_, err := arrow(context.Background(), Prompt{Messages: []Message{UserText("x")}})
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
	var ae *weft.ArrowError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *weft.ArrowError, got %T: %v", err, err)
	}
	if ae.Class != weft.ClassPermanent {
		t.Errorf("missing key should be ClassPermanent, got %v", ae.Class)
	}
}

// === Cancellation ============================================================

func TestClaude_CancellationPropagates(t *testing.T) {
	arrow, cleanup := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Block until the request's context is cancelled.
		<-r.Context().Done()
	})
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := arrow(ctx, Prompt{Messages: []Message{UserText("x")}})
	if err == nil {
		t.Fatal("expected error after cancellation")
	}
	var ae *weft.ArrowError
	if errors.As(err, &ae) && ae.Class != weft.ClassUserCancelled {
		// Or the underlying http error reports cancellation differently;
		// either way, we expect *some* indication of cancellation.
		t.Logf("got class %v with cause %v (acceptable if cancellation was detected)", ae.Class, ae.Cause)
	}
}

// === Composition with weft transforms ========================================

func TestClaude_WorksWithWithRetry(t *testing.T) {
	calls := 0
	arrow, cleanup := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(429)
			w.Write([]byte(`{"error": "rate limit"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(validResponseBody("eventually")))
	})
	defer cleanup()

	// Wrap with retry — exactly the framework's promise: any arrow,
	// any transform, no special integration needed.
	withRetry := weft.WithRetry[Prompt, Response](
		5, weft.LinearBackoff(1),
	)(arrow)

	resp, err := withRetry(context.Background(), Prompt{
		Messages: []Message{UserText("hi")},
	})
	if err != nil {
		t.Fatalf("retry should have succeeded: %v", err)
	}
	if resp.Text() != "eventually" {
		t.Errorf("text: got %q", resp.Text())
	}
	if calls != 3 {
		t.Errorf("expected 3 attempts, got %d", calls)
	}
}

// === Round-trip encoding =====================================================

func TestClaude_ToolResultsEncodeAsUserRole(t *testing.T) {
	// When the user supplies a Message with Role=Tool, Anthropic expects
	// it to be encoded as a "user" role message with tool_result content.
	arrow, cleanup := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req anthropicRequest
		json.Unmarshal(body, &req)

		// Find the message we sent as RoleTool — should now be "user".
		foundToolResult := false
		for _, m := range req.Messages {
			for _, c := range m.Content {
				if c.Type == "tool_result" {
					foundToolResult = true
					if m.Role != "user" {
						t.Errorf("tool_result message role: got %s, want user", m.Role)
					}
					if c.ToolUseID != "tu_42" {
						t.Errorf("tool_use_id: got %s", c.ToolUseID)
					}
				}
			}
		}
		if !foundToolResult {
			t.Error("did not find tool_result block in request")
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(validResponseBody("ok")))
	})
	defer cleanup()

	_, err := arrow(context.Background(), Prompt{
		Messages: []Message{
			UserText("call the tool"),
			{
				Role: RoleAssistant,
				Content: []Block{{
					Kind:      BlockToolUse,
					ToolUseID: "tu_42",
					ToolName:  "search",
					ToolInput: json.RawMessage(`{"q":"x"}`),
				}},
			},
			{
				Role: RoleTool,
				Content: []Block{{
					Kind:         BlockToolResult,
					ToolResultID: "tu_42",
					ToolResult:   "found 3 results",
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// === Sanity ===================================================================

func TestClaude_ProducesArrowOfRightType(t *testing.T) {
	// Compile-time check that Claude returns weft.Arrow[Prompt, Response].
	var _ weft.Arrow[Prompt, Response] = Claude("any-model", WithAPIKey("x"))
}

func TestUsage_AddsCorrectly(t *testing.T) {
	a := Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 100, CacheWriteTokens: 50}
	b := Usage{InputTokens: 20, OutputTokens: 10, CacheReadTokens: 0, CacheWriteTokens: 0}
	got := a.Add(b)
	want := Usage{InputTokens: 30, OutputTokens: 15, CacheReadTokens: 100, CacheWriteTokens: 50}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestResponse_TextConcatenatesMultipleBlocks(t *testing.T) {
	r := Response{
		Messages: []Message{{
			Role: RoleAssistant,
			Content: []Block{
				{Kind: BlockText, Text: "Hello, "},
				{Kind: BlockToolUse, ToolName: "search"}, // should be skipped
				{Kind: BlockText, Text: "world!"},
			},
		}},
	}
	if got := r.Text(); got != "Hello, world!" {
		t.Errorf("got %q", got)
	}
}
