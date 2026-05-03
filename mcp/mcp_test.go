package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vinodhalaharvi/weft/mcp"
	"github.com/vinodhalaharvi/weft/weft"
)

// === Domain types for the tests ==============================================

type GreetIn struct {
	Name string `json:"name"`
}
type GreetOut struct {
	Message string `json:"message"`
}

type AddIn struct {
	A int `json:"a"`
	B int `json:"b"`
}
type AddOut struct {
	Sum int `json:"sum"`
}

// === Test helpers =============================================================

// greetArrow is a typed weft.Arrow used by several tests as a "real"
// in-process arrow that we then expose as MCP, lift back, and compose with.
var greetArrow = weft.ArrowFunc(func(_ context.Context, in GreetIn) (GreetOut, error) {
	return GreetOut{Message: "hello, " + in.Name}, nil
})

var addArrow = weft.ArrowFunc(func(_ context.Context, in AddIn) (AddOut, error) {
	return AddOut{Sum: in.A + in.B}, nil
})

var failingArrow = weft.ArrowFunc(func(_ context.Context, _ GreetIn) (GreetOut, error) {
	return GreetOut{}, errors.New("intentional failure")
})

// connectInProcess sets up a server with the given tools and returns a
// connected client, with cleanup registered on the test.
func connectInProcess(t *testing.T, tools ...mcp.ErasedTool) *mcp.Client {
	t.Helper()
	server := mcp.Serve(tools...)
	transport := mcp.InMemory(server)
	client, err := mcp.Connect(context.Background(), transport)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// === Server / dispatch tests =================================================

func TestServe_DispatchesToolsList(t *testing.T) {
	client := connectInProcess(t,
		mcp.ServeAsTool("greet", greetArrow, mcp.WithDescription("Say hello")),
		mcp.ServeAsTool("add", addArrow),
	)

	tools := client.Tools()
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2: %v", len(tools), tools)
	}
	names := map[string]bool{}
	for _, n := range tools {
		names[n] = true
	}
	if !names["greet"] || !names["add"] {
		t.Errorf("missing expected tools: %v", names)
	}
}

func TestServe_DuplicateNamePanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Error("expected panic on duplicate tool name")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "duplicate") {
			t.Errorf("panic message unclear: %v", r)
		}
	}()
	mcp.Serve(
		mcp.ServeAsTool("dup", greetArrow),
		mcp.ServeAsTool("dup", greetArrow),
	)
}

// === Round-trip: lift out, then lift in, observe identical behavior =========

func TestRoundtrip_SingleTool(t *testing.T) {
	// Original arrow we'll round-trip through MCP.
	original := greetArrow

	// Lift out as MCP, lift back in as Arrow[GreetIn, GreetOut].
	client := connectInProcess(t, mcp.ServeAsTool("greet", original))
	roundtripped := mcp.Tool[GreetIn, GreetOut](client, "greet")

	// The two arrows should produce identical results for any input.
	for _, name := range []string{"world", "vinod", "", "with spaces", "spëcial-chars"} {
		ctx := context.Background()
		want, wantErr := original(ctx, GreetIn{Name: name})
		got, gotErr := roundtripped(ctx, GreetIn{Name: name})

		if (wantErr == nil) != (gotErr == nil) {
			t.Errorf("error mismatch for %q: want err=%v got err=%v", name, wantErr, gotErr)
			continue
		}
		if want != got {
			t.Errorf("output mismatch for %q: want %+v got %+v", name, want, got)
		}
	}
}

// TestRoundtrip_PreservesComposition is the categorical crux: chaining
// two roundtripped arrows must equal chaining the originals.
//
// In categorical terms, this verifies that the lift-out / lift-in pair
// forms a functor that preserves composition:
//
//	lift(f) ∘ lift(g) ≡ lift(f ∘ g)
//
// Specifically, here we verify:
//
//	roundtrip(f) ∘ adapter ∘ roundtrip(g) behaves like f ∘ adapter ∘ g
//
// where adapter is the small bridge between f's output and g's input.
func TestRoundtrip_PreservesComposition(t *testing.T) {
	// Two arrows: greet produces GreetOut, then we adapt to AddIn,
	// then add. We compose the originals and the roundtripped versions
	// and verify they produce the same output.
	adapter := weft.Pure(func(g GreetOut) AddIn {
		return AddIn{A: len(g.Message), B: 1}
	})

	originalChain := weft.Pipe3(greetArrow, adapter, addArrow)

	client := connectInProcess(t,
		mcp.ServeAsTool("greet", greetArrow),
		mcp.ServeAsTool("add", addArrow),
	)
	roundtrippedChain := weft.Pipe3(
		mcp.Tool[GreetIn, GreetOut](client, "greet"),
		adapter,
		mcp.Tool[AddIn, AddOut](client, "add"),
	)

	for _, name := range []string{"a", "world", "category theory"} {
		ctx := context.Background()
		want, _ := originalChain(ctx, GreetIn{Name: name})
		got, _ := roundtrippedChain(ctx, GreetIn{Name: name})
		if want != got {
			t.Errorf("composition not preserved for %q: want %+v got %+v", name, want, got)
		}
	}
}

// === Error handling ==========================================================

func TestTool_ToolNotFoundIsPermanent(t *testing.T) {
	client := connectInProcess(t,
		mcp.ServeAsTool("greet", greetArrow),
	)

	ghost := mcp.Tool[GreetIn, GreetOut](client, "nonexistent")
	_, err := ghost(context.Background(), GreetIn{Name: "x"})
	if err == nil {
		t.Fatal("expected error for missing tool")
	}

	var ae *weft.ArrowError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *weft.ArrowError, got %T: %v", err, err)
	}
	if ae.Class != weft.ClassPermanent {
		t.Errorf("tool-not-found should be ClassPermanent, got %v", ae.Class)
	}
}

func TestTool_HandlerErrorPropagates(t *testing.T) {
	client := connectInProcess(t,
		mcp.ServeAsTool("fail", failingArrow),
	)

	tool := mcp.Tool[GreetIn, GreetOut](client, "fail")
	_, err := tool(context.Background(), GreetIn{Name: "x"})
	if err == nil {
		t.Fatal("expected error from failing handler")
	}
	if !strings.Contains(err.Error(), "intentional failure") {
		t.Errorf("error should mention the handler's message, got: %v", err)
	}
}

func TestTool_ContextCancellationPropagates(t *testing.T) {
	client := connectInProcess(t,
		mcp.ServeAsTool("greet", greetArrow),
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tool := mcp.Tool[GreetIn, GreetOut](client, "greet")
	_, err := tool(ctx, GreetIn{Name: "x"})
	if err == nil {
		t.Fatal("expected error after cancellation")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "canceled") {
		t.Errorf("expected cancellation error, got: %v", err)
	}
}

// === Composition with the rest of the framework =============================

func TestTool_ComposesWithWeftCombinators(t *testing.T) {
	// Demonstrate that an MCP-backed arrow is genuinely just an arrow
	// by feeding it through Traverse with bounded concurrency.
	client := connectInProcess(t,
		mcp.ServeAsTool("greet", greetArrow),
	)

	greet := mcp.Tool[GreetIn, GreetOut](client, "greet")

	// Wrap with a tap to verify composition with transforms.
	// The tap fires once per item from concurrent goroutines, so the
	// counter MUST be synchronized — atomic.Int32 is the simplest fit.
	// (Plain int++ here is a textbook race; the race detector flags it.)
	var calls atomic.Int32
	greet = weft.WithTap[GreetIn, GreetOut](func(_ GreetIn, _ GreetOut, _ error) {
		calls.Add(1)
	})(greet)

	// Run via Traverse over 10 items.
	inputs := []GreetIn{
		{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}, {Name: "e"},
		{Name: "f"}, {Name: "g"}, {Name: "h"}, {Name: "i"}, {Name: "j"},
	}

	results, err := weft.Traverse(greet, weft.WithConcurrency(3))(context.Background(), inputs)
	if err != nil {
		t.Fatalf("traverse: %v", err)
	}
	if len(results) != len(inputs) {
		t.Errorf("got %d results, want %d", len(results), len(inputs))
	}
	for i, r := range results {
		want := "hello, " + inputs[i].Name
		if r.Message != want {
			t.Errorf("[%d] got %q, want %q", i, r.Message, want)
		}
	}
	if got := int(calls.Load()); got != len(inputs) {
		t.Errorf("tap fired %d times, want %d", got, len(inputs))
	}
}

// === Type signature confirmation =============================================

func TestTool_ProducesArrowOfRightType(t *testing.T) {
	// Compile-time check that mcp.Tool returns weft.Arrow[In, Out].
	client := connectInProcess(t, mcp.ServeAsTool("greet", greetArrow))
	var _ weft.Arrow[GreetIn, GreetOut] = mcp.Tool[GreetIn, GreetOut](client, "greet")
}

// === Schema handling =========================================================

func TestServeAsTool_DefaultSchemaIsPermissive(t *testing.T) {
	// Without WithInputSchema, the default schema is `{"type":"object"}`,
	// which accepts any object. This makes ServeAsTool work out of the
	// box for arbitrary Go types without requiring caller-side schema work.
	entry := mcp.ServeAsTool("greet", greetArrow)
	if len(entry.Info.InputSchema) == 0 {
		t.Error("expected default schema to be set")
	}
	var schema map[string]any
	if err := json.Unmarshal(entry.Info.InputSchema, &schema); err != nil {
		t.Errorf("default schema not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("default schema not permissive object: %v", schema)
	}
}

func TestServeAsTool_CustomSchemaIsUsed(t *testing.T) {
	custom := json.RawMessage(`{"type":"object","required":["name"]}`)
	entry := mcp.ServeAsTool("greet", greetArrow, mcp.WithInputSchema(custom))
	if string(entry.Info.InputSchema) != string(custom) {
		t.Errorf("custom schema not preserved: %s", entry.Info.InputSchema)
	}
}

func TestServeAsTool_DescriptionIsUsed(t *testing.T) {
	entry := mcp.ServeAsTool("greet", greetArrow,
		mcp.WithDescription("Greet the user warmly"))
	if entry.Info.Description != "Greet the user warmly" {
		t.Errorf("description not preserved: %q", entry.Info.Description)
	}
}
