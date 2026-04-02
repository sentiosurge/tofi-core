package chat

import (
	"testing"

	"tofi-core/internal/provider"
)

func TestRepairToolPairing_NothingToRepair(t *testing.T) {
	messages := []provider.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "using tool", ToolCalls: []provider.ToolCall{
			{ID: "call_1", Name: "tofi_shell"},
		}},
		{Role: "tool", Content: "output", ToolCallID: "call_1", ToolName: "tofi_shell"},
		{Role: "assistant", Content: "done"},
	}

	result := repairToolPairing(messages)
	if len(result) != len(messages) {
		t.Errorf("expected same length %d, got %d", len(messages), len(result))
	}
}

func TestRepairToolPairing_MissingToolResult(t *testing.T) {
	messages := []provider.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "using tool", ToolCalls: []provider.ToolCall{
			{ID: "call_1", Name: "tofi_shell"},
		}},
		// Missing tool result for call_1
		{Role: "assistant", Content: "continuing anyway"},
	}

	result := repairToolPairing(messages)

	// Should have inserted a placeholder
	if len(result) != len(messages)+1 {
		t.Fatalf("expected %d messages (1 placeholder added), got %d", len(messages)+1, len(result))
	}

	// Find the placeholder
	found := false
	for _, msg := range result {
		if msg.Role == "tool" && msg.ToolCallID == "call_1" {
			found = true
			if msg.Content == "" {
				t.Error("placeholder should have content")
			}
		}
	}
	if !found {
		t.Error("should have added a placeholder tool result")
	}
}

func TestRepairToolPairing_MultipleToolCalls(t *testing.T) {
	messages := []provider.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "using tools", ToolCalls: []provider.ToolCall{
			{ID: "call_1", Name: "tool_a"},
			{ID: "call_2", Name: "tool_b"},
			{ID: "call_3", Name: "tool_c"},
		}},
		{Role: "tool", Content: "result a", ToolCallID: "call_1", ToolName: "tool_a"},
		// call_2 missing
		{Role: "tool", Content: "result c", ToolCallID: "call_3", ToolName: "tool_c"},
	}

	result := repairToolPairing(messages)

	// Should add placeholder for call_2
	toolResults := 0
	for _, msg := range result {
		if msg.Role == "tool" {
			toolResults++
		}
	}
	if toolResults != 3 {
		t.Errorf("expected 3 tool results (2 real + 1 placeholder), got %d", toolResults)
	}
}

func TestRepairToolPairing_EmptyToolCallID(t *testing.T) {
	// Tool calls without IDs should be ignored
	messages := []provider.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "using tool", ToolCalls: []provider.ToolCall{
			{ID: "", Name: "something"},
		}},
	}

	result := repairToolPairing(messages)
	if len(result) != len(messages) {
		t.Errorf("empty ID tool calls should not trigger repair, got %d messages", len(result))
	}
}

func TestRepairToolPairing_NoToolCalls(t *testing.T) {
	messages := []provider.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
		{Role: "user", Content: "bye"},
	}

	result := repairToolPairing(messages)
	if len(result) != len(messages) {
		t.Error("no tool calls should mean no changes")
	}
}
