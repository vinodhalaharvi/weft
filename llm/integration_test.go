package llm_test

// This file demonstrates the full composition story end-to-end:
// codegen-shaped pipeline → custom transformer → llm.Claude arrow → mock API.
//
// We intentionally don't import the codegen package here (to avoid an import
// cycle through tests), but we mirror its shape: a per-item arrow that takes
// a typed request and returns a typed response, composed with weft.Traverse.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vinodhalaharvi/weft/llm"
	"github.com/vinodhalaharvi/weft/weft"
)

type fileReq struct {
	Path    string
	Content string
	Prompt  string
}
type fileResp struct {
	Path        string
	NewContent  string
	Explanation string
}

// TestFullPipeline_LLMTransformerWithRetryAndTraverse exercises the
// composition that codegen.Pipeline uses:
//
//	Traverse(
//	    Pipe3(formatPrompt, llm.Claude, parseResponse)
//	    + WithRetry
//	)
//
// Against a mock Anthropic server that fails twice then succeeds, we
// verify:
//   - the retry transform retries transient errors
//   - the LLM arrow returns valid Response objects
//   - the parser extracts the new content
//   - Traverse runs multiple files concurrently
//   - the final aggregated result is correct
func TestFullPipeline_LLMTransformerWithRetryAndTraverse(t *testing.T) {
	// Mock server: simulate a flaky API that succeeds on the 2nd call per file.
	failures := atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Parse the inbound request to extract what file we're working on.
		var req struct {
			Messages []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		userText := req.Messages[0].Content[0].Text

		// Fail the first call for "flaky.go", then succeed.
		if strings.Contains(userText, "flaky.go") && failures.Add(1) == 1 {
			w.WriteHeader(429)
			w.Write([]byte(`{"error":"rate limit"}`))
			return
		}

		// Echo back the file path + transformed content.
		path := extractPath(userText)
		modified := strings.ToUpper(extractFileContent(userText))

		// Build the assistant text content; JSON-encode it to handle newlines.
		assistantText := "```\n" + modified + "\n```\n\nEXPLANATION: uppercased"
		textJSON, _ := json.Marshal(assistantText)

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"id": "msg_%s",
			"type": "message",
			"role": "assistant",
			"model": "claude-test",
			"content": [{"type": "text", "text": %s}],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 10, "output_tokens": 5}
		}`, path, string(textJSON))
	}))
	defer srv.Close()

	// === Build the per-file transformer (mirrors codegen-llm/main.go) ====
	claudeArrow := llm.Claude("claude-test",
		llm.WithAPIKey("test"),
		llm.WithAPIBase(srv.URL),
		llm.WithHTTPClient(srv.Client()),
	)

	transformer := weft.Pipe3(
		// Format request
		weft.Pure(func(req fileReq) llm.Prompt {
			return llm.Prompt{
				Messages: []llm.Message{
					llm.UserText(fmt.Sprintf(
						"File: %s\n\nInstruction: %s\n\nCurrent content:\n```\n%s\n```",
						req.Path, req.Prompt, req.Content,
					)),
				},
			}
		}),
		// LLM call
		claudeArrow,
		// Parse response — needs to know which file we were processing.
		weft.Pure(func(r llm.Response) fileResp {
			text := r.Text()
			return fileResp{
				NewContent:  extractCodeBlock(text),
				Explanation: extractExplanation(text),
			}
		}),
	)

	// === Wrap with retry — flaky.go should succeed on 2nd attempt =======
	transformer = weft.WithRetry[fileReq, fileResp](
		3, weft.LinearBackoff(time.Millisecond),
	)(transformer)

	// === Use Traverse to process multiple files ==========================
	pipeline := weft.Traverse(transformer,
		weft.WithConcurrency(2),
		weft.OnError(weft.PartialResults),
	)

	// === Run ============================================================
	files := []fileReq{
		{Path: "good1.go", Content: "package good1", Prompt: "uppercase"},
		{Path: "flaky.go", Content: "package flaky", Prompt: "uppercase"}, // first attempt fails
		{Path: "good2.go", Content: "package good2", Prompt: "uppercase"},
	}

	results, err := pipeline(context.Background(), files)
	if err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}

	// Verify all three files were transformed correctly.
	expected := map[int]string{
		0: "PACKAGE GOOD1",
		1: "PACKAGE FLAKY", // succeeded after retry
		2: "PACKAGE GOOD2",
	}
	for i, want := range expected {
		if results[i].NewContent != want {
			t.Errorf("result[%d].NewContent: got %q, want %q",
				i, results[i].NewContent, want)
		}
	}

	// Verify the retry actually happened (the rate-limit response counts as +1).
	if failures.Load() < 2 {
		t.Errorf("expected at least 2 server hits for flaky.go (1 fail + 1 retry), got %d",
			failures.Load())
	}
}

// === Helpers (would normally live in the codegen-llm example) ================

func extractPath(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "File: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "File: "))
		}
	}
	return "unknown"
}

func extractFileContent(text string) string {
	// Pull content between the LAST pair of triple backticks
	idx := strings.LastIndex(text, "```\n")
	if idx < 0 {
		return ""
	}
	rest := text[idx+4:]
	end := strings.Index(rest, "\n```")
	if end < 0 {
		return strings.TrimSpace(rest)
	}
	return rest[:end]
}

func extractCodeBlock(text string) string {
	start := strings.Index(text, "```\n")
	if start < 0 {
		return ""
	}
	rest := text[start+4:]
	end := strings.Index(rest, "\n```")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func extractExplanation(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "EXPLANATION:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "EXPLANATION:"))
		}
	}
	return ""
}
