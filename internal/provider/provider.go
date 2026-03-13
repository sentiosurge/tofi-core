// Package provider provides a unified LLM provider abstraction for multiple AI services.
// It supports OpenAI (Responses + Chat Completions), Anthropic Claude, Google Gemini,
// and any OpenAI-compatible provider (Ollama, OpenRouter, Groq, DeepSeek, etc.).
package provider

import (
	"context"
	"fmt"
	"strings"
)

// Provider is the unified interface for LLM API calls.
type Provider interface {
	// Chat sends a non-streaming request and returns the complete response.
	Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error)

	// ChatStream sends a streaming request. onDelta is called with each incremental update.
	// Returns the final aggregated response (content, tool calls, usage).
	ChatStream(ctx context.Context, req *ChatRequest, onDelta func(StreamDelta)) (*ChatResponse, error)
}

// ChatRequest represents a unified chat request across all providers.
type ChatRequest struct {
	Model    string
	System   string    // System prompt (extracted from messages for providers that need it separate)
	Messages []Message // Conversation history
	Tools    []Tool    // Available tools for function calling
}

// Message represents a conversation message in the unified format.
type Message struct {
	Role       string     // "user", "assistant", "tool"
	Content    string     // Text content
	ToolCalls  []ToolCall // For assistant messages: tool calls made
	ToolCallID string     // For tool messages: which call this is responding to
	ToolName   string     // For tool messages: name of the tool
}

// Tool represents a callable function tool.
type Tool struct {
	Name        string
	Description string
	Parameters  interface{} // JSON Schema object
}

// ToolCall represents a tool invocation by the assistant.
type ToolCall struct {
	ID        string // Provider-assigned call ID
	Name      string // Function name
	Arguments string // Raw JSON string of arguments
}

// StreamDelta represents an incremental update during streaming.
type StreamDelta struct {
	Content   string          // Text content delta
	Reasoning string          // Reasoning/thinking content delta
	ToolCalls []ToolCallDelta // Tool call deltas
}

// ToolCallDelta represents an incremental tool call update during streaming.
type ToolCallDelta struct {
	Index     int    // Tool call index (for parallel calls)
	ID        string // Call ID (present in first chunk)
	Name      string // Function name (present in first chunk)
	Arguments string // Arguments JSON delta
}

// ChatResponse represents the aggregated response from an LLM call.
type ChatResponse struct {
	Content   string     // Full text content
	Reasoning string     // Full reasoning/thinking content
	ToolCalls []ToolCall // Completed tool calls
	Usage     Usage      // Token usage statistics
}

// HasToolCalls returns true if the response contains tool calls.
func (r *ChatResponse) HasToolCalls() bool {
	return len(r.ToolCalls) > 0
}

// Usage tracks token consumption for cost calculation.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
}

// Add accumulates usage from another Usage.
func (u *Usage) Add(other Usage) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
}

// Option configures provider creation.
type Option func(*providerConfig)

type providerConfig struct {
	BaseURL string // Custom endpoint URL (for OpenAI-compatible providers)
}

// WithBaseURL sets a custom base URL for the provider.
// Used for OpenAI-compatible providers like Ollama, vLLM, etc.
func WithBaseURL(url string) Option {
	return func(c *providerConfig) {
		c.BaseURL = strings.TrimRight(url, "/")
	}
}

// New creates a Provider instance for the given provider name.
// For OpenAI models, it automatically selects Responses API or Chat Completions
// based on the model name.
//
// Supported provider names:
//   - "openai" — OpenAI Responses API (primary)
//   - "openai_legacy" — OpenAI Chat Completions (explicit legacy)
//   - "anthropic", "claude" — Anthropic Claude Messages API
//   - "gemini" — Google Gemini API
//   - "deepseek", "groq", "openrouter", "together", "ollama" — OpenAI-compatible (Chat Completions)
func New(providerName, apiKey string, opts ...Option) (Provider, error) {
	cfg := &providerConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	switch strings.ToLower(providerName) {
	case "openai":
		return newOpenAIResponses(apiKey, cfg)
	case "openai_legacy":
		return newOpenAILegacy(apiKey, cfg)
	case "anthropic", "claude":
		return newAnthropic(apiKey, cfg)
	case "gemini":
		return newGemini(apiKey, cfg)
	case "deepseek":
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://api.deepseek.com/v1"
		}
		return newOpenAILegacy(apiKey, cfg)
	case "groq":
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://api.groq.com/openai/v1"
		}
		return newOpenAILegacy(apiKey, cfg)
	case "openrouter":
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://openrouter.ai/api/v1"
		}
		return newOpenAILegacy(apiKey, cfg)
	case "together":
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://api.together.xyz/v1"
		}
		return newOpenAILegacy(apiKey, cfg)
	case "ollama":
		if cfg.BaseURL == "" {
			cfg.BaseURL = "http://localhost:11434/v1"
		}
		return newOpenAILegacy(apiKey, cfg)
	default:
		// Assume OpenAI-compatible for unknown providers
		if cfg.BaseURL != "" {
			return newOpenAILegacy(apiKey, cfg)
		}
		return nil, fmt.Errorf("unknown provider: %s (set a custom endpoint with WithBaseURL)", providerName)
	}
}

// NewForModel creates a Provider that's optimized for the given model.
// It auto-detects the provider and API type from the model name.
func NewForModel(model, apiKey string, opts ...Option) (Provider, error) {
	info, ok := GetModelInfo(model)
	if ok {
		// Known model — check if it needs a specific API type
		if info.Provider == "openai" && info.APIType == "responses" {
			return New("openai", apiKey, opts...)
		}
		if info.Provider == "openai" {
			// For known OpenAI models that aren't marked as "responses",
			// still use Responses API (it supports all OpenAI models)
			return New("openai", apiKey, opts...)
		}
		return New(info.Provider, apiKey, opts...)
	}

	// Unknown model — detect provider from name prefix
	prov := DetectProvider(model)
	return New(prov, apiKey, opts...)
}
