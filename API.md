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

## Package `codegen` — directory-wide LLM transformations

### Domain types

```go
type Job struct {
    Dir      string   // root directory
    Patterns []string // globs to include (e.g., "**/*.go")
    Skip     []string // globs to exclude
    Prompt   string   // instruction applied to each file
    DryRun   bool     // produce diffs without writing
}

type File struct {
    Path    string
    RelPath string
    Content string
}

type Edit struct {
    File        File
    NewContent  string
    Explanation string
}

type FileResult struct {
    Path        string
    RelPath     string
    Wrote       bool
    Unchanged   bool
    Diff        string
    Explanation string
    Err         error
}
```

### The transformer slot

```go
type TransformReq struct {
    File   File
    Prompt string
}

type TransformResp struct {
    NewContent  string
    Explanation string
}

type Transformer = weft.Arrow[TransformReq, TransformResp]
```

Note the type alias: a `Transformer` is just `weft.Arrow[TransformReq, TransformResp]`.
You can plug in *any* arrow of that shape — a stub for tests, an LLM-backed
pipeline, a deterministic rule-based transformer, an ensemble of all three.

### Stages, each independently usable

```go
func Enumerate()                weft.Arrow[Job, []File]
func Apply(t Transformer, prompt string) weft.Arrow[File, Edit]
func WriteOrDiff(dryRun bool)   weft.Arrow[Edit, FileResult]

func Pipeline(
    transformer Transformer,
    concurrency int,
    policy weft.ErrorPolicy,
) weft.Arrow[Job, []FileResult]
```

`Pipeline` is what most callers use: enumerate + apply + write, all wrapped
together with bounded concurrency. The individual stages are exported so you
can mix-and-match.

---

## How they layer

```
                          ┌──────────────────────────────────┐
   Application code  ───► │  codegen.Pipeline(...)           │  ← top-level pipeline
                          └──────────────────────────────────┘
                                       │
                                       │ takes a Transformer (= weft.Arrow)
                                       ▼
                          ┌──────────────────────────────────┐
        Provider seam ──► │  weft.Pipe3(format, Claude, parse) │
                          └──────────────────────────────────┘
                                       │
                                       │ middle stage is the LLM call
                                       ▼
                          ┌──────────────────────────────────┐
        LLM API call  ──► │  llm.Claude(model)                │  ← Arrow[Prompt, Response]
                          └──────────────────────────────────┘
                                       │
                                       │ wrapped optionally with transforms
                                       ▼
                          ┌──────────────────────────────────┐
        Cross-cutting ──► │  weft.WithRetry, WithTimeout, ... │
                          └──────────────────────────────────┘
```

Every arrow at every level has the same shape: `func(ctx, In) (Out, error)`.
That uniformity is what lets you swap any layer without touching the others.

---

## The size of it all

| Package    | Public functions | Public types | Lines (impl) |
|------------|------------------|--------------|--------------|
| `weft`     | 27               | 8            | ~700         |
| `llm`      | 7                | 13           | ~450         |
| `codegen`  | 4                | 7            | ~250         |
| **Total**  | **38**           | **28**       | **~1,400**   |

Plus ~1,200 lines of tests across 74 test functions. The whole framework is
small enough to read in an afternoon.
