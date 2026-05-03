// Command mcp-roundtrip demonstrates the categorical core of the mcp
// package: lift a typed weft.Arrow out as an MCP tool, lift it back in
// as an arrow, and verify the composition story holds end-to-end.
//
// This example uses the InMemory transport so it runs without any
// external setup and without an API key — it's purely about the type
// machinery and JSON round-trip. Once a real stdio/HTTP transport is
// added to the mcp package, the same code (modulo two lines for
// transport setup) will work against actual MCP servers.
//
// Usage:
//
//	go run ./cmd/examples/mcp-roundtrip
//
// What this example demonstrates:
//
//   - mcp.ServeAsTool turns any weft.Arrow[In, Out] into an MCP tool
//     entry. The original arrow is unchanged and still usable.
//   - mcp.Serve bundles entries into a server that dispatches by name.
//   - mcp.Connect + mcp.Tool[In, Out] lifts a remote tool back into a
//     typed Go arrow that composes with everything else in weft.
//   - The round-trip is observationally transparent: an arrow lifted
//     out and back behaves the same as the original. The mid-pipeline
//     stages don't know whether they're talking to an in-process arrow
//     or a remote MCP tool.
package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/vinodhalaharvi/weft/mcp"
	"github.com/vinodhalaharvi/weft/weft"
)

// === Domain types ============================================================

type Greeting struct {
	Name string `json:"name"`
}
type Greeted struct {
	Message string `json:"message"`
}
type Shouted struct {
	Message string `json:"message"`
}

// === The "real" arrows that will be exposed via MCP =========================

// greet is a typed weft.Arrow. In a real system this might be backed by
// a database, an LLM, an MCP tool from another server, anything. The
// rest of this example doesn't care what's behind it.
var greet = weft.ArrowFunc(func(_ context.Context, g Greeting) (Greeted, error) {
	return Greeted{Message: "Hello, " + g.Name + "!"}, nil
})

// shout is another arrow we'll compose with greet.
var shout = weft.ArrowFunc(func(_ context.Context, g Greeted) (Shouted, error) {
	return Shouted{Message: strings.ToUpper(g.Message)}, nil
})

func main() {
	ctx := context.Background()

	// === Step 1: lift arrows OUT as MCP tools ==============================
	fmt.Println("Step 1: Exposing arrows as MCP tools")
	fmt.Println(strings.Repeat("─", 60))

	server := mcp.Serve(
		mcp.ServeAsTool("greet", greet,
			mcp.WithDescription("Produce a greeting for the given name")),
		mcp.ServeAsTool("shout", shout,
			mcp.WithDescription("Convert a greeting to UPPERCASE")),
	)

	transport := mcp.InMemory(server)

	// === Step 2: lift them BACK IN as typed arrows =========================
	fmt.Println("\nStep 2: Connecting back and lifting tools as typed arrows")
	fmt.Println(strings.Repeat("─", 60))

	client, err := mcp.Connect(ctx, transport)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer client.Close()

	fmt.Printf("Server publishes %d tools: %v\n", len(client.Tools()), client.Tools())

	greetRT := mcp.Tool[Greeting, Greeted](client, "greet")
	shoutRT := mcp.Tool[Greeted, Shouted](client, "shout")

	// === Step 3: call the round-tripped arrows ============================
	fmt.Println("\nStep 3: Calling each arrow individually")
	fmt.Println(strings.Repeat("─", 60))

	g1, _ := greetRT(ctx, Greeting{Name: "Vinod"})
	fmt.Printf("greet(Vinod):  %+v\n", g1)

	s1, _ := shoutRT(ctx, g1)
	fmt.Printf("shout(...):    %+v\n", s1)

	// === Step 4: compose them with weft, exactly like in-process arrows ===
	fmt.Println("\nStep 4: Composing the round-tripped arrows with weft.Pipe2")
	fmt.Println(strings.Repeat("─", 60))

	roundTrippedPipeline := weft.Pipe2(greetRT, shoutRT)

	for _, name := range []string{"World", "Vinod", "Category Theory"} {
		out, err := roundTrippedPipeline(ctx, Greeting{Name: name})
		if err != nil {
			fmt.Printf("  %-20s → ERROR: %v\n", name, err)
			continue
		}
		fmt.Printf("  %-20s → %s\n", name, out.Message)
	}

	// === Step 5: verify the round-trip preserves composition ==============
	// This is the categorical claim. The original chain `greet ∘ shout`
	// and the round-tripped chain should produce identical results for
	// any input. If they didn't, the lift-out / lift-in pair wouldn't
	// be a functor and the framework's promise would be broken.
	fmt.Println("\nStep 5: Verifying round-trip preserves composition")
	fmt.Println(strings.Repeat("─", 60))

	originalPipeline := weft.Pipe2(greet, shout)

	mismatches := 0
	for _, name := range []string{"a", "World", "Vinod", "with spaces", "ñoño"} {
		want, _ := originalPipeline(ctx, Greeting{Name: name})
		got, _ := roundTrippedPipeline(ctx, Greeting{Name: name})
		match := want == got
		if !match {
			mismatches++
		}
		fmt.Printf("  %-15s  original=%-30s  roundtripped=%-30s  %s\n",
			name, fmt.Sprintf("%q", want.Message),
			fmt.Sprintf("%q", got.Message),
			tickOrCross(match))
	}
	fmt.Println()
	if mismatches == 0 {
		fmt.Println("✓ All inputs produced identical results — the round-trip is a functor.")
	} else {
		fmt.Printf("✗ %d mismatches — the round-trip is broken.\n", mismatches)
	}

	// === Step 6: compose round-tripped arrow with anything else ===========
	// The whole point: an MCP-backed arrow is just an arrow. It composes
	// with weft.Traverse, weft.Apply, weft.Par, anything. No special API
	// for "MCP-aware composition" is needed.
	fmt.Println("\nStep 6: Running round-tripped arrows through weft.Traverse")
	fmt.Println(strings.Repeat("─", 60))

	names := []Greeting{
		{Name: "Alice"}, {Name: "Bob"}, {Name: "Charlie"},
		{Name: "Diana"}, {Name: "Eve"}, {Name: "Frank"},
	}
	traverse := weft.Traverse(roundTrippedPipeline,
		weft.WithConcurrency(3),
	)
	results, err := traverse(ctx, names)
	if err != nil {
		log.Fatalf("traverse: %v", err)
	}
	for _, r := range results {
		fmt.Printf("  %s\n", r.Message)
	}
}

func tickOrCross(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}
