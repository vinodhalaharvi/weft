// Package main is a runnable example of the codegen pipeline.
//
// Usage:
//
//	go run ./examples/codegen -dir ./testdata -prompt "add a doc comment to each function" -dry
//	go run ./examples/codegen -dir ./testdata -prompt "..." -patterns "*.go" -concurrency 4
//
// Without an ANTHROPIC_API_KEY env var, this uses a deterministic stub
// transformer (uppercases content, prepends a marker). With the key set
// and a real LLM client wired in, this becomes a useful tool.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/vinodhalaharvi/weft/codegen"
	"github.com/vinodhalaharvi/weft/weft"
)

func main() {
	var (
		dir         = flag.String("dir", ".", "directory to process")
		prompt      = flag.String("prompt", "", "instruction to apply to each file (required)")
		patterns    = flag.String("patterns", "**/*", "comma-separated glob patterns, e.g. '*.go,*.md'")
		skip        = flag.String("skip", "vendor/**,node_modules/**,.git/**", "comma-separated skip patterns")
		concurrency = flag.Int("concurrency", 4, "number of files to process in parallel")
		dryRun      = flag.Bool("dry", false, "show diffs without writing")
		timeout     = flag.Duration("timeout", 5*time.Minute, "overall pipeline timeout")
	)
	flag.Parse()

	if *prompt == "" {
		fmt.Fprintln(os.Stderr, "error: -prompt is required")
		flag.Usage()
		os.Exit(1)
	}

	job := codegen.Job{
		Dir:      *dir,
		Patterns: splitCSV(*patterns),
		Skip:     splitCSV(*skip),
		Prompt:   *prompt,
		DryRun:   *dryRun,
	}

	// Pick a transformer. In a real build you'd plug in llm.Claude(...);
	// for the example we use the stub so this runs offline.
	transformer := stubUppercaseTransformer()

	// Build the pipeline. PartialResults so one bad file doesn't abort
	// everything — the user gets a per-file report at the end.
	pipeline := codegen.Pipeline(transformer, *concurrency, weft.PartialResults)

	// Wrap with cross-cutting concerns. This is the thing weft is good at:
	// retry, timeout, and observability all become arrow-to-arrow transforms.
	pipeline = weft.Apply(pipeline,
		weft.WithTimeout[codegen.Job, []codegen.FileResult](*timeout),
	)

	ctx := context.Background()
	results, err := pipeline(ctx, job)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pipeline error: %v\n", err)
		os.Exit(1)
	}

	// Report.
	wrote, unchanged, failed := 0, 0, 0
	for _, r := range results {
		switch {
		case r.Err != nil:
			failed++
			fmt.Printf("✗ %s: %v\n", r.RelPath, r.Err)
		case r.Wrote:
			wrote++
			fmt.Printf("✓ %s — %s\n", r.RelPath, r.Explanation)
		case r.Unchanged:
			unchanged++
			fmt.Printf("· %s (no change)\n", r.RelPath)
		case *dryRun && r.Diff != "":
			// In dry-run, results are neither Wrote nor Unchanged; print the diff header.
			fmt.Printf("~ %s — would change (%d bytes diff)\n", r.RelPath, len(r.Diff))
		}
	}

	fmt.Println()
	fmt.Printf("summary: %d processed (%d wrote, %d unchanged, %d failed)\n",
		len(results), wrote, unchanged, failed)
	if failed > 0 {
		os.Exit(2)
	}
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// stubUppercaseTransformer is a deterministic transformer for offline use.
// In production you'd replace this with an LLM-backed arrow.
func stubUppercaseTransformer() codegen.Transformer {
	return weft.ArrowFunc(func(_ context.Context, req codegen.TransformReq) (codegen.TransformResp, error) {
		newContent := fmt.Sprintf("// %s\n%s",
			strings.TrimSpace(req.Prompt),
			strings.ToUpper(req.File.Content))
		return codegen.TransformResp{
			NewContent:  newContent,
			Explanation: "uppercased + prompt header (stub)",
		}, nil
	})
}
