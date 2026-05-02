# weft — categorical composition primitives for Go

A small Go module providing a category-theoretic algebra for composing
LLM, MCP, and agent workflows as typed, cancellable, fallible functions.
The central type is one line:

```go
type Arrow[A, B any] func(ctx context.Context, a A) (B, error)
```

Every "thing that does work" — a remote API call, a local function,
an LLM invocation, an MCP tool, a multi-step pipeline, an agent loop —
can be expressed as an `Arrow`. Once it is, the same combinators
(`Compose`, `Par`, `Sum`, `Map`, `Traverse`) operate uniformly on it
without caring how it was constructed. This is the **role-erasure**
property the package exists to provide.

## What's here

The repo contains three Go packages plus example CLIs:

| Package      | What it provides                                   | Tests |
|--------------|----------------------------------------------------|------:|
| `weft/`      | The core algebra: `Arrow`, combinators, transforms |    47 |
| `llm/`       | Provider-neutral LLM types and the Claude HTTP seam |   14 |
| `codegen/`   | Directory-wide LLM-driven code transformations      |   13 |
| **Total**    |                                                    |  **74** |

The core algebra is verified against categorical laws via property-based
tests (identity, associativity, functor laws, cancellation propagation,
concurrency correctness in `Par` and `Traverse`). The LLM seam is
verified against a mock Anthropic API server. The MCP functor and
streaming arrow type are designed but not yet implemented.

See [API.md](./API.md) for every exported type and function across all
three packages, organized for at-a-glance comparison.

## Quick start

Requires Go 1.22 or later.

```bash
git clone https://github.com/vinodhalaharvi/weft.git
cd weft
make             # build + run all 74 tests
```

Or use as a library:

```bash
go get github.com/vinodhalaharvi/weft
```

```go
import (
    "github.com/vinodhalaharvi/weft/weft"
    "github.com/vinodhalaharvi/weft/llm"
    "github.com/vinodhalaharvi/weft/codegen"
)
```

### A few examples of the core algebra

```go
// Compose pure functions and effectful arrows uniformly.
greet := weft.Pure(func(name string) string { return "Hello, " + name })
shout := weft.Pure(strings.ToUpper)
loud  := weft.Compose(greet, shout)

result, err := loud(ctx, "world")  // "HELLO, WORLD"
```

```go
// Run two arrows in parallel, get both results.
both := weft.Par(fetchUser, fetchOrders)
pair, err := both(ctx, userID)
// pair.Fst is the user, pair.Snd is the orders

// Process a slice with bounded concurrency, partial-results error policy.
each := weft.Traverse(processOne,
    weft.WithConcurrency(8),
    weft.OnError(weft.PartialResults),
)
results, err := each(ctx, items)
```

```go
// Wrap an arrow with cross-cutting concerns. Type signature is preserved.
robust := weft.Apply(myArrow,
    weft.WithRetry[In, Out](3, weft.ExponentialBackoff(time.Second)),
    weft.WithTimeout[In, Out](30*time.Second),
    weft.WithTap[In, Out](logProgress),
)
```

## Worked example: codegen across a directory

The `codegen` package applies an LLM transformation across every file in
a directory matching a glob pattern. It exercises the full composition
story: pure Go stages, an LLM call wrapped in retry and timeout, bounded
concurrency, atomic writes with dry-run support.

Try it without an API key (uses a deterministic stub transformer):

```bash
mkdir -p /tmp/demo
cat > /tmp/demo/foo.go <<'EOF'
package demo

func Hello() string { return "hello" }
EOF

go run ./examples/codegen-llm \
    -dir /tmp/demo \
    -prompt "add a doc comment to each function" \
    -patterns "*.go" \
    -dry -offline
```

Output:

```
~ foo.go — would change (... bytes diff)
DRY RUN: 1 files (1 would change, 0 unchanged, 0 failed)
```

With a real Claude API key:

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run ./examples/codegen-llm \
    -dir /tmp/demo \
    -prompt "add a one-line godoc comment to each exported function" \
    -patterns "*.go" \
    -dry           # always dry-run first to inspect the diff
```

### How the layers stack

The codegen example is composition all the way down:

```
codegen.Pipeline(transformer, 4, weft.PartialResults)         ← top-level
  └─ transformer = weft.Apply(                                  ← wrapped with
       weft.Pipe3(formatPrompt, llm.Claude(model), parseResp),  ← composed of
       weft.WithRetry(3, ExponentialBackoff(time.Second)),      ↓ transforms
       weft.WithTimeout(60 * time.Second),
       weft.WithTap(logProgress),
     )
```

Every arrow at every layer has the same type. The codegen pipeline doesn't
know it contains an LLM call. The LLM call doesn't know it's wrapped in
retry. The retry doesn't know it's part of a parallel traversal. Each
layer only sees its argument's type contract — that's the role-erasure.

To swap providers, you change one stage:

```go
weft.Pipe3(formatPrompt, llm.Claude(model), parseResp)
//                       ^^^^^^^^^^^^^^^^^
// becomes:
weft.Pipe3(formatPrompt, llm.OpenAI(model), parseResp)  // when implemented
```

Nothing else changes.

## Building and testing

```
make            # build and test (default)
make test       # run all 74 tests
make test-race  # run tests with the race detector
make laws       # run only the categorical law tests (the spec)
make example    # run the end-to-end codegen pipeline test
make cover      # generate HTML coverage report
make lint       # go vet + gofmt check
make help       # list every target with descriptions
```

## Docker

If you don't want to install Go locally, or you want a uniform environment
that includes Claude Code and common MCP servers:

```
# CI-style: build a lean image, run the test suite, exit.
make docker-ci

# Dev-style: start a long-running container with Claude Code, MCP servers,
# Node.js, and the full toolbox. Bind-mounts your source for live editing.
make docker-dev
make docker-shell           # open a shell inside it
make docker-test            # run tests inside it
docker compose exec dev claude mcp serve   # run Claude Code as an MCP server
make docker-down            # stop everything
```

Set `ANTHROPIC_API_KEY` (and optionally `GITHUB_TOKEN`) in your shell or
in a `.env` file next to `docker-compose.yml`. See `.env.example`.

The dev image includes:
- `golang:1.22-bookworm` as base
- `make`, `git`, `curl`, `jq`, `ripgrep`, `vim`
- Node.js + npm, Python 3
- Claude Code (`@anthropic-ai/claude-code`)
- MCP servers: `server-filesystem`, `server-github`, `server-memory`

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

### `codegen/` — directory-wide LLM transformations

| File              | Provides |
|-------------------|----------|
| `codegen.go`      | `Pipeline`, `Enumerate`, `Apply`, `WriteOrDiff`, `Job`, `File`, `Edit`, `FileResult`, `Transformer` |

## Design philosophy

**No interfaces in the composition path.** Type information flows
end-to-end through composition. `Arrow[A, B]` is a generic function type,
not an interface. `Compose(f, g)` is type-checked at compile time —
refactor an input or output type and the compiler tells you exactly which
calls broke.

**Combinators encode laws, not features.** Every combinator corresponds
to a categorical primitive: identity, sequential composition, product,
coproduct, functor map. The laws those primitives satisfy are tested as
the package's spec, not as an afterthought.

**Erasure is contained at boundaries.** When erasure is unavoidable
(e.g., LLM tool dispatch over JSON, MCP wire format), it lives in a
single function that wraps a typed arrow into an erased shape. The
original arrow stays usable, fully typed, in the rest of the program.

For a longer discussion, including comparison tables against LangChain
and other frameworks, see [API.md](./API.md).

## A note on the LLM seam

The Claude arrow has been tested against a mock Anthropic server that
mirrors the documented API shape. It has not been exercised against the
real API in this repo's CI. The first time you run it with a real key,
the most likely failure modes are:

- The default model name (`claude-opus-4-5`); update to current per
  [Anthropic's docs](https://docs.claude.com/en/docs/about-claude/models).
- The `anthropic-version` header value (`2023-06-01` is the default;
  configurable via `llm.WithAPIVersion`).

Both are one-line changes if needed.

## A note on naming

The name "weft" comes from weaving — the thread woven crosswise through
the warp threads to make fabric. It captures the framework's idea:
heterogeneous arrows woven together into composable pipelines.

There are unrelated projects also named "weft" — notably
[WeaveMindAI/weft](https://github.com/WeaveMindAI/weft) (a Rust-based
visual programming language for AI workflows) and
[hyperledger-labs/weft](https://github.com/hyperledger-labs/weft) (a
Hyperledger Fabric CLI). This project is a Go library for arrow-based
composition; the import path `github.com/vinodhalaharvi/weft` makes the
distinction unambiguous.

## License

[MIT](./LICENSE).
