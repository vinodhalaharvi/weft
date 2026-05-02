package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/vinodhalaharvi/weft/weft"
)

// === Configuration ===========================================================

// ClaudeOption configures a Claude arrow.
type ClaudeOption func(*claudeConfig)

type claudeConfig struct {
	apiKey     string
	apiBase    string
	httpClient *http.Client
	version    string // anthropic-version header
}

// WithAPIKey sets the API key. If unset, falls back to ANTHROPIC_API_KEY env var.
func WithAPIKey(key string) ClaudeOption {
	return func(c *claudeConfig) { c.apiKey = key }
}

// WithAPIBase overrides the API base URL. Useful for proxies and mock servers.
func WithAPIBase(url string) ClaudeOption {
	return func(c *claudeConfig) { c.apiBase = url }
}

// WithHTTPClient supplies a custom *http.Client (e.g., with custom timeouts
// or transport for in-test mocking).
func WithHTTPClient(client *http.Client) ClaudeOption {
	return func(c *claudeConfig) { c.httpClient = client }
}

// WithAPIVersion sets the anthropic-version header. Defaults to a known-good value.
func WithAPIVersion(v string) ClaudeOption {
	return func(c *claudeConfig) { c.version = v }
}

// Claude returns a weft.Arrow that calls the Anthropic Messages API.
//
// The arrow's shape is weft.Arrow[Prompt, Response], same as every other
// LLM provider in this package. This makes Claude interchangeable with
// OpenAI, Ollama, or any in-process stub for tests.
//
// Errors from the API are wrapped in *weft.ArrowError with appropriate
// classification: rate limits and 5xx are ClassTransient (retryable),
// 4xx auth/schema errors are ClassPermanent (not retryable).
func Claude(model string, opts ...ClaudeOption) weft.Arrow[Prompt, Response] {
	cfg := claudeConfig{
		apiBase:    "https://api.anthropic.com",
		httpClient: &http.Client{Timeout: 120 * time.Second},
		version:    "2023-06-01",
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.apiKey == "" {
		cfg.apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}

	return func(ctx context.Context, p Prompt) (Response, error) {
		if cfg.apiKey == "" {
			return Response{}, &weft.ArrowError{
				Class: weft.ClassPermanent,
				Op:    "llm.Claude",
				Cause: fmt.Errorf("no API key (set ANTHROPIC_API_KEY or use WithAPIKey)"),
			}
		}

		body, err := buildAnthropicRequest(model, p)
		if err != nil {
			return Response{}, &weft.ArrowError{
				Class: weft.ClassPermanent, // bad input shape; not retryable
				Op:    "llm.Claude",
				Cause: fmt.Errorf("encode request: %w", err),
			}
		}

		req, err := http.NewRequestWithContext(
			ctx, "POST", cfg.apiBase+"/v1/messages", bytes.NewReader(body),
		)
		if err != nil {
			return Response{}, &weft.ArrowError{Class: weft.ClassPermanent, Op: "llm.Claude", Cause: err}
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", cfg.apiKey)
		req.Header.Set("anthropic-version", cfg.version)
		if p.Extra.Anthropic != nil && len(p.Extra.Anthropic.Beta) > 0 {
			req.Header.Set("anthropic-beta", strings.Join(p.Extra.Anthropic.Beta, ","))
		}

		resp, err := cfg.httpClient.Do(req)
		if err != nil {
			class := weft.ClassTransient
			if ctx.Err() != nil {
				class = weft.ClassUserCancelled
			}
			return Response{}, &weft.ArrowError{Class: class, Op: "llm.Claude", Cause: err}
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return Response{}, &weft.ArrowError{Class: weft.ClassTransient, Op: "llm.Claude", Cause: err}
		}

		if resp.StatusCode != 200 {
			return Response{}, classifyHTTPError(resp.StatusCode, respBody)
		}

		return parseAnthropicResponse(respBody)
	}
}

// === Request encoding ========================================================

// anthropicRequest mirrors the Messages API request shape.
type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	StopSeqs    []string           `json:"stop_sequences,omitempty"`
	Thinking    *anthropicThinking `json:"thinking,omitempty"`
}

type anthropicMessage struct {
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content"`
}

type anthropicContent struct {
	Type string `json:"type"`

	// type=text
	Text string `json:"text,omitempty"`

	// type=image
	Source *anthropicImageSource `json:"source,omitempty"`

	// type=tool_use (assistant)
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// type=tool_result (user)
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

type anthropicImageSource struct {
	Type      string `json:"type"` // "base64" or "url"
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

func buildAnthropicRequest(model string, p Prompt) ([]byte, error) {
	maxTokens := p.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	req := anthropicRequest{
		Model:     model,
		MaxTokens: maxTokens,
		System:    p.System,
		Messages:  make([]anthropicMessage, 0, len(p.Messages)),
		StopSeqs:  p.StopSeqs,
	}
	if p.Temperature != 0 {
		t := p.Temperature
		req.Temperature = &t
	}

	// Convert messages.
	for _, m := range p.Messages {
		// Anthropic doesn't accept system messages in the messages list;
		// system prompts go in the top-level System field.
		if m.Role == RoleSystem {
			continue
		}
		role := string(m.Role)
		if m.Role == RoleTool {
			// Tool results are sent as user messages with tool_result content blocks.
			role = "user"
		}
		amsg := anthropicMessage{
			Role:    role,
			Content: blocksToAnthropic(m.Content),
		}
		req.Messages = append(req.Messages, amsg)
	}

	// Convert tools.
	for _, t := range p.Tools {
		req.Tools = append(req.Tools, anthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}

	// Provider-specific extras.
	if p.Extra.Anthropic != nil && p.Extra.Anthropic.ThinkingBudget != nil {
		req.Thinking = &anthropicThinking{
			Type:         "enabled",
			BudgetTokens: *p.Extra.Anthropic.ThinkingBudget,
		}
	}

	return json.Marshal(req)
}

func blocksToAnthropic(blocks []Block) []anthropicContent {
	out := make([]anthropicContent, 0, len(blocks))
	for _, b := range blocks {
		switch b.Kind {
		case BlockText:
			out = append(out, anthropicContent{Type: "text", Text: b.Text})
		case BlockImage:
			src := &anthropicImageSource{}
			if b.ImageURL != "" {
				src.Type = "url"
				src.URL = b.ImageURL
			} else {
				src.Type = "base64"
				src.MediaType = b.MimeType
				// Caller is responsible for base64-encoding into ImageBytes
				// if they choose this path. For inline raw bytes we'd need
				// to encode here; keeping it explicit.
				src.Data = string(b.ImageBytes)
			}
			out = append(out, anthropicContent{Type: "image", Source: src})
		case BlockToolUse:
			out = append(out, anthropicContent{
				Type:  "tool_use",
				ID:    b.ToolUseID,
				Name:  b.ToolName,
				Input: b.ToolInput,
			})
		case BlockToolResult:
			out = append(out, anthropicContent{
				Type:      "tool_result",
				ToolUseID: b.ToolResultID,
				Content:   b.ToolResult,
			})
		case BlockThinking:
			// Thinking blocks aren't sent back to the API as input.
		}
	}
	return out
}

// === Response decoding =======================================================

type anthropicResponse struct {
	ID           string             `json:"id"`
	Type         string             `json:"type"`
	Role         string             `json:"role"`
	Model        string             `json:"model"`
	Content      []anthropicContent `json:"content"`
	StopReason   string             `json:"stop_reason"`
	StopSequence *string            `json:"stop_sequence"`
	Usage        anthropicUsage     `json:"usage"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

func parseAnthropicResponse(body []byte) (Response, error) {
	var ar anthropicResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return Response{}, &weft.ArrowError{
			Class: weft.ClassPermanent,
			Op:    "llm.Claude",
			Cause: fmt.Errorf("decode response: %w (body: %s)", err, truncate(string(body), 200)),
		}
	}

	blocks := make([]Block, 0, len(ar.Content))
	for _, c := range ar.Content {
		switch c.Type {
		case "text":
			blocks = append(blocks, Block{Kind: BlockText, Text: c.Text})
		case "tool_use":
			blocks = append(blocks, Block{
				Kind:      BlockToolUse,
				ToolUseID: c.ID,
				ToolName:  c.Name,
				ToolInput: c.Input,
			})
		case "thinking":
			blocks = append(blocks, Block{Kind: BlockThinking, Thinking: c.Text})
		}
	}

	return Response{
		Messages: []Message{{
			Role:    RoleAssistant,
			Content: blocks,
		}},
		StopReason: stopReasonFromString(ar.StopReason),
		Usage: Usage{
			InputTokens:      ar.Usage.InputTokens,
			OutputTokens:     ar.Usage.OutputTokens,
			CacheReadTokens:  ar.Usage.CacheReadInputTokens,
			CacheWriteTokens: ar.Usage.CacheCreationInputTokens,
		},
		Model: ar.Model,
		RawID: ar.ID,
	}, nil
}

func stopReasonFromString(s string) StopReason {
	switch s {
	case "end_turn":
		return StopEndTurn
	case "max_tokens":
		return StopMaxTokens
	case "stop_sequence":
		return StopSequence
	case "tool_use":
		return StopToolUse
	case "refusal":
		return StopRefusal
	default:
		return StopUnknown
	}
}

// === Error classification ====================================================

func classifyHTTPError(status int, body []byte) error {
	class := weft.ClassUnknown
	switch {
	case status == 429: // rate limit
		class = weft.ClassTransient
	case status == 529: // overloaded
		class = weft.ClassTransient
	case status >= 500: // server errors
		class = weft.ClassTransient
	case status == 401, status == 403: // auth
		class = weft.ClassPermanent
	case status == 400, status == 404, status == 422: // bad request
		class = weft.ClassPermanent
	default:
		class = weft.ClassUnknown
	}
	return &weft.ArrowError{
		Class: class,
		Op:    "llm.Claude",
		Cause: fmt.Errorf("HTTP %d: %s", status, truncate(string(body), 500)),
		Metadata: map[string]any{
			"http_status": status,
		},
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
