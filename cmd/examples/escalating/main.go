// Command escalating demonstrates a cost-tiered LLM call: try a cheap
// model first, escalate to a stronger one only when the cheap answer
// looks unconfident.
//
// This is the closure form of the pattern. The whole "tier" is just a
// regular Go function that happens to take and return the same shapes
// as llm.Claude. Anywhere you'd use llm.Claude(...) directly, you can
// drop in `escalating` instead — the type is the same.
//
// Usage:
//
//	export ANTHROPIC_API_KEY=sk-ant-...
//	go run ./cmd/examples/escalating "What's the capital of France?"
//	go run ./cmd/examples/escalating "Explain why category theory is useful for software design"
//
// The first prompt is easy; Haiku will likely answer confidently and
// Opus is never called. The second is open-ended; Haiku may hedge, in
// which case we escalate to Opus.
//
// What this example demonstrates:
//
//   - Two llm.Claude arrows can be composed without any framework code —
//     they're just functions, you call them in an `if`.
//   - The result has the same type as a single LLM call:
//     func(context.Context, llm.Prompt) (llm.Response, error)
//     ...so it composes with weft.WithRetry, weft.Traverse, weft.Apply,
//     and everything else in the framework, identically.
//   - The "tier" is invisible to anything that consumes it.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/vinodhalaharvi/weft/llm"
)

// Models — adjust to current Anthropic naming if these are stale.
const (
	cheapModel  = "claude-haiku-4-5"
	strongModel = "claude-opus-4-5"
)

// escalating tries cheapModel first. If the result looks like a hedge
// or a refusal, it escalates to strongModel and returns that instead.
//
// Crucially: the type of `escalating` is the same as `llm.Claude(model)`.
// It IS an arrow, just constructed as a closure rather than a primitive.
func escalating(ctx context.Context, p llm.Prompt) (llm.Response, error) {
	cheap := llm.Claude(cheapModel)
	strong := llm.Claude(strongModel)

	resp, err := cheap(ctx, p)
	if err != nil {
		// Real error from the cheap path — propagate. The framework's
		// error classification (transient vs permanent) is preserved
		// because we're returning *weft.ArrowError unchanged.
		return resp, err
	}

	if isConfident(resp) {
		log.Printf("✓ haiku answered confidently (%d output tokens)", resp.Usage.OutputTokens)
		return resp, nil
	}

	log.Printf("→ haiku hedged; escalating to opus")
	return strong(ctx, p)
}

// isConfident is a heuristic for "the cheap model answered the question
// cleanly." Real systems would parse a structured confidence score from
// the response; for this example we look for hedging language.
func isConfident(r llm.Response) bool {
	text := strings.ToLower(r.Text())
	hedges := []string{
		"i'm not sure",
		"i don't know",
		"it depends",
		"i can't",
		"it's unclear",
		"i would need",
		"more context",
	}
	for _, h := range hedges {
		if strings.Contains(text, h) {
			return false
		}
	}
	// Also treat very short responses as suspicious — usually the model
	// is punting when it gives a one-line answer to an open question.
	if len(strings.TrimSpace(r.Text())) < 30 {
		return false
	}
	return true
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: escalating <question>")
		fmt.Fprintln(os.Stderr, "       go run ./cmd/examples/escalating \"What's the capital of France?\"")
		os.Exit(1)
	}
	question := strings.Join(os.Args[1:], " ")

	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "error: ANTHROPIC_API_KEY not set")
		os.Exit(1)
	}

	ctx := context.Background()
	prompt := llm.Prompt{
		System: "You are a helpful, concise assistant. If you don't know something, say so plainly.",
		Messages: []llm.Message{
			llm.UserText(question),
		},
		MaxTokens:   1024,
		Temperature: 0.3,
	}

	resp, err := escalating(ctx, prompt)
	if err != nil {
		log.Fatalf("escalating failed: %v", err)
	}

	fmt.Println()
	fmt.Println(resp.Text())
	fmt.Println()
	fmt.Printf("[model=%s tokens_in=%d tokens_out=%d]\n",
		resp.Model, resp.Usage.InputTokens, resp.Usage.OutputTokens)
}
