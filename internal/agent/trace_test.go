package agent

import (
	"fmt"
	"testing"
	"time"

	"tofi-core/internal/provider"
)

func TestTrace_RecordAPICall(t *testing.T) {
	tr := NewTrace()

	tr.RecordAPICall(1, "gpt-5-mini", provider.Usage{InputTokens: 1000, OutputTokens: 500},
		&provider.ChatResponse{Content: "hello", ToolCalls: []provider.ToolCall{{ID: "1"}}},
		100*time.Millisecond)

	entries := tr.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.Event != "api_call" {
		t.Errorf("expected event 'api_call', got %q", e.Event)
	}
	if e.Step != 1 {
		t.Errorf("expected step 1, got %d", e.Step)
	}
	if e.Detail.Model != "gpt-5-mini" {
		t.Errorf("expected model 'gpt-5-mini', got %q", e.Detail.Model)
	}
	if e.Detail.InputTokens != 1000 {
		t.Errorf("expected 1000 input tokens, got %d", e.Detail.InputTokens)
	}
	if !e.Detail.HasToolCalls {
		t.Error("expected HasToolCalls to be true")
	}
	if e.Detail.ToolCount != 1 {
		t.Errorf("expected 1 tool call, got %d", e.Detail.ToolCount)
	}
	if e.DurationMs != 100 {
		t.Errorf("expected 100ms, got %d", e.DurationMs)
	}
}

func TestTrace_RecordToolExec(t *testing.T) {
	tr := NewTrace()

	tr.RecordToolExec(2, "tofi_shell", `{"command":"echo hello"}`, "hello\n", true, 50*time.Millisecond)

	entries := tr.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.Event != "tool_exec" {
		t.Errorf("expected event 'tool_exec', got %q", e.Event)
	}
	if e.Detail.ToolName != "tofi_shell" {
		t.Errorf("expected tool 'tofi_shell', got %q", e.Detail.ToolName)
	}
	if e.Detail.Success == nil || !*e.Detail.Success {
		t.Error("expected success to be true")
	}
}

func TestTrace_RecordCompact(t *testing.T) {
	tr := NewTrace()

	tr.RecordCompact(5, 50000, 10000, 20, 5, 2*time.Second)

	entries := tr.Entries()
	e := entries[0]
	if e.Event != "compact" {
		t.Errorf("expected event 'compact', got %q", e.Event)
	}
	if e.Detail.OriginalTokens != 50000 {
		t.Errorf("expected 50000 original tokens, got %d", e.Detail.OriginalTokens)
	}
	if e.Detail.CompactedMsgs != 5 {
		t.Errorf("expected 5 compacted msgs, got %d", e.Detail.CompactedMsgs)
	}
}

func TestTrace_Summary(t *testing.T) {
	tr := NewTrace()

	tr.RecordAPICall(1, "gpt-5-mini", provider.Usage{InputTokens: 1000, OutputTokens: 500}, nil, 200*time.Millisecond)
	tr.RecordToolExec(1, "tofi_shell", "{}", "ok", true, 100*time.Millisecond)
	tr.RecordAPICall(2, "gpt-5-mini", provider.Usage{InputTokens: 2000, OutputTokens: 800}, nil, 300*time.Millisecond)
	tr.RecordToolExec(2, "tofi_shell", "{}", "ok", true, 50*time.Millisecond)
	tr.RecordToolExec(2, "tofi_read", "{}", "data", true, 10*time.Millisecond)
	tr.RecordCompact(3, 50000, 10000, 20, 5, 1*time.Second)

	summary := tr.Summary()
	if summary.APICallCount != 2 {
		t.Errorf("expected 2 API calls, got %d", summary.APICallCount)
	}
	if summary.TotalAPIMs != 500 {
		t.Errorf("expected 500ms total API time, got %d", summary.TotalAPIMs)
	}
	if summary.TotalInputTokens != 3000 {
		t.Errorf("expected 3000 input tokens, got %d", summary.TotalInputTokens)
	}
	if summary.ToolExecCount != 3 {
		t.Errorf("expected 3 tool execs, got %d", summary.ToolExecCount)
	}
	if summary.CompactCount != 1 {
		t.Errorf("expected 1 compact, got %d", summary.CompactCount)
	}
	if len(summary.ToolNames) != 2 {
		t.Errorf("expected 2 unique tools, got %d: %v", len(summary.ToolNames), summary.ToolNames)
	}
}

func TestTrace_JSON(t *testing.T) {
	tr := NewTrace()
	tr.RecordAPICall(1, "gpt-5-mini", provider.Usage{InputTokens: 100, OutputTokens: 50}, nil, 10*time.Millisecond)

	data, err := tr.JSON()
	if err != nil {
		t.Fatalf("JSON failed: %v", err)
	}
	if len(data) == 0 {
		t.Error("JSON should not be empty")
	}
}

func TestTrace_EntriesIsCopy(t *testing.T) {
	tr := NewTrace()
	tr.RecordError(1, fmt.Errorf("test error"))

	entries1 := tr.Entries()
	entries2 := tr.Entries()

	// Modifying one copy should not affect the other
	entries1[0].Step = 999
	if entries2[0].Step == 999 {
		t.Error("Entries() should return a copy, not a reference")
	}
}
