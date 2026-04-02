package agent

import (
	"testing"

	"tofi-core/internal/provider"
)

func TestEstimateStringTokens(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		minToks int
		maxToks int
	}{
		{"empty", "", 0, 0},
		{"short english", "hello world", 1, 5},
		{"long english", "The quick brown fox jumps over the lazy dog. This is a test sentence.", 10, 25},
		{"chinese text", "这是一个测试句子", 3, 12},
		{"mixed", "Hello 你好 World 世界", 3, 15},
		{"code", "func main() { fmt.Println(\"hello\") }", 5, 15},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := estimateStringTokens(tt.input)
			if got < tt.minToks || got > tt.maxToks {
				t.Errorf("estimateStringTokens(%q) = %d, want [%d, %d]", tt.input, got, tt.minToks, tt.maxToks)
			}
		})
	}
}

func TestEstimateContextUsage(t *testing.T) {
	system := "You are a helpful assistant."
	messages := []provider.Message{
		{Role: "user", Content: "Hello, can you help me?"},
		{Role: "assistant", Content: "Of course! What do you need?"},
		{Role: "user", Content: "Write a function that adds two numbers."},
	}
	tools := []provider.Tool{
		{Name: "calculator", Description: "Performs math operations", Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"expression": map[string]interface{}{
					"type":        "string",
					"description": "math expression",
				},
			},
		}},
	}

	tokens := EstimateContextUsage(system, messages, tools)
	if tokens <= 0 {
		t.Error("estimated tokens should be positive")
	}
	// Sanity check: should be more than just the message text divided by 4
	rawChars := len(system)
	for _, m := range messages {
		rawChars += len(m.Content)
	}
	minExpected := rawChars / 6 // very conservative lower bound
	if tokens < minExpected {
		t.Errorf("estimated %d tokens seems too low for %d chars of content", tokens, rawChars)
	}
}

func TestTokenTracker_RecordUsage(t *testing.T) {
	tracker := NewTokenTracker("gpt-5-mini")

	tracker.RecordUsage("gpt-5-mini", provider.Usage{InputTokens: 100, OutputTokens: 50})
	tracker.RecordUsage("gpt-5-mini", provider.Usage{InputTokens: 200, OutputTokens: 100})
	tracker.RecordUsage("gpt-5", provider.Usage{InputTokens: 500, OutputTokens: 200})

	total := tracker.TotalUsage()
	if total.InputTokens != 800 {
		t.Errorf("expected 800 input tokens, got %d", total.InputTokens)
	}
	if total.OutputTokens != 350 {
		t.Errorf("expected 350 output tokens, got %d", total.OutputTokens)
	}

	breakdown := tracker.ModelBreakdown()
	if len(breakdown) != 2 {
		t.Fatalf("expected 2 models, got %d", len(breakdown))
	}
	if breakdown["gpt-5-mini"].APICallCount != 2 {
		t.Errorf("expected 2 API calls for gpt-5-mini, got %d", breakdown["gpt-5-mini"].APICallCount)
	}
	if breakdown["gpt-5"].APICallCount != 1 {
		t.Errorf("expected 1 API call for gpt-5, got %d", breakdown["gpt-5"].APICallCount)
	}
}

func TestTokenTracker_ShouldCompact(t *testing.T) {
	tracker := NewTokenTracker("gpt-4o") // 128000 context window

	if tracker.ShouldCompact(50000, 0.80) {
		t.Error("50000 should not trigger compact at 80% of 128000")
	}
	if !tracker.ShouldCompact(110000, 0.80) {
		t.Error("110000 should trigger compact at 80% of 128000")
	}
}

func TestTokenTracker_CheckBudget(t *testing.T) {
	tracker := NewTokenTracker("gpt-4o") // 128000

	remaining, err := tracker.CheckBudget(100000, 4096)
	if err != nil {
		t.Errorf("100000 should fit with 4096 reserve: %v", err)
	}
	if remaining != 28000 {
		t.Errorf("expected 28000 remaining, got %d", remaining)
	}

	_, err = tracker.CheckBudget(126000, 4096)
	if err == nil {
		t.Error("126000 should not fit with 4096 reserve in 128000 window")
	}
}

func TestTokenTracker_TotalCost(t *testing.T) {
	tracker := NewTokenTracker("gpt-5-mini")

	tracker.RecordUsage("gpt-5-mini", provider.Usage{InputTokens: 1000000, OutputTokens: 500000})

	cost := tracker.TotalCost()
	if cost <= 0 {
		t.Error("cost should be positive for non-zero usage")
	}
}

func TestIsCJK(t *testing.T) {
	tests := []struct {
		r    rune
		want bool
	}{
		{'A', false},
		{'z', false},
		{'0', false},
		{'中', true},
		{'日', true},
		{'あ', true},  // Hiragana
		{'カ', true},  // Katakana
		{'한', true},  // Hangul
		{'!', false},
	}

	for _, tt := range tests {
		got := isCJK(tt.r)
		if got != tt.want {
			t.Errorf("isCJK(%c) = %v, want %v", tt.r, got, tt.want)
		}
	}
}
