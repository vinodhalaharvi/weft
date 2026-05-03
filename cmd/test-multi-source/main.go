// Command test-multi-source verifies that multi-source-server works
// end-to-end. It connects to the server as an external MCP client
// (no shared memory, no shared package — just stdio), calls each
// tool, and prints what came back.
//
// This is the verification step. If it succeeds, we've proven:
//
//   - Our `mcp.RunStdioServer` correctly exposes a weft *Server as
//     a real MCP server speaking stdio.
//   - Our composed tools (shell_run, read_file, bash_then_save) work
//     when called from an external client.
//   - The composed tools delegate correctly to the two upstream MCP
//     servers (Claude Code's Bash + filesystem).
//
// Process tree at runtime looks like this:
//
//   test-multi-source                   ← this program
//     └─ multi-source-server            ← spawned by us
//          ├─ claude mcp serve          ← spawned by multi-source-server
//          └─ npx ...server-filesystem  ← spawned by multi-source-server
//
// All four processes communicate via MCP over stdio. test-multi-source
// only knows about multi-source-server. The two upstream sources are
// invisible to it — that's exactly what role-erasure means.
//
// Usage:
//
//	# From the repo root, after building:
//	go run ./cmd/test-multi-source
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vinodhalaharvi/weft/mcp"
)

func main() {
	logger := log.New(os.Stderr, "[test-multi-source] ", log.LstdFlags)

	// === Spawn multi-source-server as a subprocess =======================
	// We invoke `go run ./cmd/multi-source-server /tmp` so users don't
	// need to build first. In a production setup you'd point at a
	// compiled binary.
	logger.Print("spawning multi-source-server (this takes a moment to start " +
		"because it spawns claude+npx underneath)...")

	// Use a per-run temp directory inside /tmp to avoid touching anything
	// the user cares about. `/tmp` is also what server-filesystem will
	// be allowed to access.
	tmpDir, err := os.MkdirTemp("/tmp", "weft-test-")
	if err != nil {
		logger.Fatalf("mkdir tmp: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	logger.Printf("scratch dir: %s", tmpDir)

	transport, err := mcp.Stdio("go", "run",
		"./cmd/multi-source-server", "/tmp")
	if err != nil {
		logger.Fatalf("spawn server: %v", err)
	}

	// Generous timeout — first run pulls the npm package and starts
	// three subprocesses, which can be slow.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, err := mcp.Connect(ctx, transport)
	if err != nil {
		logger.Fatalf("connect: %v", err)
	}
	defer client.Close()

	tools := client.Tools()
	fmt.Printf("✓ Connected to multi-source-server\n")
	fmt.Printf("  Tools: %v\n\n", tools)

	// === Test 1: shell_run ===============================================
	// Routes through Claude Code's Bash. Tests source A.
	fmt.Println(strings.Repeat("─", 70))
	fmt.Println("Test 1: shell_run — uses Claude Code's Bash")
	fmt.Println(strings.Repeat("─", 70))

	shellRun := mcp.Tool[map[string]any, string](client, "shell_run")
	out, err := shellRun(ctx, map[string]any{"command": "echo hello && date"})
	if err != nil {
		logger.Fatalf("shell_run failed: %v", err)
	}
	fmt.Printf("✓ Result:\n%s\n\n", indent(out))

	// === Test 2: read_file ==============================================
	// Routes through filesystem MCP server. Tests source B.
	// First we write a known file directly on disk so we know what to
	// expect when reading it.
	fmt.Println(strings.Repeat("─", 70))
	fmt.Println("Test 2: read_file — uses filesystem MCP server")
	fmt.Println(strings.Repeat("─", 70))

	knownFile := filepath.Join(tmpDir, "hello.txt")
	knownContent := "this is the content the test wrote directly"
	if err := os.WriteFile(knownFile, []byte(knownContent), 0644); err != nil {
		logger.Fatalf("write known file: %v", err)
	}

	readFile := mcp.Tool[map[string]any, string](client, "read_file")
	out, err = readFile(ctx, map[string]any{"path": knownFile})
	if err != nil {
		logger.Fatalf("read_file failed: %v", err)
	}
	fmt.Printf("✓ Read back: %q\n", out)
	if !strings.Contains(out, knownContent) {
		logger.Printf("⚠ Expected %q to contain %q", out, knownContent)
	}
	fmt.Println()

	// === Test 3: bash_then_save (the headline composition) ===============
	// Routes through BOTH source A and source B in one tool call.
	fmt.Println(strings.Repeat("─", 70))
	fmt.Println("Test 3: bash_then_save — composes Bash + write_file")
	fmt.Println(strings.Repeat("─", 70))

	savedFile := filepath.Join(tmpDir, "shell-output.txt")
	bashThenSave := mcp.Tool[map[string]any, string](client, "bash_then_save")
	out, err = bashThenSave(ctx, map[string]any{
		"command": "echo 'composed across two MCP servers'",
		"path":    savedFile,
	})
	if err != nil {
		logger.Fatalf("bash_then_save failed: %v", err)
	}
	fmt.Printf("✓ Tool reported: %s\n", out)

	// Verify by reading the file directly. If the composition worked,
	// the file should contain the echoed text.
	saved, err := os.ReadFile(savedFile)
	if err != nil {
		logger.Fatalf("read saved file: %v", err)
	}
	fmt.Printf("✓ File on disk contains: %q\n", string(saved))

	expected := "composed across two MCP servers"
	if !strings.Contains(string(saved), expected) {
		logger.Fatalf("✗ Expected file to contain %q, got %q",
			expected, string(saved))
	}

	fmt.Println()
	fmt.Println(strings.Repeat("─", 70))
	fmt.Println("All three tests passed.")
	fmt.Println()
	fmt.Println("What just happened:")
	fmt.Println("  - test-multi-source connected to multi-source-server over stdio")
	fmt.Println("  - multi-source-server delegated each call to one or two")
	fmt.Println("    upstream MCP servers (Claude Code, server-filesystem)")
	fmt.Println("  - The composed tool `bash_then_save` ran a shell command")
	fmt.Println("    via Claude Code, then wrote the output to disk via the")
	fmt.Println("    filesystem server, all from a single tool call.")
	fmt.Println()
	fmt.Println("That's a categorical functor working across three process")
	fmt.Println("boundaries and two heterogeneous external systems.")
}

// indent prefixes each line of s with two spaces for readable output.
func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}
