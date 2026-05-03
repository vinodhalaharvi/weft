# weft — Complete API Reference

Every exported type and function across all three packages, organized so you
can see the whole framework at one glance.

---

## Package `weft` — the core algebra

### The central type

```go
type Arrow[A, B any] func(ctx context.Context, a A) (B, error)
```

Everything else in the framework is either a constructor that produces an
`Arrow`, a combinator that composes `Arrow`s, or a transform that wraps an
`Arrow` with cross-cutting behavior.

---

### Constructors — make an Arrow

```go
func Id[A any]() Arrow[A, A]
func ArrowFunc[A, B any](f func(ctx context.Context, a A) (B, error)) Arrow[A, B]
func Pure[A, B any](f func(A) B) Arrow[A, B]
```

| Function     | When to use                                             |
|--------------|---------------------------------------------------------|
| `Id`         | Pass-through; the no-op branch of `Sum`, identity laws  |
| `ArrowFunc`  | Force a function literal to be treated as `Arrow`       |
| `Pure`       | Lift a non-effectful, non-failing function into `Arrow` |

---

### Sequential composition — chain Arrows

```go
func Compose[A, B, C any](f Arrow[A, B], g Arrow[B, C]) Arrow[A, C]

func Pipe2[A, B, C any]                  (f, g)             Arrow[A, C]
func Pipe3[A, B, C, D any]               (f, g, h)          Arrow[A, D]
func Pipe4[A, B, C, D, E any]            (f, g, h, i)       Arrow[A, E]
func Pipe5[A, B, C, D, E, F any]         (f, g, h, i, j)    Arrow[A, F]
func Pipe6[A, B, C, D, E, F, G any]      (f, g, h, i, j, k) Arrow[A, G]
```

`Compose(f, g)` is the primitive. `Pipe3..Pipe6` are sugar for chains of that
length. Beyond six, name intermediate stages.

---

### Mapping — transform a single side

```go
func Map[A, B, C any]    (f Arrow[A, B], h func(B) C) Arrow[A, C]
func PreMap[A, B, C any] (h func(A) B, f Arrow[B, C]) Arrow[A, C]
```

`Map` rewrites the output. `PreMap` rewrites the input. Both keep types
end-to-end.

---

### Product — run two Arrows on the same input

```go
type Pair[A, B any]   struct { Fst A; Snd B }
type Triple[A, B, C any] struct { Fst A; Snd B; Thd C }

func MakePair[A, B any](a A, b B) Pair[A, B]
func Fst[A, B any]() Arrow[Pair[A, B], A]
func Snd[A, B any]() Arrow[Pair[A, B], B]

func Par[A, B, C any]       (f Arrow[A, B], g Arrow[A, C]) Arrow[A, Pair[B, C]]
func ParStrict[A, B, C any] (f Arrow[A, B], g Arrow[A, C]) Arrow[A, Pair[B, C]]
func Par3[A, B, C, D any]   (f, g, h)                       Arrow[A, Triple[B, C, D]]
func Fanout[A, B, C any]    (f Arrow[A, B], g Arrow[A, C]) Arrow[A, Pair[B, C]]
```

| Function      | Behavior                                                   |
|---------------|------------------------------------------------------------|
| `Par`         | Run both concurrently; cancel sibling on first failure     |
| `ParStrict`   | Same as Par but fail-fast cancellation                     |
| `Par3`        | Three-way version                                          |
| `Fanout`      | Alias for Par; the CT-literature name for "same input"     |

---

### Coproduct — branch between two Arrows

```go
type Either[A, B any]    struct { Left *A; Right *B }
type FourWay[A, B, C, D any] struct { A *A; B *B; C *C; D *D }

func Left[A, B any](a A) Either[A, B]
func Right[A, B any](b B) Either[A, B]
func (e Either[A, B]) IsLeft() bool
func (e Either[A, B]) IsRight() bool

func Sum[A, B, C any]       (f Arrow[A, C], g Arrow[B, C])             Arrow[Either[A, B], C]
func Sum4[A, B, C, D, R any](fA, fB, fC, fD)                            Arrow[FourWay[...], R]
func Fallback[A, B any]     (primary, backup Arrow[A, B])               Arrow[A, B]
func OnSentinel[A, B any]   (sentinel error, backup Arrow[A, B])        func(Arrow[A, B]) Arrow[A, B]
```

| Function      | Behavior                                                       |
|---------------|----------------------------------------------------------------|
| `Sum`         | Dispatch by Either tag                                         |
| `Sum4`        | Dispatch by FourWay tag (4-way coproduct)                      |
| `Fallback`    | Try primary; on any non-cancellation error, try backup         |
| `OnSentinel`  | Try primary; if error matches sentinel, run backup             |

---

### Traverse — apply an Arrow over a slice

```go
type ErrorPolicy int
const (
    FailFast ErrorPolicy = iota
    CollectErrors
    SkipFailures
    PartialResults
)

type TraverseOpt func(*traverseConfig)

func WithConcurrency(n int) TraverseOpt
func OnError(p ErrorPolicy) TraverseOpt

func Traverse[A, B any](f Arrow[A, B], opts ...TraverseOpt) Arrow[[]A, []B]
```

| `ErrorPolicy`     | Behavior on per-item failure                                 |
|-------------------|--------------------------------------------------------------|
| `FailFast`        | First error cancels remaining work                           |
| `CollectErrors`   | Run all; return `errors.Join(...)`                           |
| `SkipFailures`    | Drop failed items; return only successes                     |
| `PartialResults`  | Return `[]B` (zero at failed indices) + `*PartialError`      |

---

### Transforms — wrap an Arrow with cross-cutting behavior

```go
type Transform[A, B any] func(Arrow[A, B]) Arrow[A, B]
type BackoffFunc func(attempt int) time.Duration

func Apply[A, B any]   (a Arrow[A, B], transforms ...Transform[A, B]) Arrow[A, B]
func LinearBackoff     (d time.Duration) BackoffFunc
func ExponentialBackoff(base time.Duration) BackoffFunc

func WithRetry[A, B any]   (maxAttempts int, backoff BackoffFunc)             Transform[A, B]
func WithTimeout[A, B any] (d time.Duration)                                  Transform[A, B]
func WithTap[A, B any]     (tap func(in A, out B, err error))                 Transform[A, B]
```

`Apply` layers transforms left-to-right (leftmost = outermost wrapper).
Transforms preserve the arrow's type signature; they only change behavior.

---

### Errors

```go
type Class int
const (
    ClassUnknown Class = iota
    ClassTransient        // retryable: rate limit, 5xx, network blip
    ClassPermanent        // not retryable: auth, schema, validation
    ClassBudget           // out of tokens / time / money
    ClassUserCancelled    // ctx cancelled by user
)

type ArrowError struct {
    Class    Class
    Op       string             // e.g., "llm.Claude"
    Cause    error
    Metadata map[string]any
}

type PartialError struct {
    Failures map[int]error
    Total    int
}

func Classify(err error) Class
```

`WithRetry` and `Fallback` consult `Classify` to decide what to retry.
Seams populate `ArrowError` fields; consumers use `errors.As` to inspect.

---

## Package `llm` — provider-neutral LLM types and seams

### The core types

```go
type Role string
const (
    RoleSystem    Role = "system"
    RoleUser      Role = "user"
    RoleAssistant Role = "assistant"
    RoleTool      Role = "tool"
)

type BlockKind int
const (
    BlockText BlockKind = iota
    BlockImage
    BlockToolUse
    BlockToolResult
    BlockThinking
)

type Block struct {
    Kind         BlockKind
    Text         string
    ImageURL     string
    ImageBytes   []byte
    MimeType     string
    ToolUseID    string
    ToolName     string
    ToolInput    json.RawMessage
    ToolResultID string
    ToolResult   string
    Thinking     string
}

type Message struct {
    Role    Role
    Content []Block
}

func UserText(text string) Message
func AssistantText(text string) Message

type ToolSpec struct {
    Name        string
    Description string
    InputSchema json.RawMessage  // JSON Schema; same shape MCP uses
}

type Prompt struct {
    System      string
    Messages    []Message
    Tools       []ToolSpec
    MaxTokens   int
    Temperature float64
    StopSeqs    []string
    Extra       ProviderExtras
}

type StopReason int
const (
    StopUnknown StopReason = iota
    StopEndTurn
    StopMaxTokens
    StopSequence
    StopToolUse
    StopRefusal
)

type Response struct {
    Messages   []Message
    StopReason StopReason
    Usage      Usage
    Model      string
    RawID      string
}

func (r Response) Text() string
func (r Response) ToolCalls() []Block

type Usage struct {
    InputTokens      int
    OutputTokens     int
    CacheReadTokens  int
    CacheWriteTokens int
}

func (u Usage) Add(o Usage) Usage
```

### Provider-specific options

```go
type ProviderExtras struct {
    Anthropic *AnthropicExtras
    OpenAI    *OpenAIExtras
    Ollama    *OllamaExtras
}

type AnthropicExtras struct {
    Beta           []string
    ThinkingBudget *int
    MaxToolUses    *int
}

type OpenAIExtras struct {
    LogProbs       *bool
    TopLogProbs    *int
    ResponseFormat *string
}

type OllamaExtras struct {
    NumCtx     *int
    Mirostat   *int
    RepeatLast *int
}
```

### The Claude seam

```go
type ClaudeOption func(*claudeConfig)

func WithAPIKey(key string)              ClaudeOption
func WithAPIBase(url string)             ClaudeOption
func WithHTTPClient(client *http.Client) ClaudeOption
func WithAPIVersion(v string)            ClaudeOption

func Claude(model string, opts ...ClaudeOption) weft.Arrow[Prompt, Response]
```

**The headline:** `Claude(...)` returns `weft.Arrow[Prompt, Response]`. That
shape is the contract for every LLM provider. When `OpenAI(...)` and
`Ollama(...)` exist, they will return the same type.

---

## Package `mcp` — lift MCP tools in/out of the arrow algebra

### Lift in: remote MCP tool → typed weft.Arrow

```go
type Client struct { /* ... */ }

func Stdio(command string, args ...string) (Transport, error)
func Connect(ctx context.Context, t Transport) (*Client, error)
func (c *Client) Tools() []string
func (c *Client) Close() error

func Tool[In, Out any](c *Client, name string) weft.Arrow[In, Out]
```

`mcp.Tool[A, B](client, name)` is the headline lift-in. It returns a
`weft.Arrow[A, B]` that, when called, marshals `A` to JSON args, sends
a `tools/call` over the transport, parses the result back to `B`, and
returns it. From the caller's perspective it's just an arrow — same
shape as `llm.Claude` or `weft.Pure`, composes the same way.

### Lift out: typed weft.Arrow → MCP tool

```go
type ErasedTool struct {
    Info    ToolInfo
    Handler func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error)
}

type Server struct { /* ... */ }

func ServeAsTool[In, Out any](
    name string,
    arrow weft.Arrow[In, Out],
    opts ...ServeOption,
) ErasedTool

func Serve(entries ...ErasedTool) *Server
func RunStdioServer(server *Server, opts ...StdioServerOption) error
```

`ServeAsTool[A, B](name, myArrow)` is the headline lift-out. It takes
any typed arrow and packages it as a JSON-shaped tool that external
MCP clients can call. Combined with `Serve` and `RunStdioServer`, you
get a real stdio MCP server with one or more tools, where each tool's
implementation is a composed weft pipeline.

### Transports

```go
type Transport interface { /* ... */ }

func InMemory(server *Server) Transport
func Stdio(command string, args ...string) (Transport, error)
```

Two transports ship: `InMemory` for in-process round-trips (used by
the functor-laws property test in `mcp_test.go`), and `Stdio` for
real subprocess MCP servers, which wraps `mark3labs/mcp-go`'s stdio
client. The `Transport` interface is open — adding HTTP/SSE later is
a contained addition.

---

## How they layer

```
                          ┌──────────────────────────────────┐
   Application code  ───► │  llm.Loop(claude, tools)         │  ← agent loop
                          └──────────────────────────────────┘
                                       │
                                       │ tools come from anywhere
                                       ▼
                          ┌──────────────────────────────────┐
       MCP lift-in   ───► │  mcp.Tool[A, B](client, name)    │
                          └──────────────────────────────────┘
                                       │
                                       │ wrapped optionally with transforms
                                       ▼
                          ┌──────────────────────────────────┐
       Cross-cutting ───► │  weft.WithRetry, WithTimeout, …  │
                          └──────────────────────────────────┘

   At any point, the composed arrow can flow back out:

                          ┌──────────────────────────────────┐
       MCP lift-out  ───► │  mcp.ServeAsTool(name, myArrow)  │  ← exposed as MCP tool
                          └──────────────────────────────────┘
```

Every arrow at every level has the same shape: `func(ctx, In) (Out, error)`.
That uniformity is what lets you swap any layer without touching the
others — and it lets you compose a tool from one MCP server with a
tool from a different MCP server, plus pure Go logic, into a new tool
that you re-expose as a third MCP server. The `multi-source-server`
example demonstrates exactly this.

---

## The size of it all

| Package    | Public functions | Public types | Lines (impl) |
|------------|------------------|--------------|--------------|
| `weft`     | 27               | 8            | ~700         |
| `llm`      | 9                | 13           | ~700         |
| `mcp`      | 11               | 6            | ~600         |
| **Total**  | **47**           | **27**       | **~2,000**   |

Plus ~2,000 lines of tests across 83 test functions. The whole framework
is still small enough to read in an afternoon.
