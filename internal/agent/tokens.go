package agent

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"tofi-core/internal/provider"
)

// TokenTracker provides accurate token counting with per-model usage tracking
// and budget management for the agent loop.
type TokenTracker struct {
	mu sync.Mutex

	// Per-model usage breakdown
	modelUsage map[string]*ModelUsage

	// Context window for the current model
	contextWindow int

	// Budget: max tokens per turn (0 = no limit)
	turnBudget int
}

// ModelUsage tracks token consumption for a specific model.
type ModelUsage struct {
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	APICallCount int     `json:"api_call_count"`
}

// NewTokenTracker creates a tracker for the given model.
func NewTokenTracker(model string) *TokenTracker {
	return &TokenTracker{
		modelUsage:    make(map[string]*ModelUsage),
		contextWindow: provider.GetContextWindow(model),
	}
}

// RecordUsage records token usage from an API response.
func (t *TokenTracker) RecordUsage(model string, usage provider.Usage) {
	t.mu.Lock()
	defer t.mu.Unlock()

	mu, ok := t.modelUsage[model]
	if !ok {
		mu = &ModelUsage{}
		t.modelUsage[model] = mu
	}

	mu.InputTokens += usage.InputTokens
	mu.OutputTokens += usage.OutputTokens
	mu.TotalCostUSD += provider.CalculateCost(model, usage)
	mu.APICallCount++
}

// TotalUsage returns aggregated usage across all models.
func (t *TokenTracker) TotalUsage() provider.Usage {
	t.mu.Lock()
	defer t.mu.Unlock()

	var total provider.Usage
	for _, mu := range t.modelUsage {
		total.InputTokens += mu.InputTokens
		total.OutputTokens += mu.OutputTokens
	}
	return total
}

// TotalCost returns total cost in USD across all models.
func (t *TokenTracker) TotalCost() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()

	var total float64
	for _, mu := range t.modelUsage {
		total += mu.TotalCostUSD
	}
	return total
}

// ModelBreakdown returns per-model usage map (copy).
func (t *TokenTracker) ModelBreakdown() map[string]ModelUsage {
	t.mu.Lock()
	defer t.mu.Unlock()

	result := make(map[string]ModelUsage, len(t.modelUsage))
	for model, mu := range t.modelUsage {
		result[model] = *mu
	}
	return result
}

// EstimateContextUsage estimates the total token count for a request
// (system prompt + messages + tool definitions) before sending to the API.
// Returns estimated input tokens.
func EstimateContextUsage(system string, messages []provider.Message, tools []provider.Tool) int {
	total := 0

	// System prompt
	total += estimateStringTokens(system)

	// Messages: content + overhead per message (~4 tokens for role/delimiters)
	for _, msg := range messages {
		total += 4 // message framing overhead
		total += estimateStringTokens(msg.Content)
		for _, tc := range msg.ToolCalls {
			total += 4 // tool call framing
			total += estimateStringTokens(tc.Name)
			total += estimateStringTokens(tc.Arguments)
		}
	}

	// Tool definitions eat context too
	for _, tool := range tools {
		total += estimateToolTokens(tool)
	}

	return total
}

// CheckBudget validates whether the estimated context usage fits within
// the context window, accounting for a reserved output buffer.
// Returns (remainingForOutput, error).
func (t *TokenTracker) CheckBudget(estimatedInput int, minOutputReserve int) (int, error) {
	if minOutputReserve == 0 {
		minOutputReserve = 4096 // default reserve for output
	}

	remaining := t.contextWindow - estimatedInput
	if remaining < minOutputReserve {
		return remaining, fmt.Errorf(
			"context budget exceeded: estimated %d input tokens, context window %d, need %d for output (deficit: %d)",
			estimatedInput, t.contextWindow, minOutputReserve, minOutputReserve-remaining,
		)
	}

	return remaining, nil
}

// ShouldCompact returns true if the estimated input usage exceeds the
// compaction threshold (default 80% of context window).
func (t *TokenTracker) ShouldCompact(estimatedInput int, threshold float64) bool {
	if threshold <= 0 {
		threshold = 0.80
	}
	return estimatedInput > int(float64(t.contextWindow)*threshold)
}

// ContextWindow returns the context window size.
func (t *TokenTracker) ContextWindow() int {
	return t.contextWindow
}

// estimateStringTokens estimates token count for a string.
// Uses ~4 chars per token as baseline, with adjustments for
// code (denser) and CJK text (more tokens per char).
func estimateStringTokens(s string) int {
	if len(s) == 0 {
		return 0
	}

	// Count CJK characters (they typically use more tokens)
	cjkCount := 0
	for _, r := range s {
		if isCJK(r) {
			cjkCount++
		}
	}

	// Base estimate: ~4 chars per token for English/code
	// CJK characters: ~1-2 chars per token
	nonCJKLen := len(s) - cjkCount*3 // UTF-8 CJK chars are 3 bytes
	if nonCJKLen < 0 {
		nonCJKLen = 0
	}
	tokens := nonCJKLen / 4   // English/code: ~4 chars per token
	tokens += cjkCount * 2 / 3 // CJK: ~1.5 chars per token (conservative)

	if tokens == 0 && len(s) > 0 {
		tokens = 1
	}

	return tokens
}

// estimateToolTokens estimates tokens consumed by a tool definition.
func estimateToolTokens(tool provider.Tool) int {
	tokens := 4 // tool framing
	tokens += estimateStringTokens(tool.Name)
	tokens += estimateStringTokens(tool.Description)

	// Parameters schema (JSON serialized)
	if tool.Parameters != nil {
		paramJSON, err := json.Marshal(tool.Parameters)
		if err != nil {
			log.Printf("[tokens] failed to marshal tool params for %s: %v", tool.Name, err)
			tokens += 50 // rough fallback
		} else {
			tokens += estimateStringTokens(string(paramJSON))
		}
	}

	return tokens
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK Unified Ideographs
		(r >= 0x3400 && r <= 0x4DBF) || // CJK Extension A
		(r >= 0x3000 && r <= 0x303F) || // CJK Symbols
		(r >= 0x3040 && r <= 0x30FF) || // Hiragana + Katakana
		(r >= 0xAC00 && r <= 0xD7AF) // Hangul
}
