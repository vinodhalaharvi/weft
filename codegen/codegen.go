// Package codegen applies an LLM-style transformation across a directory
// of files using the weft composition vocabulary.
//
// The central idea: given a directory, a glob pattern, and a prompt,
// build a weft.Arrow[Job, []FileResult] that reads each matching file,
// runs it through a transformer arrow (typically backed by an LLM), and
// writes the result back. The whole thing is a pipeline composed of
// smaller arrows, so you can swap, wrap, or replace any stage:
//
//   - swap the transformer (Claude, GPT, a deterministic stub for tests)
//   - wrap with retry, timeout, token budgets via weft.Apply
//   - choose error policy (fail-fast / partial / collect)
//   - choose context mode (independent / each file sees neighbor summaries)
//   - choose write mode (dry-run produces diffs / real mode writes atomically)
//
// Because each stage is a typed arrow, the type system enforces correctness
// across the entire pipeline. There is no "config schema" — the configuration
// is the function arguments.
package codegen

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vinodhalaharvi/weft/weft"
)

// === Domain types ============================================================

// Job is the top-level input: what to transform and how.
type Job struct {
	Dir      string   // root directory to process
	Patterns []string // glob patterns, e.g. []string{"*.go", "**/*.md"}
	Skip     []string // glob patterns to exclude, e.g. []string{"vendor/**"}
	Prompt   string   // the instruction applied to each file
	DryRun   bool     // if true, produce diffs without writing
}

// File is a single file picked up by enumeration.
type File struct {
	Path    string // absolute path
	RelPath string // path relative to Job.Dir, for display
	Content string
}

// Edit is the result of applying the transformer to a single file.
type Edit struct {
	File        File
	NewContent  string
	Explanation string // why the change was made (from the LLM)
}

// FileResult is the final outcome per file: either a successful write,
// a dry-run diff, or a recorded error.
type FileResult struct {
	Path        string
	RelPath     string
	Wrote       bool   // true if file was written
	Unchanged   bool   // true if NewContent matched original (no write needed)
	Diff        string // populated for dry-run or for logging
	Explanation string
	Err         error
}

// Transformer is the LLM-shaped arrow that turns a file + prompt into an edit.
// In real use, you'd construct this with llm.Claude(...) wrapped in formatting
// arrows. Tests can use a deterministic stub. The codegen package only cares
// about the shape.
type Transformer = weft.Arrow[TransformReq, TransformResp]

type TransformReq struct {
	File   File
	Prompt string
}

type TransformResp struct {
	NewContent  string
	Explanation string
}

// === Stage 1: enumerate files ================================================

// Enumerate walks a directory and returns matching files.
// Implemented as a weft.Arrow so it composes with the rest of the pipeline.
func Enumerate() weft.Arrow[Job, []File] {
	return weft.ArrowFunc(func(ctx context.Context, job Job) ([]File, error) {
		if job.Dir == "" {
			return nil, fmt.Errorf("codegen: Job.Dir is required")
		}
		absDir, err := filepath.Abs(job.Dir)
		if err != nil {
			return nil, fmt.Errorf("codegen: resolve dir: %w", err)
		}
		info, err := os.Stat(absDir)
		if err != nil {
			return nil, fmt.Errorf("codegen: stat dir: %w", err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("codegen: %s is not a directory", absDir)
		}
		patterns := job.Patterns
		if len(patterns) == 0 {
			patterns = []string{"**/*"}
		}

		var files []File
		err = filepath.Walk(absDir, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if info.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(absDir, path)
			if err != nil {
				return err
			}
			if !matchesAny(rel, patterns) {
				return nil
			}
			if matchesAny(rel, job.Skip) {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read %s: %w", rel, err)
			}
			files = append(files, File{
				Path:    path,
				RelPath: rel,
				Content: string(content),
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
		// Stable order so reruns are deterministic.
		sort.Slice(files, func(i, j int) bool {
			return files[i].RelPath < files[j].RelPath
		})
		return files, nil
	})
}

// matchesAny returns true if path matches any of the glob patterns.
// Supports a basic ** for recursive matching by treating ** as "any path".
func matchesAny(path string, patterns []string) bool {
	for _, p := range patterns {
		if matched, _ := matchGlob(p, path); matched {
			return true
		}
	}
	return false
}

// matchGlob is a small extension of filepath.Match that handles a single
// "**" segment as "match any number of directories." Sufficient for the
// common cases ("**/*.go", "vendor/**") without pulling in a full glob lib.
func matchGlob(pattern, path string) (bool, error) {
	// Fast path: no ** — use stdlib.
	if !strings.Contains(pattern, "**") {
		return filepath.Match(pattern, path)
	}
	// Split on **; check each side.
	parts := strings.Split(pattern, "**")
	if len(parts) != 2 {
		return false, fmt.Errorf("at most one ** allowed in pattern: %s", pattern)
	}
	prefix := strings.TrimSuffix(parts[0], "/")
	suffix := strings.TrimPrefix(parts[1], "/")

	// Path must start with prefix (if any).
	if prefix != "" && !strings.HasPrefix(path, prefix+"/") && path != prefix {
		// Allow prefix to be a glob itself.
		if ok, _ := filepath.Match(prefix, firstSegment(path)); !ok {
			return false, nil
		}
	}
	// Path must end with a segment matching suffix.
	if suffix == "" {
		return true, nil
	}
	// Try suffix against every tail of the path.
	segments := strings.Split(path, "/")
	for i := 0; i < len(segments); i++ {
		tail := strings.Join(segments[i:], "/")
		if ok, _ := filepath.Match(suffix, tail); ok {
			return true, nil
		}
		if ok, _ := filepath.Match(suffix, segments[len(segments)-1]); ok {
			return true, nil
		}
	}
	return false, nil
}

func firstSegment(path string) string {
	if i := strings.Index(path, "/"); i > 0 {
		return path[:i]
	}
	return path
}

// === Stage 2: apply the transformer to each file =============================

// Apply takes a transformer and a prompt, and produces an arrow that
// transforms a single File into an Edit. This is the per-file unit that
// gets parallelized by Traverse.
func Apply(transformer Transformer, prompt string) weft.Arrow[File, Edit] {
	return weft.ArrowFunc(func(ctx context.Context, f File) (Edit, error) {
		resp, err := transformer(ctx, TransformReq{File: f, Prompt: prompt})
		if err != nil {
			return Edit{}, err
		}
		return Edit{
			File:        f,
			NewContent:  resp.NewContent,
			Explanation: resp.Explanation,
		}, nil
	})
}

// === Stage 3: write or diff ==================================================

// WriteOrDiff produces a FileResult per Edit. In dry-run mode it computes
// a unified diff string. In real mode it writes the new content atomically
// (write to temp, rename) so a crash doesn't corrupt the file.
func WriteOrDiff(dryRun bool) weft.Arrow[Edit, FileResult] {
	return weft.ArrowFunc(func(_ context.Context, e Edit) (FileResult, error) {
		result := FileResult{
			Path:        e.File.Path,
			RelPath:     e.File.RelPath,
			Explanation: e.Explanation,
		}

		if e.NewContent == e.File.Content {
			result.Unchanged = true
			return result, nil
		}

		result.Diff = unifiedDiff(e.File.RelPath, e.File.Content, e.NewContent)

		if dryRun {
			return result, nil
		}

		if err := writeAtomic(e.File.Path, []byte(e.NewContent)); err != nil {
			result.Err = err
			return result, err
		}
		result.Wrote = true
		return result, nil
	})
}

// writeAtomic writes data to path via a temp file + rename, so a crash
// mid-write doesn't leave the original truncated.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".weft-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	// Preserve the original file's mode if it exists.
	if info, err := os.Stat(path); err == nil {
		_ = os.Chmod(tmpName, info.Mode())
	}
	return os.Rename(tmpName, path)
}

// unifiedDiff produces a small unified-diff-like string. Keeping it
// minimal and dependency-free; real diff libraries are a one-line
// import away if needed.
func unifiedDiff(name, old, new string) string {
	if old == new {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", name, name)
	oldLines := strings.Split(old, "\n")
	newLines := strings.Split(new, "\n")
	// Naive line-level diff: mark removed lines from old, added from new.
	// Good enough for human review; replace with a real diff if needed.
	for _, l := range oldLines {
		fmt.Fprintf(&b, "-%s\n", l)
	}
	for _, l := range newLines {
		fmt.Fprintf(&b, "+%s\n", l)
	}
	return b.String()
}

// === The whole pipeline as one arrow =========================================

// Pipeline assembles the full directory-wide code-generation pipeline from
// the three stages, parameterized by:
//   - the transformer (typically an LLM-backed arrow)
//   - the per-item concurrency
//   - the error policy
//
// The result is a weft.Arrow[Job, []FileResult] you can wrap with retries,
// timeouts, observability, or further composition.
//
// This is the function most callers will use directly.
func Pipeline(
	transformer Transformer,
	concurrency int,
	policy weft.ErrorPolicy,
) weft.Arrow[Job, []FileResult] {
	return weft.ArrowFunc(func(ctx context.Context, job Job) ([]FileResult, error) {
		// Stage 1: enumerate files.
		files, err := Enumerate()(ctx, job)
		if err != nil {
			return nil, fmt.Errorf("enumerate: %w", err)
		}
		if len(files) == 0 {
			return nil, nil
		}

		// Per-file pipeline: Apply -> WriteOrDiff, but wrapped so that
		// any failure produces a FileResult with the path filled in
		// rather than a zero-value result. This way, when policy is
		// PartialResults, the caller still gets per-file context for
		// failures, not just an opaque map of indices.
		apply := Apply(transformer, job.Prompt)
		write := WriteOrDiff(job.DryRun)

		perFile := weft.ArrowFunc(func(ctx context.Context, f File) (FileResult, error) {
			edit, err := apply(ctx, f)
			if err != nil {
				// Produce a FileResult with path info preserved, plus
				// the error. Return the error too so policies that care
				// (FailFast, CollectErrors) see it.
				return FileResult{
					Path:    f.Path,
					RelPath: f.RelPath,
					Err:     err,
				}, err
			}
			result, err := write(ctx, edit)
			if err != nil {
				// write already populated Path/RelPath/Err in the result.
				return result, err
			}
			return result, nil
		})

		// Traverse all files with bounded concurrency.
		results, err := weft.Traverse(perFile,
			weft.WithConcurrency(concurrency),
			weft.OnError(policy),
		)(ctx, files)

		// For PartialResults, weft.Traverse returns a *PartialError but
		// also fills `results` with our FileResult-with-Err values at
		// failed indices (because perFile returns both result AND error).
		// We hide the PartialError from the caller because per-result
		// errors are already accessible via FileResult.Err — the caller
		// shouldn't need to type-assert.
		if _, ok := err.(*weft.PartialError); ok {
			return results, nil
		}
		if err != nil {
			return results, err
		}
		return results, nil
	})
}
