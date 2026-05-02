// Package example_test demonstrates the weft package with a complete
// worked example that exercises every major combinator and shows the
// role-erasure property concretely: pure functions, simulated MCP
// tool calls, simulated LLM calls, and agent loops all compose under
// the same Arrow type.
//
// This file lives in _test.go so it doesn't pollute the public API
// surface but is exercised by the test runner — if the framework
// breaks, this example breaks loudly.
package weft_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vinodhalaharvi/weft/weft"
)

// === Domain types ============================================================

type Issue struct {
	ID    int
	Title string
	Body  string
}

type Category int

const (
	CatBug Category = iota
	CatFeature
	CatDoc
)

type Conclusion struct {
	IssueID    int
	Category   Category
	Suggestion string
}

// === Simulated arrows ========================================================
// In a real codebase, these would be: mcp.Tool[...] for tools,
// llm.Claude(...) for LLM calls, etc. Here we simulate them so the
// example runs hermetically. The key point: from the perspective of
// the combinators, they're all just Arrow values.

// Pretend this came from an MCP server — would be mcp.Tool[ListReq, []Issue].
func fetchIssues() weft.Arrow[string, []Issue] {
	return weft.ArrowFunc(func(_ context.Context, repo string) ([]Issue, error) {
		// Simulated network latency
		time.Sleep(10 * time.Millisecond)
		return []Issue{
			{ID: 1, Title: "[bug] crash on null input", Body: "..."},
			{ID: 2, Title: "[feature] add dark mode", Body: "..."},
			{ID: 3, Title: "[docs] fix typo in README", Body: "..."},
			{ID: 4, Title: "[bug] memory leak in worker", Body: "..."},
			{ID: 5, Title: "[feature] export to CSV", Body: "..."},
		}, nil
	})
}

// Pretend this is an LLM — would be llm.Claude("..."). We make it
// stochastic enough to be interesting: returns "uncertain" for some
// inputs so we can demonstrate fallback.
var errAmbiguous = errors.New("classifier ambiguous")

func llmClassifier() weft.Arrow[Issue, Conclusion] {
	return weft.ArrowFunc(func(_ context.Context, issue Issue) (Conclusion, error) {
		time.Sleep(20 * time.Millisecond) // simulated LLM latency
		// Pretend the LLM is uncertain about issue #5
		if issue.ID == 5 {
			return Conclusion{}, errAmbiguous
		}
		return Conclusion{
			IssueID:    issue.ID,
			Category:   classifyByPrefix(issue.Title),
			Suggestion: fmt.Sprintf("LLM-suggested fix for #%d", issue.ID),
		}, nil
	})
}

// Pretend this is a fallback rules-based classifier — pure Go function.
func rulesClassifier() weft.Arrow[Issue, Conclusion] {
	return weft.ArrowFunc(func(_ context.Context, issue Issue) (Conclusion, error) {
		return Conclusion{
			IssueID:    issue.ID,
			Category:   classifyByPrefix(issue.Title),
			Suggestion: fmt.Sprintf("rules-based handling for #%d", issue.ID),
		}, nil
	})
}

func classifyByPrefix(title string) Category {
	switch {
	case strings.HasPrefix(title, "[bug]"):
		return CatBug
	case strings.HasPrefix(title, "[feature]"):
		return CatFeature
	case strings.HasPrefix(title, "[docs]"):
		return CatDoc
	default:
		return CatBug
	}
}

// Pretend this is another MCP tool — formats a report.
func renderReport() weft.Arrow[[]Conclusion, string] {
	return weft.ArrowFunc(func(_ context.Context, cs []Conclusion) (string, error) {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("Triage report (%d items):\n", len(cs)))
		for _, c := range cs {
			b.WriteString(fmt.Sprintf("  #%d [%v] %s\n", c.IssueID, c.Category, c.Suggestion))
		}
		return b.String(), nil
	})
}

// === The pipeline ============================================================

// TestEndToEnd_TriagePipeline assembles a realistic pipeline using
// every major combinator the framework provides:
//
//   - Compose for sequential stages
//   - Fallback for LLM → rules-based degradation
//   - Traverse for parallel per-item processing
//   - Apply with WithRetry and WithTimeout for resilience
//
// And it demonstrates that the *types* line up end-to-end without
// any interface{} or runtime dispatch.
func TestEndToEnd_TriagePipeline(t *testing.T) {
	// The classifier: try LLM first, fall back to rules on ambiguity.
	// This is the role-erasure visible: llmClassifier() is a "simulated
	// LLM call" and rulesClassifier() is a "pure function," but both
	// produce weft.Arrow[Issue, Conclusion] and Fallback works on them
	// identically.
	classifier := weft.OnSentinel[Issue, Conclusion](
		errAmbiguous,
		rulesClassifier(),
	)(llmClassifier())

	// Wrap the classifier with cross-cutting concerns.
	classifier = weft.Apply[Issue, Conclusion](classifier,
		weft.WithRetry[Issue, Conclusion](2, weft.LinearBackoff(time.Millisecond)),
		weft.WithTimeout[Issue, Conclusion](500*time.Millisecond),
	)

	// Apply the classifier to each issue with bounded concurrency.
	classifyAll := weft.Traverse(classifier,
		weft.WithConcurrency(3),
		weft.OnError(weft.PartialResults),
	)
	// classifyAll : Arrow[[]Issue, []Conclusion]

	// The full pipeline: fetch → classify-each → report.
	pipeline := weft.Pipe3(
		fetchIssues(),  // Arrow[string,       []Issue]
		classifyAll,    // Arrow[[]Issue,      []Conclusion]
		renderReport(), // Arrow[[]Conclusion, string]
	)
	// pipeline : Arrow[string, string]

	// Run it.
	report, err := pipeline(context.Background(), "anthropic/example-repo")
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}

	// Sanity checks on the output.
	expected := []string{
		"Triage report (5 items):",
		"#1 [0]",                      // CatBug
		"#2 [1]",                      // CatFeature
		"#3 [2]",                      // CatDoc
		"#4 [0]",                      // CatBug
		"#5 [1]",                      // CatFeature - this one used the rules fallback
		"rules-based handling for #5", // proves the fallback ran
		"LLM-suggested fix for #1",    // proves the LLM ran for others
	}
	for _, want := range expected {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q\nGot:\n%s", want, report)
		}
	}

	t.Logf("Report:\n%s", report)
}

// TestEndToEnd_RoleSwapping demonstrates the framework's central
// claim: a stage can be implemented as one kind of arrow and replaced
// with a completely different kind without changing surrounding code.
func TestEndToEnd_RoleSwapping(t *testing.T) {
	pipeline := func(classifier weft.Arrow[Issue, Conclusion]) weft.Arrow[string, string] {
		return weft.Pipe3(
			fetchIssues(),
			weft.Traverse(classifier, weft.WithConcurrency(3)),
			renderReport(),
		)
	}

	// Same pipeline shape, three completely different classifiers.
	t.Run("rules-only", func(t *testing.T) {
		report, err := pipeline(rulesClassifier())(context.Background(), "repo")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(report, "rules-based") {
			t.Errorf("expected rules-based output: %s", report)
		}
	})

	t.Run("LLM-with-fallback", func(t *testing.T) {
		c := weft.OnSentinel[Issue, Conclusion](errAmbiguous, rulesClassifier())(llmClassifier())
		report, err := pipeline(c)(context.Background(), "repo")
		if err != nil {
			t.Fatal(err)
		}
		// Some are LLM-suggested, the ambiguous one is rules-based.
		if !strings.Contains(report, "LLM-suggested") {
			t.Error("expected LLM output for non-ambiguous issues")
		}
		if !strings.Contains(report, "rules-based handling for #5") {
			t.Error("expected rules fallback for ambiguous issue #5")
		}
	})

	t.Run("ensemble-vote", func(t *testing.T) {
		// Run two classifiers in parallel, take whichever's not "uncertain".
		// This exercises Par + Map.
		ensemble := weft.Map(
			weft.Par(llmClassifier(), rulesClassifier()),
			func(p weft.Pair[Conclusion, Conclusion]) Conclusion {
				// Prefer LLM if its result is non-zero, else rules.
				if p.Fst.IssueID != 0 {
					return p.Fst
				}
				return p.Snd
			},
		)
		// LLM returns errAmbiguous for #5, which causes Par to fail there.
		// We want the test to demonstrate ensemble works for the easy cases
		// and falls back to rules-only for the hard ones.
		safeEnsemble := weft.Fallback(ensemble, rulesClassifier())

		report, err := pipeline(safeEnsemble)(context.Background(), "repo")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("Ensemble report:\n%s", report)
	})
}
