package codegen

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vinodhalaharvi/weft/weft"
)

// === Test helpers =============================================================

// makeTempProject creates a temporary directory with the given file tree.
// Returns the dir path; t.Cleanup removes it after the test.
func makeTempProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "weft-codegen-test-*")
	if err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	for relPath, content := range files {
		full := filepath.Join(dir, relPath)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	return dir
}

// stubTransformer returns a deterministic transformer for tests. It uppercases
// the file content, prepending a marker line. Replace this with llm.Claude(...)
// in production.
func stubTransformer() Transformer {
	return weft.ArrowFunc(func(_ context.Context, req TransformReq) (TransformResp, error) {
		return TransformResp{
			NewContent: fmt.Sprintf("// transformed by: %s\n%s",
				strings.TrimSpace(req.Prompt), strings.ToUpper(req.File.Content)),
			Explanation: "uppercased file with prompt header",
		}, nil
	})
}

// failingTransformer always returns an error — for testing error policies.
func failingTransformer(failOn string) Transformer {
	return weft.ArrowFunc(func(_ context.Context, req TransformReq) (TransformResp, error) {
		if strings.Contains(req.File.RelPath, failOn) {
			return TransformResp{}, fmt.Errorf("simulated failure on %s", req.File.RelPath)
		}
		return TransformResp{
			NewContent:  req.File.Content + "\n// touched",
			Explanation: "appended marker",
		}, nil
	})
}

// === Enumerate tests ==========================================================

func TestEnumerate_FindsAllFiles(t *testing.T) {
	dir := makeTempProject(t, map[string]string{
		"main.go":              "package main",
		"util/helpers.go":      "package util",
		"util/helpers_test.go": "package util",
		"README.md":            "# project",
	})

	files, err := Enumerate()(context.Background(), Job{
		Dir:      dir,
		Patterns: []string{"**/*.go"},
	})
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	if len(files) != 3 {
		t.Errorf("got %d files, want 3: %v", len(files), filesPaths(files))
	}
	for _, f := range files {
		if !strings.HasSuffix(f.RelPath, ".go") {
			t.Errorf("non-go file slipped through: %s", f.RelPath)
		}
	}
}

func TestEnumerate_RespectsSkip(t *testing.T) {
	dir := makeTempProject(t, map[string]string{
		"main.go":           "package main",
		"vendor/dep/dep.go": "package dep",
		"util/helpers.go":   "package util",
	})

	files, err := Enumerate()(context.Background(), Job{
		Dir:      dir,
		Patterns: []string{"**/*.go"},
		Skip:     []string{"vendor/**"},
	})
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	for _, f := range files {
		if strings.HasPrefix(f.RelPath, "vendor/") {
			t.Errorf("vendor file should have been skipped: %s", f.RelPath)
		}
	}
	if len(files) != 2 {
		t.Errorf("got %d files, want 2: %v", len(files), filesPaths(files))
	}
}

func TestEnumerate_RejectsNonexistentDir(t *testing.T) {
	_, err := Enumerate()(context.Background(), Job{
		Dir: "/nonexistent/path/that/should/not/exist",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent directory")
	}
}

func TestEnumerate_StableOrder(t *testing.T) {
	dir := makeTempProject(t, map[string]string{
		"z.go": "z", "a.go": "a", "m.go": "m",
	})

	for i := 0; i < 5; i++ {
		files, err := Enumerate()(context.Background(), Job{
			Dir:      dir,
			Patterns: []string{"*.go"},
		})
		if err != nil {
			t.Fatalf("enumerate iter %d: %v", i, err)
		}
		if files[0].RelPath != "a.go" || files[1].RelPath != "m.go" || files[2].RelPath != "z.go" {
			t.Errorf("iter %d: unstable order: %v", i, filesPaths(files))
		}
	}
}

// === WriteOrDiff tests ========================================================

func TestWriteOrDiff_DryRunDoesNotWrite(t *testing.T) {
	dir := makeTempProject(t, map[string]string{
		"foo.txt": "original",
	})
	original, _ := os.ReadFile(filepath.Join(dir, "foo.txt"))

	edit := Edit{
		File: File{
			Path:    filepath.Join(dir, "foo.txt"),
			RelPath: "foo.txt",
			Content: string(original),
		},
		NewContent:  "modified",
		Explanation: "test",
	}

	result, err := WriteOrDiff(true)(context.Background(), edit)
	if err != nil {
		t.Fatalf("WriteOrDiff: %v", err)
	}
	if result.Wrote {
		t.Error("dry-run should not write")
	}
	if result.Diff == "" {
		t.Error("dry-run should produce a diff")
	}
	current, _ := os.ReadFile(filepath.Join(dir, "foo.txt"))
	if string(current) != "original" {
		t.Errorf("file modified during dry-run: %q", current)
	}
}

func TestWriteOrDiff_RealRunWrites(t *testing.T) {
	dir := makeTempProject(t, map[string]string{
		"foo.txt": "original",
	})
	path := filepath.Join(dir, "foo.txt")

	edit := Edit{
		File:       File{Path: path, RelPath: "foo.txt", Content: "original"},
		NewContent: "modified",
	}

	result, err := WriteOrDiff(false)(context.Background(), edit)
	if err != nil {
		t.Fatalf("WriteOrDiff: %v", err)
	}
	if !result.Wrote {
		t.Error("expected Wrote=true")
	}
	current, _ := os.ReadFile(path)
	if string(current) != "modified" {
		t.Errorf("file content: got %q, want %q", current, "modified")
	}
}

func TestWriteOrDiff_UnchangedSkipsWrite(t *testing.T) {
	dir := makeTempProject(t, map[string]string{
		"foo.txt": "same",
	})
	path := filepath.Join(dir, "foo.txt")

	// Get the inode/mtime before; if Unchanged works, mtime won't change.
	info1, _ := os.Stat(path)

	edit := Edit{
		File:       File{Path: path, RelPath: "foo.txt", Content: "same"},
		NewContent: "same",
	}

	result, err := WriteOrDiff(false)(context.Background(), edit)
	if err != nil {
		t.Fatalf("WriteOrDiff: %v", err)
	}
	if !result.Unchanged {
		t.Error("expected Unchanged=true")
	}
	if result.Wrote {
		t.Error("should not write when content unchanged")
	}
	info2, _ := os.Stat(path)
	if info1.ModTime() != info2.ModTime() {
		t.Error("file modtime changed despite Unchanged result")
	}
}

func TestWriteOrDiff_AtomicOnFailure(t *testing.T) {
	// Try to write to a directory that doesn't exist — atomic write
	// should leave no partial files behind.
	edit := Edit{
		File: File{
			Path:    "/nonexistent-dir/foo.txt",
			RelPath: "foo.txt",
			Content: "old",
		},
		NewContent: "new",
	}
	_, err := WriteOrDiff(false)(context.Background(), edit)
	if err == nil {
		t.Fatal("expected error writing to nonexistent dir")
	}
	// No way to check "no temp file left behind" portably, but the error
	// path going through our code is exercised.
}

// === End-to-end pipeline tests ================================================

func TestPipeline_DryRun(t *testing.T) {
	dir := makeTempProject(t, map[string]string{
		"a.go": "package a",
		"b.go": "package b",
		"c.md": "# notes",
	})

	pipeline := Pipeline(stubTransformer(), 2, weft.FailFast)
	results, err := pipeline(context.Background(), Job{
		Dir:      dir,
		Patterns: []string{"*.go"},
		Prompt:   "uppercase everything",
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for _, r := range results {
		if r.Wrote {
			t.Errorf("dry-run wrote %s", r.RelPath)
		}
		if r.Diff == "" {
			t.Errorf("expected diff for %s", r.RelPath)
		}
	}
	// Verify files weren't actually modified.
	a, _ := os.ReadFile(filepath.Join(dir, "a.go"))
	if string(a) != "package a" {
		t.Errorf("file was modified during dry-run: %q", a)
	}
}

func TestPipeline_RealRun(t *testing.T) {
	dir := makeTempProject(t, map[string]string{
		"a.go":            "package a",
		"b.go":            "package b",
		"util/helpers.go": "package util",
	})

	pipeline := Pipeline(stubTransformer(), 3, weft.FailFast)
	results, err := pipeline(context.Background(), Job{
		Dir:      dir,
		Patterns: []string{"**/*.go"},
		Prompt:   "uppercase",
		DryRun:   false,
	})
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	written := 0
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("%s: error %v", r.RelPath, r.Err)
		}
		if r.Wrote {
			written++
		}
	}
	if written != 3 {
		t.Errorf("wrote %d files, want 3", written)
	}

	// Verify the actual content was modified.
	a, _ := os.ReadFile(filepath.Join(dir, "a.go"))
	if !strings.Contains(string(a), "PACKAGE A") {
		t.Errorf("a.go was not transformed: %q", a)
	}
	if !strings.Contains(string(a), "transformed by") {
		t.Errorf("a.go missing prompt marker: %q", a)
	}
}

func TestPipeline_PartialResultsOnFailure(t *testing.T) {
	dir := makeTempProject(t, map[string]string{
		"good1.go": "package good1",
		"bad.go":   "package bad",
		"good2.go": "package good2",
	})

	pipeline := Pipeline(failingTransformer("bad"), 2, weft.PartialResults)
	results, err := pipeline(context.Background(), Job{
		Dir:      dir,
		Patterns: []string{"*.go"},
		Prompt:   "touch",
		DryRun:   false,
	})
	if err != nil {
		t.Fatalf("pipeline returned error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}

	// Find which result is bad.go and verify its error.
	var badResult, goodResults int
	for _, r := range results {
		if r.RelPath == "bad.go" {
			badResult++
			if r.Err == nil {
				t.Error("bad.go should have an error")
			}
			if r.Wrote {
				t.Error("bad.go should not have been written")
			}
		} else {
			goodResults++
			if r.Err != nil {
				t.Errorf("%s: unexpected error %v", r.RelPath, r.Err)
			}
		}
	}
	if badResult != 1 || goodResults != 2 {
		t.Errorf("got %d bad and %d good, want 1 and 2", badResult, goodResults)
	}

	// good files should have actually been written
	g1, _ := os.ReadFile(filepath.Join(dir, "good1.go"))
	if !strings.Contains(string(g1), "// touched") {
		t.Errorf("good1.go not written: %q", g1)
	}
	// bad file should NOT have been modified
	b, _ := os.ReadFile(filepath.Join(dir, "bad.go"))
	if string(b) != "package bad" {
		t.Errorf("bad.go was modified: %q", b)
	}
}

func TestPipeline_ComposesWithTransforms(t *testing.T) {
	// Demonstrate that the pipeline is just an arrow — it composes
	// with the rest of the weft vocabulary.
	dir := makeTempProject(t, map[string]string{
		"a.go": "package a",
	})

	base := Pipeline(stubTransformer(), 1, weft.FailFast)
	// Wrap with a tap to observe it ran (proves composition works).
	called := false
	wrapped := weft.WithTap[Job, []FileResult](func(in Job, out []FileResult, err error) {
		called = true
	})(base)

	_, err := wrapped(context.Background(), Job{
		Dir:      dir,
		Patterns: []string{"*.go"},
		Prompt:   "x",
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("wrapped pipeline: %v", err)
	}
	if !called {
		t.Error("tap was not invoked")
	}
}

func TestPipeline_Cancellation(t *testing.T) {
	dir := makeTempProject(t, map[string]string{
		"a.go": "package a", "b.go": "package b", "c.go": "package c",
	})
	// Slow transformer so we can cancel mid-flight.
	slowTransformer := weft.ArrowFunc(func(ctx context.Context, req TransformReq) (TransformResp, error) {
		select {
		case <-ctx.Done():
			return TransformResp{}, ctx.Err()
		}
	})

	pipeline := Pipeline(slowTransformer, 1, weft.FailFast)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := pipeline(ctx, Job{
		Dir:      dir,
		Patterns: []string{"*.go"},
		Prompt:   "x",
	})
	if !errors.Is(err, context.Canceled) {
		// pipeline might wrap the cancellation; check loosely
		if err == nil || !strings.Contains(err.Error(), "canceled") {
			t.Errorf("expected cancellation error, got %v", err)
		}
	}
}

// === Helpers ==================================================================

func filesPaths(files []File) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.RelPath
	}
	return out
}
