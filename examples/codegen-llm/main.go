// Package main is a runnable example of the codegen pipeline using a real
// LLM transformer.
//
// Usage:
//
//	# Without an API key — uses the deterministic stub
//	go run ./examples/codegen-llm -dir ./testdata -prompt "..." -dry
//
//	# With an API key — calls Claude
//	export ANTHROPIC_API_KEY=sk-ant-...
//	go run ./examples/codegen-llm -dir ./testdata -prompt "..." -dry
//
// This file demonstrates the full composition story:
//
//  1. The LLM call is a weft.Arrow[Prompt, Response] from the llm package.
//  2. The transformer for codegen is a 3-stage pipeline: format → LLM → parse,
//     composed with weft.Pipe3.
//  3. Cross-cutting concerns (retry, timeout, observability) are weft.Apply
//     transforms layered on top.
//  4. Codegen.Pipeline takes the transformer and produces a job-level arrow.
//
// Every layer is independently swappable. Replace llm.Claude with llm.OpenAI
// (when implemented) and nothing else changes. Replace the parser with one
// that handles a different output format and nothing else changes.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/vinodhalaharvi/weft/codegen"
	"github.com/vinodhalaharvi/weft/llm"
	"github.com/vinodhalaharvi/weft/weft"
)

func main() {
	var (
		dir         = flag.String("dir", ".", "directory to process")
		prompt      = flag.String("prompt", "", "instruction (required)")
		patterns    = flag.String("patterns", "**/*.go", "comma-separated globs")
		skip        = flag.String("skip", "vendor/**,**/*_test.go,.git/**", "skip patterns")
		concurrency = flag.Int("concurrency", 4, "concurrent file workers")
		dryRun      = flag.Bool("dry", false, "show diffs without writing")
		timeout     = flag.Duration("timeout", 5*time.Minute, "overall timeout")
		retries     = flag.Int("retries", 3, "per-file retry count on transient errors")
		model       = flag.String("model", "claude-opus-4-5", "Claude model to use")
		offline     = flag.Bool("offline", false, "force offline stub even if API key is set")
	)
	flag.Parse()

	if *prompt == "" {
		fmt.Fprintln(os.Stderr, "error: -prompt is required")
		flag.Usage()
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// === Build the transformer =============================================
	// The transformer is the per-file LLM-backed arrow. It's three stages
	// composed with weft.Pipe3.
	var transformer codegen.Transformer
	if *offline || os.Getenv("ANTHROPIC_API_KEY") == "" {
		logger.Info("using offline stub transformer (no API key)")
		transformer = stubTransformer()
	} else {
		logger.Info("using Claude transformer", "model", *model)
		transformer = claudeTransformer(*model)
	}

	// === Wrap with cross-cutting concerns ==================================
	transformer = weft.Apply(transformer,
		weft.WithRetry[codegen.TransformReq, codegen.TransformResp](
			*retries, weft.ExponentialBackoff(time.Second)),
		weft.WithTimeout[codegen.TransformReq, codegen.TransformResp](
			60*time.Second),
		weft.WithTap[codegen.TransformReq, codegen.TransformResp](
			func(in codegen.TransformReq, out codegen.TransformResp, err error) {
				if err != nil {
					logger.Warn("transform failed", "file", in.File.RelPath, "err", err)
				} else {
					logger.Info("transform ok", "file", in.File.RelPath)
				}
			}),
	)

	// === Build the pipeline ================================================
	pipeline := codegen.Pipeline(transformer, *concurrency, weft.PartialResults)
	pipeline = weft.WithTimeout[codegen.Job, []codegen.FileResult](*timeout)(pipeline)

	// === Run it ============================================================
	ctx := context.Background()
	results, err := pipeline(ctx, codegen.Job{
		Dir:      *dir,
		Patterns: splitCSV(*patterns),
		Skip:     splitCSV(*skip),
		Prompt:   *prompt,
		DryRun:   *dryRun,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "pipeline error: %v\n", err)
		os.Exit(1)
	}

	report(results, *dryRun)
}

// === The Claude-backed transformer =========================================
//
// This is what you'd swap for OpenAI, Ollama, or another provider. The
// shape is fixed (codegen.Transformer = weft.Arrow[TransformReq, TransformResp]),
// the implementation is a 3-stage pipeline.

func claudeTransformer(model string) codegen.Transformer {
	return weft.Pipe3(
		// Stage 1: TransformReq → llm.Prompt
		weft.Pure(func(req codegen.TransformReq) llm.Prompt {
			return llm.Prompt{
				System: codegenSystemPrompt,
				Messages: []llm.Message{
					llm.UserText(formatUserPrompt(req)),
				},
				MaxTokens:   8192,
				Temperature: 0.2, // low temperature for code edits
			}
		}),
		// Stage 2: llm.Prompt → llm.Response (the actual API call)
		llm.Claude(model),
		// Stage 3: llm.Response → codegen.TransformResp
		weft.Pure(parseClaudeResponse),
	)
}

const codegenSystemPrompt = `You are a careful Go programmer making code edits.
You will receive a file and an instruction. Return ONLY the modified file
content inside a fenced code block, followed by a one-line explanation of
what you changed.

Format your response EXACTLY like:

` + "```" + `
<modified file content here>
` + "```" + `

EXPLANATION: <one sentence summary of the change>

Do not add commentary outside this format. Preserve original formatting,
package declarations, and imports unless the instruction explicitly says
to change them.`

func formatUserPrompt(req codegen.TransformReq) string {
	return fmt.Sprintf(
		"File: %s\n\nInstruction: %s\n\nCurrent content:\n```\n%s\n```",
		req.File.RelPath, req.Prompt, req.File.Content,
	)
}

// parseClaudeResponse extracts the new file content and explanation from
// Claude's response. Robust to minor format variations.
func parseClaudeResponse(r llm.Response) codegen.TransformResp {
	text := r.Text()

	// Pull out the fenced code block.
	codeBlock := extractCodeBlock(text)
	if codeBlock == "" {
		// Fall back to using the whole response — better than failing.
		codeBlock = strings.TrimSpace(text)
	}

	// Pull out the explanation, if present.
	explanation := extractExplanation(text)
	if explanation == "" {
		explanation = "(no explanation provided)"
	}

	return codegen.TransformResp{
		NewContent:  codeBlock,
		Explanation: explanation,
	}
}

// codeBlockRE matches ```optional-lang\n...\n```
var codeBlockRE = regexp.MustCompile("(?s)```(?:[a-zA-Z0-9]*\\n)?(.*?)```")

func extractCodeBlock(text string) string {
	m := codeBlockRE.FindStringSubmatch(text)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
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

// === Stub transformer for offline use ======================================

func stubTransformer() codegen.Transformer {
	return weft.ArrowFunc(func(_ context.Context, req codegen.TransformReq) (codegen.TransformResp, error) {
		// Just prepend a comment marker; useful for verifying the pipeline
		// runs end-to-end without hitting the API.
		newContent := fmt.Sprintf("// TODO(weft-codegen): %s\n%s",
			strings.TrimSpace(req.Prompt), req.File.Content)
		return codegen.TransformResp{
			NewContent:  newContent,
			Explanation: "stub: prepended TODO comment",
		}, nil
	})
}

// === Output ================================================================

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func report(results []codegen.FileResult, dryRun bool) {
	wrote, unchanged, failed, dryChanges := 0, 0, 0, 0
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
		case dryRun && r.Diff != "":
			dryChanges++
			fmt.Printf("~ %s — would change (%d bytes diff)\n", r.RelPath, len(r.Diff))
		}
	}
	fmt.Println()
	if dryRun {
		fmt.Printf("DRY RUN: %d files (%d would change, %d unchanged, %d failed)\n",
			len(results), dryChanges, unchanged, failed)
	} else {
		fmt.Printf("DONE: %d files (%d wrote, %d unchanged, %d failed)\n",
			len(results), wrote, unchanged, failed)
	}
	if failed > 0 {
		os.Exit(2)
	}
}
