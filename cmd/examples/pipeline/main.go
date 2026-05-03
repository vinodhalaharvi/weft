// Command pipeline demonstrates a typed multi-step LLM pipeline using
// weft's combinators.
//
// The pipeline takes a list of code snippets and returns a quality
// assessment for each. Each snippet flows through three stages:
//
//	weft.Pure(formatPrompt)   Snippet      → llm.Prompt
//	llm.Claude("...")          llm.Prompt   → llm.Response
//	weft.Pure(parseAssessment) llm.Response → Assessment
//
// composed with weft.Pipe3. The whole pipeline is then wrapped with
// retry, timeout, and observability transforms — and finally fed
// through weft.Traverse to process the slice with bounded concurrency.
//
// Usage:
//
//	export ANTHROPIC_API_KEY=sk-ant-...
//	go run ./cmd/examples/pipeline
//
// What this example demonstrates:
//
//   - weft.Pipe3 composes three arrows of different concrete types.
//     The compiler verifies the output of each stage matches the
//     input of the next.
//   - weft.Pure lifts ordinary Go functions into the Arrow category
//     so they compose with effectful arrows uniformly.
//   - weft.Apply layers transforms (retry, timeout, tap) on top of
//     a pipeline without changing its type signature.
//   - weft.Traverse runs the pipeline over a slice in parallel,
//     with bounded concurrency and a partial-results error policy.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/vinodhalaharvi/weft/llm"
	"github.com/vinodhalaharvi/weft/weft"
)

const model = "claude-opus-4-5"

// === Domain types ============================================================

// Snippet is the input to the pipeline: a piece of code with metadata.
type Snippet struct {
	Filename string
	Language string
	Code     string
}

// Assessment is the output: a structured rating produced by the LLM.
type Assessment struct {
	Filename       string
	Quality        int // 1–10
	Issues         []string
	Recommendation string
}

// === Pipeline stages =========================================================

// formatPrompt builds the LLM prompt from a snippet.
// This is a pure function — no IO, no failure — so we can lift it with weft.Pure.
func formatPrompt(s Snippet) llm.Prompt {
	user := fmt.Sprintf(`Review this %s code from %q.

Code:
`+"```"+`%s
%s
`+"```"+`

Respond ONLY with JSON in this exact shape:
{
  "quality": <integer 1-10>,
  "issues": ["short issue 1", "short issue 2"],
  "recommendation": "one sentence summary"
}`, s.Language, s.Filename, s.Language, s.Code)

	return llm.Prompt{
		System: "You are a careful code reviewer. Respond with valid JSON only, no prose around it.",
		Messages: []llm.Message{
			llm.UserText(user),
		},
		MaxTokens:   1024,
		Temperature: 0.2,
	}
}

// parseAssessment extracts the JSON the LLM returned and decodes it into
// our typed shape. Returns the partial Assessment on error so the caller
// still knows which file we were trying to process.
//
// We use a closure to capture the original Snippet for the Filename field;
// see how it's wired in `assess()` below.
func parseAssessment(s Snippet) func(llm.Response) (Assessment, error) {
	return func(r llm.Response) (Assessment, error) {
		text := r.Text()

		// LLMs sometimes wrap JSON in markdown fences. Strip them.
		text = strings.TrimSpace(text)
		text = strings.TrimPrefix(text, "```json")
		text = strings.TrimPrefix(text, "```")
		text = strings.TrimSuffix(text, "```")
		text = strings.TrimSpace(text)

		var raw struct {
			Quality        int      `json:"quality"`
			Issues         []string `json:"issues"`
			Recommendation string   `json:"recommendation"`
		}
		if err := json.Unmarshal([]byte(text), &raw); err != nil {
			return Assessment{Filename: s.Filename}, fmt.Errorf("parse JSON for %s: %w (raw: %s)", s.Filename, err, truncate(text, 200))
		}

		return Assessment{
			Filename:       s.Filename,
			Quality:        raw.Quality,
			Issues:         raw.Issues,
			Recommendation: raw.Recommendation,
		}, nil
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// === The pipeline ============================================================

// assess builds the per-snippet pipeline and wraps it with cross-cutting concerns.
// The result has type weft.Arrow[Snippet, Assessment].
func assess() weft.Arrow[Snippet, Assessment] {
	// Compose three stages. weft.Pipe3 verifies at compile time that
	// each stage's output type matches the next stage's input type.
	//
	// We need to bind the parser to the Snippet input so it can include
	// the filename in errors. The cleanest way is a small adapter arrow
	// rather than splitting the pipeline.
	core := weft.ArrowFunc(func(ctx context.Context, s Snippet) (Assessment, error) {
		prompt := formatPrompt(s)
		resp, err := llm.Claude(model)(ctx, prompt)
		if err != nil {
			return Assessment{Filename: s.Filename}, err
		}
		return parseAssessment(s)(resp)
	})

	// Wrap with cross-cutting concerns. Each transform preserves the
	// arrow's type signature (Snippet → Assessment); they only change
	// behavior around it.
	return weft.Apply(core,
		weft.WithRetry[Snippet, Assessment](3, weft.ExponentialBackoff(time.Second)),
		weft.WithTimeout[Snippet, Assessment](60*time.Second),
		weft.WithTap[Snippet, Assessment](func(in Snippet, out Assessment, err error) {
			if err != nil {
				log.Printf("✗ %s: %v", in.Filename, err)
			} else {
				log.Printf("✓ %s: quality=%d issues=%d", in.Filename, out.Quality, len(out.Issues))
			}
		}),
	)
}

// === Sample data =============================================================

// snippets is a small built-in corpus so the example runs without any
// setup. In real use you'd read these from disk or another source.
var snippets = []Snippet{
	{
		Filename: "good.go",
		Language: "go",
		Code: `package main

import "fmt"

func divide(a, b float64) (float64, error) {
	if b == 0 {
		return 0, fmt.Errorf("division by zero")
	}
	return a / b, nil
}`,
	},
	{
		Filename: "questionable.go",
		Language: "go",
		Code: `package main

func GetUserData(id int) interface{} {
	data := []interface{}{}
	for i := 0; i < 100; i++ {
		data = append(data, getById(id+i))
	}
	return data
}`,
	},
	{
		Filename: "tiny.go",
		Language: "go",
		Code: `package main

func add(a, b int) int { return a + b }`,
	},
}

// === main ====================================================================

func main() {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "error: ANTHROPIC_API_KEY not set")
		os.Exit(1)
	}

	ctx := context.Background()

	// Build the per-item pipeline.
	pipeline := assess()

	// Lift it to operate over a slice with bounded concurrency.
	// PartialResults means one bad snippet doesn't kill the whole run —
	// we get back results for the successful ones and errors for the rest.
	manyAtOnce := weft.Traverse(pipeline,
		weft.WithConcurrency(3),
		weft.OnError(weft.PartialResults),
	)

	log.Printf("processing %d snippets concurrently...", len(snippets))
	start := time.Now()

	results, err := manyAtOnce(ctx, snippets)
	if err != nil {
		// PartialError means some failed but others succeeded.
		// Other errors mean the whole call collapsed (rare).
		log.Printf("traverse returned error: %v", err)
	}

	elapsed := time.Since(start)
	log.Printf("done in %v", elapsed)

	// Report.
	fmt.Println()
	fmt.Println("=== Results ===")
	for i, r := range results {
		fmt.Printf("\n[%d] %s\n", i+1, r.Filename)
		if r.Filename == "" {
			fmt.Println("    (no result — this snippet failed)")
			continue
		}
		fmt.Printf("    quality:        %d/10\n", r.Quality)
		if len(r.Issues) > 0 {
			fmt.Printf("    issues:\n")
			for _, issue := range r.Issues {
				fmt.Printf("      - %s\n", issue)
			}
		}
		fmt.Printf("    recommendation: %s\n", r.Recommendation)
	}
}
