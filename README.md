# weft — categorical composition primitives for Go

A small Go module that treats every step in an LLM/MCP/agent workflow —
pure functions, MCP tool calls, LLM invocations, agent loops, parallel
fan-outs — as the same `Arrow[A, B]` type, and provides a coherent
algebra for composing them. The categorical laws (identity, associativity,
functor, product, coproduct) are verified as the package's spec via
property-based tests.

```go
type Arrow[A, B any] func(ctx context.Context, a A) (B, error)
```

That's the central abstraction. The combinators (`Compose`, `Pipe2`–`Pipe6`,
`Par`, `Sum`, `Map`, `Traverse`, `Apply`, `WithRetry`, `WithTimeout`,
`WithTap`, `Loop`, …) operate uniformly on every arrow regardless of how
it was constructed. This is the **role-erasure** the package exists to
provide.

---

## What you can do with it

The repo contains three Go packages plus eight runnable example commands:

| Package      | What it provides                                       | Tests |
|--------------|--------------------------------------------------------|------:|
| `weft/`      | The core algebra: `Arrow`, combinators, transforms     |    47 |
| `llm/`       | Provider-neutral LLM types, Claude HTTP arrow, `Loop`  |    24 |
| `mcp/`       | Lift MCP tools in/out of arrow algebra; stdio + server |    12 |
| **Total**    |                                                        |  **83** |

The MCP package builds on [`mark3labs/mcp-go`](https://github.com/mark3labs/mcp-go)
for the wire protocol — weft is **complementary**, not competing. mcp-go
gives you reliable JSON-RPC over stdio/SSE/HTTP; weft adds the
categorical algebra that lets you compose lifted tools with non-MCP
arrows uniformly, and re-expose composed arrows as new MCP servers.

---

## The headline demo

`cmd/multi-source-server` is a real working program. It connects to two
heterogeneous MCP servers as a client, lifts tools from each, composes
them into new tools using weft pipelines, and re-exposes the composed
tools as its own MCP server. Three process boundaries, two external
systems, one program.

```
test-multi-source                      ← any MCP client (here: included verifier)
  └─ multi-source-server (weft)        ← composes upstream tools into new ones
       ├─ claude mcp serve             ← Claude Code's MCP tools
       └─ npx server-filesystem        ← Official Node.js filesystem MCP
```

It exposes three composed tools: `shell_run` (delegates to Bash),
`read_file` (delegates to filesystem), and `bash_then_save` (composes
both — runs a shell command, parses the output, writes it to disk).
From a calling client, `bash_then_save` looks like one tool. Internally
it's an arrow composed of two arrows from two different subprocesses.

To verify against your own machine:

```bash
# Requires `claude` (Claude Code) and `npx` available on PATH.
go run ./cmd/test-multi-source
```

If all three subprocesses are reachable, you'll see three passing
assertions covering both source paths and the composition.

To use it from Claude Desktop, build the binary and add to your
`claude_desktop_config.json`:

```bash
go build -o /usr/local/bin/multi-source-server ./cmd/multi-source-server
```

```json
{
  "mcpServers": {
    "weft-multi": {
      "command": "/usr/local/bin/multi-source-server",
      "args": ["/private/tmp"]
    }
  }
}
```

After restarting Claude Desktop, the three composed tools become
available in any chat.

---

## Quick start

Requires Go 1.23 or later (`go.mod` declares 1.23; the toolchain may
auto-upgrade to 1.25.x to satisfy mcp-go's transitive deps).

```bash
git clone https://github.com/vinodhalaharvi/weft.git
cd weft
make             # build + run all 96 tests
```

Or use as a library:

```bash
go get github.com/vinodhalaharvi/weft
```

```go
import (
    "github.com/vinodhalaharvi/weft/weft"
    "github.com/vinodhalaharvi/weft/llm"
    "github.com/vinodhalaharvi/weft/mcp"
)
```

---

## Examples in increasing depth

### Compose pure functions and effectful arrows uniformly

```go
greet := weft.Pure(func(name string) string { return "Hello, " + name })
shout := weft.Pure(strings.ToUpper)
loud  := weft.Compose(greet, shout)

result, err := loud(ctx, "world")  // "HELLO, WORLD"
```

### Run things in parallel, traverse a slice with bounded concurrency

```go
both := weft.Par(fetchUser, fetchOrders)
pair, err := both(ctx, userID)
// pair.Fst is the user, pair.Snd is the orders

each := weft.Traverse(processOne,
    weft.WithConcurrency(8),
    weft.OnError(weft.PartialResults),
)
results, err := each(ctx, items)
```

### Wrap arrows with cross-cutting concerns; type signatures preserved

```go
robust := weft.Apply(myArrow,
    weft.WithRetry[In, Out](3, weft.ExponentialBackoff(time.Second)),
    weft.WithTimeout[In, Out](30 * time.Second),
    weft.WithTap[In, Out](logProgress),
)
```

### Lift an MCP tool into the arrow algebra

```go
client, _ := mcp.Connect(ctx, mustStdio("claude", "mcp", "serve"))
defer client.Close()

// MCP tool → typed weft.Arrow
bash := mcp.Tool[map[string]any, string](client, "Bash")

// Composes with everything else in the algebra
robust := weft.Apply(bash,
    weft.WithRetry[map[string]any, string](2, weft.ExponentialBackoff(time.Second)),
    weft.WithTimeout[map[string]any, string](30 * time.Second),
)

out, err := robust(ctx, map[string]any{"command": "ls /tmp"})
```

### Run an agent loop with real Claude + real MCP tools

```go
client, _ := mcp.Connect(ctx, mustStdio("claude", "mcp", "serve"))
defer client.Close()

bash := mcp.Tool[map[string]any, string](client, "Bash")

bashBinding := llm.ToolBinding{
    Spec: llm.ToolSpec{
        Name:        "Bash",
        Description: "Run a shell command",
        InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`),
    },
    Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
        var m map[string]any
        json.Unmarshal(args, &m)
        return bash(ctx, m)
    },
}

agent := llm.Loop(
    llm.Claude("claude-sonnet-4-5-20250929"),
    []llm.ToolBinding{bashBinding /*, readBinding, ... */},
    llm.WithMaxIter(8),
)

// agent is just an Arrow[Prompt, Response]. Compose, retry, timeout
// it, parallelize across many prompts — all the same machinery.
resp, err := agent(ctx, llm.Prompt{
    Messages: []llm.Message{llm.UserText("count Go files in this repo")},
})
fmt.Println(resp.Text())
```

A complete runnable version is in
[`cmd/examples/agent/main.go`](./cmd/examples/agent/main.go). With a
real `ANTHROPIC_API_KEY`:

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run ./cmd/examples/agent "How many Go files are in this repo? Use any tools you need."
```

---

## How the layers stack

Composition is the design point. A typical pipeline looks like this:

```
weft.Traverse(                                               ← top-level: many items
    weft.Apply(                                              ← wrap one-item arrow
        weft.Pipe3(formatPrompt, llm.Claude(model), parse),  ← composed of three stages
        weft.WithRetry(3, ExponentialBackoff(time.Second)),  ↓ cross-cutting transforms
        weft.WithTimeout(60 * time.Second),
        weft.WithTap(logProgress),
    ),
    weft.WithConcurrency(8),
)
```

Every arrow at every layer has the same type. `Traverse` doesn't know it
contains an LLM call. The LLM call doesn't know it's wrapped in retry.
The retry doesn't know it's running concurrently across many items.
Each layer only sees its argument's type contract — that's the
role-erasure.

To swap providers, you change one line:

```go
weft.Pipe3(formatPrompt, llm.Claude(model), parse)
//                       ^^^^^^^^^^^^^^^^^
// becomes (when implemented):
weft.Pipe3(formatPrompt, llm.OpenAI(model), parse)
```

The runnable demo of this exact pattern is
[`cmd/examples/pipeline`](./cmd/examples/pipeline). It assesses code
quality across a slice of snippets — different application, same
composition machinery.

---

## Examples in this repo

| Command                          | What it does                                            |
|----------------------------------|---------------------------------------------------------|
| `cmd/examples/escalating`        | Haiku → Opus escalation when output looks low quality   |
| `cmd/examples/pipeline`          | Multi-step typed LLM pipeline                           |
| `cmd/examples/mcp-roundtrip`     | InMemory functor: lift in, lift out, verify identity    |
| `cmd/examples/mcp-stdio`         | Spawn `claude mcp serve`, call its tools                |
| `cmd/examples/agent`             | Full agent loop: real Claude + real MCP tools           |
| `cmd/discover-mcp`               | Inspector for any MCP stdio server                      |
| `cmd/multi-source-server`        | Compose two upstream MCP servers, expose as one         |
| `cmd/test-multi-source`          | End-to-end verifier for the multi-source server         |

The last two prove the framework's claims hold across three process
boundaries and two heterogeneous external systems, with structured
outputs flowing through cleanly.

---

## Building and testing

```
make            # build and test
make test       # run all 83 tests
make test-race  # run tests with the race detector
make laws       # run only the categorical law tests (the spec)
make example    # run the end-to-end pipeline test
make cover      # generate HTML coverage report
make lint       # go vet + gofmt check
make help       # list every target
```

---

## What's in each package

### `weft/` — the core algebra

| File              | Provides |
|-------------------|----------|
| `arrow.go`        | `Arrow[A,B]`, `Id`, `ArrowFunc`, `Pure` |
| `compose.go`      | `Compose`, `Pipe2`–`Pipe6`, `Map`, `PreMap` |
| `types.go`        | `Pair`, `Triple`, `Either`, `Fst`, `Snd` |
| `par.go`          | `Par`, `ParStrict`, `Par3`, `Fanout` |
| `sum.go`          | `Sum`, `Sum4`, `Fallback`, `OnSentinel` |
| `traverse.go`     | `Traverse` with FailFast / CollectErrors / SkipFailures / PartialResults |
| `transform.go`    | `Apply`, `WithRetry`, `WithTimeout`, `WithTap` |
| `errors.go`       | `ArrowError`, `Class`, `Classify`, `PartialError` |

### `llm/` — provider-neutral LLM types and Claude seam

| File              | Provides |
|-------------------|----------|
| `types.go`        | `Prompt`, `Response`, `Message`, `Block`, `ToolSpec`, `Usage`, `ProviderExtras` |
| `anthropic.go`    | `Claude(model, opts...) Arrow[Prompt, Response]` over the Anthropic Messages API |
| `loop.go`         | `Loop(llm, bindings, opts...) Arrow[Prompt, Response]` — agent loop combinator |

### `mcp/` — lift MCP tools in/out of the arrow algebra

| File              | Provides |
|-------------------|----------|
| `client.go`       | `Client`, `Connect`, `Tool[In, Out]` (lift in: MCP tool → arrow) |
| `server.go`       | `Server`, `Serve`, `ServeAsTool[In, Out]` (lift out: arrow → MCP tool) |
| `stdio.go`        | `Stdio` transport for client side; wraps `mark3labs/mcp-go` |
| `stdio_server.go` | `RunStdioServer` — expose a weft Server as a real MCP stdio server |
| `mcp.go`          | `Transport`, `InMemory` for in-process round-trips |

---

## Design philosophy

**No interfaces in the composition path.** Type information flows
end-to-end through composition. `Arrow[A, B]` is a generic function
type, not an interface. `Compose(f, g)` is type-checked at compile
time — refactor an input or output type and the compiler tells you
exactly which calls broke.

**Combinators encode laws, not features.** Every combinator corresponds
to a categorical primitive: identity, sequential composition, product,
coproduct, functor map. The laws those primitives satisfy are tested
as the package's spec, not as an afterthought.

**Erasure is contained at boundaries.** When erasure is unavoidable
(LLM tool dispatch over JSON, MCP wire format), it lives in a single
function that wraps a typed arrow into an erased shape. The original
arrow stays usable, fully typed, in the rest of the program.

**`mark3labs/mcp-go` does the wire protocol.** weft does the algebra.
The two are layered, not competing. mcp-go's reliability and ecosystem
become weft's reliability and ecosystem.

---

## A note on the LLM seam

The Claude arrow has been tested extensively against a mock Anthropic
server that mirrors the documented API shape, and exercised live via
`cmd/examples/agent` against the real Anthropic API. If you run into
trouble, the most likely failure modes are:

- The default `anthropic-version` header (`2023-06-01`); configurable
  via `llm.WithAPIVersion`.
- The model name in your code; pick one from
  [Anthropic's docs](https://docs.claude.com/en/docs/about-claude/models).

The Claude Desktop integration was verified end-to-end: a weft server
registered in `claude_desktop_config.json` is callable from real Claude
conversations.

---

## A note on naming

The name "weft" comes from weaving — the thread woven crosswise through
the warp threads to make fabric. It captures the framework's idea:
heterogeneous arrows woven together into composable pipelines.

There are unrelated projects also named "weft" — notably
[WeaveMindAI/weft](https://github.com/WeaveMindAI/weft) (a Rust-based
visual programming language) and
[hyperledger-labs/weft](https://github.com/hyperledger-labs/weft) (a
Hyperledger Fabric CLI). This project is a Go library; the import path
`github.com/vinodhalaharvi/weft` makes the distinction unambiguous.

---

## License

[MIT](./LICENSE).
