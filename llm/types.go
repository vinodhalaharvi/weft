// Package llm provides provider-neutral types for LLM-shaped arrows.
//
// The central types are llm.Prompt and llm.Response. Every LLM-backed
// arrow has the shape weft.Arrow[Prompt, Response], regardless of
// provider. This means Claude, OpenAI, Ollama, and any future provider
// are interchangeable from the perspective of the rest of the system.
//
// Provider-specific features (Claude's prompt caching, OpenAI's logprobs,
// Ollama's mirostat sampling) live in typed pointer fields on
// ProviderExtras — set the one for your provider, the others are ignored.
// This keeps the common path simple while leaving full power available.
package llm

import "encoding/json"

// Role identifies who said a message. Borrowed from the OpenAI/Claude
// conventions; every major provider supports these four.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// BlockKind discriminates the variants of Block.
type BlockKind int

const (
	BlockText BlockKind = iota
	BlockImage
	BlockToolUse
	BlockToolResult
	BlockThinking
)

// Block is a tagged-union element of a message.
//
// We use a discriminator + struct rather than an interface because
// interfaces erase types and break round-trip serialization. Only the
// fields relevant to Kind are populated; the rest stay zero.
type Block struct {
	Kind BlockKind

	// Populated when Kind == BlockText
	Text string

	// Populated when Kind == BlockImage. Either URL or inline bytes
	// (with MimeType) — providers vary in what they accept.
	ImageURL   string
	ImageBytes []byte
	MimeType   string

	// Populated when Kind == BlockToolUse (assistant invoked a tool)
	ToolUseID string
	ToolName  string
	ToolInput json.RawMessage

	// Populated when Kind == BlockToolResult (user/system providing tool output)
	ToolResultID string
	ToolResult   string // serialized result; format depends on the tool

	// Populated when Kind == BlockThinking (Claude's extended thinking)
	Thinking string
}

// Message is a single turn in the conversation.
type Message struct {
	Role    Role
	Content []Block
}

// UserText is sugar for the very common case of a single text message.
func UserText(text string) Message {
	return Message{
		Role:    RoleUser,
		Content: []Block{{Kind: BlockText, Text: text}},
	}
}

// AssistantText is sugar for an assistant text message.
func AssistantText(text string) Message {
	return Message{
		Role:    RoleAssistant,
		Content: []Block{{Kind: BlockText, Text: text}},
	}
}

// ToolSpec describes a tool the LLM may call.
//
// InputSchema is a JSON Schema describing the tool's input shape.
// We use json.RawMessage so the schema is exactly what providers expect
// without forcing us to model JSON Schema in Go types.
type ToolSpec struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// Prompt is the input to an LLM-shaped arrow.
type Prompt struct {
	// System prompt, applied before all messages. Most providers want
	// this as a separate field; we model it that way too.
	System string

	// The conversation so far.
	Messages []Message

	// Tools the model may call. Empty for plain text completion.
	Tools []ToolSpec

	// Generation parameters. Zero values mean "use provider default."
	MaxTokens   int
	Temperature float64
	StopSeqs    []string

	// Provider-specific configuration. See ProviderExtras.
	Extra ProviderExtras
}

// StopReason describes why the model stopped generating.
type StopReason int

const (
	StopUnknown   StopReason = iota
	StopEndTurn              // model finished naturally
	StopMaxTokens            // hit MaxTokens
	StopSequence             // hit a stop sequence
	StopToolUse              // model is calling a tool
	StopRefusal              // safety / policy refusal
)

// Response is the output from an LLM-shaped arrow.
type Response struct {
	// Messages produced by the model. Usually a single assistant
	// message; may include thinking blocks (Claude) and/or tool_use
	// blocks if the model is invoking tools.
	Messages []Message

	StopReason StopReason
	Usage      Usage
	Model      string // resolved model identifier from the provider
	RawID      string // provider's response ID, useful for tracing
}

// Text is the convenience accessor for the simple case: concatenate
// all text blocks from the (typically single) assistant message.
func (r Response) Text() string {
	var out string
	for _, msg := range r.Messages {
		for _, b := range msg.Content {
			if b.Kind == BlockText {
				out += b.Text
			}
		}
	}
	return out
}

// ToolCalls returns the tool_use blocks from the response, if any.
// Used by agent loops to dispatch tool invocations.
func (r Response) ToolCalls() []Block {
	var out []Block
	for _, msg := range r.Messages {
		for _, b := range msg.Content {
			if b.Kind == BlockToolUse {
				out = append(out, b)
			}
		}
	}
	return out
}

// Usage tracks token consumption for cost accounting.
type Usage struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int // Claude prompt-caching reads
	CacheWriteTokens int // Claude prompt-caching writes
}

// Add combines two Usage values, useful for accumulating across calls.
func (u Usage) Add(o Usage) Usage {
	return Usage{
		InputTokens:      u.InputTokens + o.InputTokens,
		OutputTokens:     u.OutputTokens + o.OutputTokens,
		CacheReadTokens:  u.CacheReadTokens + o.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens + o.CacheWriteTokens,
	}
}

// ProviderExtras is a typed escape hatch for provider-specific features.
// Set the field for your provider; leave the others nil. Each provider
// arrow only reads its own field.
type ProviderExtras struct {
	Anthropic *AnthropicExtras
	OpenAI    *OpenAIExtras
	Ollama    *OllamaExtras
}

// AnthropicExtras captures Claude-specific options.
type AnthropicExtras struct {
	// Beta features (e.g. "prompt-caching-2024-07-31")
	Beta []string

	// Extended thinking budget; nil means disabled.
	ThinkingBudget *int

	// Stop the model after the assistant produces this many tool calls.
	// Nil means no limit (uses MaxIter from the loop combinator instead).
	MaxToolUses *int
}

// OpenAIExtras captures OpenAI-specific options.
type OpenAIExtras struct {
	LogProbs       *bool
	TopLogProbs    *int
	ResponseFormat *string // "json_object" etc.
}

// OllamaExtras captures Ollama-specific options.
type OllamaExtras struct {
	NumCtx     *int // context window size
	Mirostat   *int // 0 = disabled, 1 or 2 enables variants
	RepeatLast *int
}
