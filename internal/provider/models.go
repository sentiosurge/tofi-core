package provider

import "strings"

// ModelInfo holds metadata for a known model.
type ModelInfo struct {
	Provider        string  // Provider name (e.g., "openai", "anthropic", "gemini")
	APIType         string  // For OpenAI: "responses" or "chat_completions" (empty = auto)
	ContextWindow   int     // Maximum context window in tokens
	InputCostPer1M  float64 // Cost per 1M input tokens in USD
	OutputCostPer1M float64 // Cost per 1M output tokens in USD
}

// Registry maps model names/prefixes to their metadata.
// For prefix-matched models, we check longest match first.
var Registry = map[string]ModelInfo{
	// ─── OpenAI — Responses API ───
	"o3":        {Provider: "openai", APIType: "responses", ContextWindow: 200000, InputCostPer1M: 2.00, OutputCostPer1M: 8.00},
	"o3-pro":    {Provider: "openai", APIType: "responses", ContextWindow: 200000, InputCostPer1M: 20.00, OutputCostPer1M: 80.00},
	"o4-mini":   {Provider: "openai", APIType: "responses", ContextWindow: 200000, InputCostPer1M: 1.10, OutputCostPer1M: 4.40},
	"gpt-5":      {Provider: "openai", APIType: "responses", ContextWindow: 1047576, InputCostPer1M: 1.25, OutputCostPer1M: 10.00},
	"gpt-5.1":    {Provider: "openai", APIType: "responses", ContextWindow: 1047576, InputCostPer1M: 1.25, OutputCostPer1M: 10.00},
	"gpt-5.2":    {Provider: "openai", APIType: "responses", ContextWindow: 1047576, InputCostPer1M: 1.75, OutputCostPer1M: 14.00},
	"gpt-5.4":    {Provider: "openai", APIType: "responses", ContextWindow: 1047576, InputCostPer1M: 2.50, OutputCostPer1M: 15.00},
	"gpt-5-mini": {Provider: "openai", APIType: "responses", ContextWindow: 1047576, InputCostPer1M: 0.25, OutputCostPer1M: 2.00},
	"gpt-5-nano": {Provider: "openai", APIType: "responses", ContextWindow: 1047576, InputCostPer1M: 0.05, OutputCostPer1M: 0.40},

	// ─── OpenAI — Chat Completions (also work with Responses API) ───
	"gpt-4o":       {Provider: "openai", ContextWindow: 128000, InputCostPer1M: 2.50, OutputCostPer1M: 10.00},
	"gpt-4o-mini":  {Provider: "openai", ContextWindow: 128000, InputCostPer1M: 0.15, OutputCostPer1M: 0.60},
	"gpt-4.1":      {Provider: "openai", ContextWindow: 1047576, InputCostPer1M: 2.00, OutputCostPer1M: 8.00},
	"gpt-4.1-mini": {Provider: "openai", ContextWindow: 1047576, InputCostPer1M: 0.40, OutputCostPer1M: 1.60},
	"gpt-4.1-nano": {Provider: "openai", ContextWindow: 1047576, InputCostPer1M: 0.10, OutputCostPer1M: 0.40},

	// ─── Anthropic ───
	"claude-opus-4-20250514":   {Provider: "anthropic", ContextWindow: 200000, InputCostPer1M: 15.00, OutputCostPer1M: 75.00},
	"claude-sonnet-4-20250514": {Provider: "anthropic", ContextWindow: 200000, InputCostPer1M: 3.00, OutputCostPer1M: 15.00},
	"claude-haiku-4-20250514":  {Provider: "anthropic", ContextWindow: 200000, InputCostPer1M: 0.80, OutputCostPer1M: 4.00},

	// ─── Google Gemini ───
	"gemini-2.5-pro":   {Provider: "gemini", ContextWindow: 1048576, InputCostPer1M: 1.25, OutputCostPer1M: 10.00},
	"gemini-2.5-flash": {Provider: "gemini", ContextWindow: 1048576, InputCostPer1M: 0.15, OutputCostPer1M: 0.60},
	"gemini-2.0-flash": {Provider: "gemini", ContextWindow: 1048576, InputCostPer1M: 0.10, OutputCostPer1M: 0.40},

	// ─── DeepSeek ───
	"deepseek-chat":     {Provider: "deepseek", ContextWindow: 64000, InputCostPer1M: 0.27, OutputCostPer1M: 1.10},
	"deepseek-reasoner": {Provider: "deepseek", ContextWindow: 64000, InputCostPer1M: 0.55, OutputCostPer1M: 2.19},
}

// GetModelInfo returns metadata for a known model.
// It first tries exact match, then prefix match (longest prefix wins).
func GetModelInfo(model string) (ModelInfo, bool) {
	// Exact match
	if info, ok := Registry[model]; ok {
		return info, true
	}

	// Prefix match — find the longest matching prefix
	bestPrefix := ""
	var bestInfo ModelInfo
	for name, info := range Registry {
		if strings.HasPrefix(model, name) && len(name) > len(bestPrefix) {
			bestPrefix = name
			bestInfo = info
		}
	}
	if bestPrefix != "" {
		return bestInfo, true
	}

	return ModelInfo{}, false
}

// DetectProvider infers the provider name from a model name.
func DetectProvider(model string) string {
	m := strings.ToLower(model)

	// Check known prefixes
	switch {
	case strings.HasPrefix(m, "claude"):
		return "anthropic"
	case strings.HasPrefix(m, "gemini"):
		return "gemini"
	case strings.HasPrefix(m, "deepseek"):
		return "deepseek"
	case strings.HasPrefix(m, "llama"), strings.HasPrefix(m, "mistral"), strings.HasPrefix(m, "mixtral"):
		return "ollama" // Common local models
	case strings.HasPrefix(m, "gpt-"), strings.HasPrefix(m, "o1"), strings.HasPrefix(m, "o3"), strings.HasPrefix(m, "o4"):
		return "openai"
	default:
		return "openai" // Default fallback
	}
}

// GetContextWindow returns the context window size for a model.
// Falls back to 128000 for unknown models.
func GetContextWindow(model string) int {
	if info, ok := GetModelInfo(model); ok {
		return info.ContextWindow
	}

	// Heuristic fallbacks
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "claude"):
		return 200000
	case strings.Contains(m, "gemini"):
		return 1048576
	case strings.Contains(m, "deepseek"):
		return 64000
	default:
		return 128000
	}
}

// CalculateCost calculates the USD cost for given token usage on a model.
func CalculateCost(model string, usage Usage) float64 {
	info, ok := GetModelInfo(model)
	if !ok {
		return 0
	}
	inputCost := float64(usage.InputTokens) / 1_000_000 * info.InputCostPer1M
	outputCost := float64(usage.OutputTokens) / 1_000_000 * info.OutputCostPer1M
	return inputCost + outputCost
}

// ListModelsForProvider returns all known models for a given provider.
func ListModelsForProvider(providerName string) []string {
	var models []string
	for name, info := range Registry {
		if info.Provider == providerName {
			models = append(models, name)
		}
	}
	return models
}

// ListAllModels returns all known models with their info.
func ListAllModels() map[string]ModelInfo {
	result := make(map[string]ModelInfo, len(Registry))
	for k, v := range Registry {
		result[k] = v
	}
	return result
}
