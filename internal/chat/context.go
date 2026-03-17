package chat

import (
	"tofi-core/internal/provider"
)

// Compaction thresholds as fractions of the model's context window.
const (
	// SessionCompactThreshold triggers session-level summarization when
	// the estimated history tokens exceed this fraction of the context window.
	// Applied before sending messages to the LLM.
	SessionCompactThreshold = 0.60

	// AgentCompactThreshold is used inside RunAgentLoop for in-flight compaction
	// when tool calls cause context to grow. Already set to 0.80 in agent.go.
	AgentCompactThreshold = 0.80

	// MaxCompactReduction caps how much a single compaction pass can remove.
	// Prevents over-aggressive summarization that loses important context.
	MaxCompactReduction = 0.50
)

// EstimateTokens provides a rough token count for a slice of chat messages.
// Uses ~4 chars per token heuristic (good enough for triggering compaction).
func EstimateTokens(messages []Message) int {
	total := 0
	for _, msg := range messages {
		total += len(msg.Content) / 4
		for _, tc := range msg.ToolCalls {
			total += len(tc.Input) / 4
		}
	}
	return total
}

// ContextBudget returns the token budget for a model's context window.
func ContextBudget(model string) int {
	return provider.GetContextWindow(model)
}

// ShouldCompact returns true if the message history exceeds the session compaction threshold.
func ShouldCompact(messages []Message, model string) bool {
	budget := ContextBudget(model)
	if budget == 0 {
		return false
	}
	estimated := EstimateTokens(messages)
	threshold := int(float64(budget) * SessionCompactThreshold)
	return estimated > threshold
}

// ContextUsagePercent returns the estimated context usage as a percentage (0-100).
func ContextUsagePercent(inputTokens int64, model string) int {
	budget := ContextBudget(model)
	if budget == 0 {
		return 0
	}
	pct := int(float64(inputTokens) / float64(budget) * 100)
	if pct > 100 {
		pct = 100
	}
	return pct
}
