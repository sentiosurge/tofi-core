package agent

import (
	"strings"
	"testing"

	"tofi-core/internal/provider"
)

func TestSmartTruncate_Short(t *testing.T) {
	output := "Hello world\nLine 2\nLine 3"
	result := smartTruncate(output, 1000)
	if result != output {
		t.Errorf("short output should not be truncated, got %q", result)
	}
}

func TestSmartTruncate_LongFewLines(t *testing.T) {
	// A single very long line
	output := strings.Repeat("x", 5000)
	result := smartTruncate(output, 1000)
	if len(result) > 1100 { // some overhead for truncation message
		t.Errorf("expected truncated to ~1000 chars, got %d", len(result))
	}
	if !strings.Contains(result, "truncated") {
		t.Error("expected truncation notice")
	}
}

func TestSmartTruncate_LongManyLines(t *testing.T) {
	// Simulate pip install output: many lines
	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, "Downloading package-"+strings.Repeat("x", 50))
	}
	output := strings.Join(lines, "\n")

	result := smartTruncate(output, 2000)

	if len(result) > 2500 {
		t.Errorf("expected roughly 2000 chars, got %d", len(result))
	}

	// Should contain head lines
	if !strings.Contains(result, "Downloading package-") {
		t.Error("should preserve head lines")
	}

	// Should contain omission notice
	if !strings.Contains(result, "lines omitted") {
		t.Error("should contain omission notice")
	}

	// Should contain tail lines too
	parts := strings.Split(result, "lines omitted")
	if len(parts) < 2 {
		t.Error("should have content after omission notice")
	}
}

func TestMicroCompact_ShortConversation(t *testing.T) {
	messages := []provider.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
	result := microCompact(messages, 6)
	if len(result) != 2 {
		t.Errorf("expected 2 messages unchanged, got %d", len(result))
	}
}

func TestMicroCompact_TruncatesOldToolResults(t *testing.T) {
	longOutput := strings.Repeat("data line\n", 200) // ~2000 chars

	messages := []provider.Message{
		{Role: "user", Content: "install packages"},
		{Role: "assistant", Content: "ok", ToolCalls: []provider.ToolCall{{ID: "1", Name: "tofi_shell"}}},
		{Role: "tool", Content: longOutput, ToolCallID: "1", ToolName: "tofi_shell"},
		{Role: "assistant", Content: "installed successfully"},
		{Role: "user", Content: "now run the script"},
		{Role: "assistant", Content: "ok", ToolCalls: []provider.ToolCall{{ID: "2", Name: "tofi_shell"}}},
		{Role: "tool", Content: "script output", ToolCallID: "2", ToolName: "tofi_shell"},
		{Role: "assistant", Content: "done"},
		{Role: "user", Content: "great"},
	}

	result := microCompact(messages, 6)

	// The first tool result (index 2) should be truncated
	if len(result[2].Content) >= len(longOutput) {
		t.Error("old tool result should be truncated")
	}
	if !strings.Contains(result[2].Content, "omitted") {
		t.Error("truncated result should contain 'omitted' notice")
	}

	// Recent tool result (index 6) should be preserved
	if result[6].Content != "script output" {
		t.Errorf("recent tool result should be preserved, got %q", result[6].Content)
	}
}

func TestMicroCompact_PreservesNonToolMessages(t *testing.T) {
	messages := []provider.Message{
		{Role: "user", Content: strings.Repeat("long user message ", 100)},
		{Role: "assistant", Content: strings.Repeat("long assistant response ", 100)},
		// recent messages
		{Role: "user", Content: "short"},
		{Role: "assistant", Content: "short"},
		{Role: "user", Content: "short"},
		{Role: "assistant", Content: "short"},
		{Role: "user", Content: "short"},
		{Role: "assistant", Content: "short"},
	}

	result := microCompact(messages, 6)

	// User and assistant messages should never be truncated by microCompact
	if result[0].Content != messages[0].Content {
		t.Error("user messages should not be truncated by microCompact")
	}
	if result[1].Content != messages[1].Content {
		t.Error("assistant messages should not be truncated by microCompact")
	}
}

func TestCompactAndRebuild(t *testing.T) {
	messages := []provider.Message{
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "first answer"},
		{Role: "user", Content: "second question"},
		{Role: "assistant", Content: "using tools", ToolCalls: []provider.ToolCall{{ID: "1", Name: "shell"}}},
		{Role: "tool", Content: "tool output", ToolCallID: "1"},
		{Role: "assistant", Content: "done with tools"},
		{Role: "user", Content: "last question"},
	}

	result := compactAndRebuild(messages, "Summary of conversation")

	// Should start with summary
	if !strings.Contains(result[0].Content, "Summary of conversation") {
		t.Error("first message should contain summary")
	}
	if result[0].Role != "user" {
		t.Error("summary should be a user message")
	}

	// Should preserve recent messages
	lastMsg := result[len(result)-1]
	if lastMsg.Content != "last question" {
		t.Errorf("should preserve last message, got %q", lastMsg.Content)
	}

	// Should be shorter than original
	if len(result) >= len(messages) {
		t.Errorf("compacted should be shorter: %d >= %d", len(result), len(messages))
	}
}
