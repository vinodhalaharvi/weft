# weft — categorical composition primitives for Go

A small Go module providing a category-theoretic algebra for composing
effectful, cancellable computations. The central type is:

```go
type Arrow[A, B any] func(ctx context.Context, a A) (B, error)
```

Every "thing that does work" — a remote API call, a local function,
an LLM invocation, an MCP tool, a multi-step pipeline, an agent loop —
can be expressed as an `Arrow`. Once it is, the same combinators
(`Compose`, `Par`, `Sum`, `Map`, `Traverse`) operate uniformly on it
without caring how it was constructed. This is the **role-erasure**
property the package exists to provide.

## Status

This is the algebra core. It is verified against the categorical laws
via property-based tests:

- Identity: `Compose(Id, f) ≡ f ≡ Compose(f, Id)`
- Associativity: `Compose(Compose(f, g), h) ≡ Compose(f, Compose(g, h))`
- Functor laws on `Map`: identity preservation, composition preservation
- Cancellation propagation through every combinator
- Concurrency correctness in `Par` and `Traverse`

The MCP functor (lifting MCP servers into typed arrows and back), the
LLM provider arrows, and the agent loop combinator are designed but
not yet implemented in this version. They are the natural next layer
on top of this core.

## Quick start

```go
import "github.com/vinodhalaharvi/weft/ct"

// Compose pure functions and effectful arrows uniformly.
greet := weft.Pure(func(name string) string { return "Hello, " + name })
shout := weft.Pure(strings.ToUpper)
loud  := weft.Compose(greet, shout)

result, err := loud(ctx, "world")  // "HELLO, WORLD"
```

```go
// Run two arrows in parallel.
both := weft.Par(fetchUser, fetchOrders)
pair, err := both(ctx, userID)
// pair.Fst is the user, pair.Snd is the orders

// Process a slice with bounded concurrency.
each := weft.Traverse(processOne,
    weft.WithConcurrency(8),
    weft.OnError(weft.PartialResults),
)
results, err := each(ctx, items)
```

```go
// Wrap an arrow with cross-cutting concerns.
robust := weft.Apply(myArrow,
    weft.WithRetry[In, Out](3, weft.ExponentialBackoff(time.Second)),
    weft.WithTimeout[In, Out](30*time.Second),
)
```

## Building and testing

```
make            # build and test
make laws       # run only the categorical law tests
make example    # run the end-to-end pipeline example
make test-race  # run tests with the race detector
make cover      # generate HTML coverage report
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

Set `ANTHROPIC_API_KEY` (and optionally `GITHUB_TOKEN`) in your shell or in
a `.env` file next to `docker-compose.yml`. See `.env.example` for the
expected variables.

The dev image includes:
- `golang:1.22-bookworm` as base
- `make`, `git`, `curl`, `jq`, `ripgrep`, `vim`
- Node.js + npm
- Python 3
- Claude Code (`@anthropic-ai/claude-code`)
- MCP servers: `server-filesystem`, `server-github`, `server-memory`

## What's in `weft/`

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

## Design philosophy

**No interfaces in the composition path.** The framework's value is
that types flow end-to-end through composition. `Arrow[A, B]` is a
generic function type, not an interface. `Compose(f, g)` is type-checked
at compile time. Refactoring an input or output type breaks compilation
predictably.

**Combinators encode laws, not features.** Every combinator in this
package corresponds to a categorical primitive: identity, sequential
composition, product, coproduct, functor map. The laws those primitives
satisfy are tested as the package's spec.

**Erasure is contained at boundaries.** When erasure is unavoidable
(e.g., LLM tool dispatch over JSON), it lives in a single function
that wraps a typed arrow into an erased shape. The original arrow
stays usable, fully typed, in the rest of the program.

## License

TBD by the project owner.
