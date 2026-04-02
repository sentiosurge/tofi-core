package agent

import (
	"testing"

	"tofi-core/internal/provider"
)

func TestAgentPhase_String(t *testing.T) {
	tests := []struct {
		phase AgentPhase
		want  string
	}{
		{PhaseInit, "init"},
		{PhaseThinking, "thinking"},
		{PhaseExecuting, "executing"},
		{PhaseCompacting, "compacting"},
		{PhaseDone, "done"},
		{PhaseCancelled, "cancelled"},
		{PhaseError, "error"},
	}
	for _, tt := range tests {
		if got := tt.phase.String(); got != tt.want {
			t.Errorf("%d.String() = %q, want %q", tt.phase, got, tt.want)
		}
	}
}

func TestAgentPhase_IsTerminal(t *testing.T) {
	terminal := []AgentPhase{PhaseDone, PhaseCancelled, PhaseError}
	nonTerminal := []AgentPhase{PhaseInit, PhaseThinking, PhaseExecuting, PhaseCompacting}

	for _, p := range terminal {
		if !p.IsTerminal() {
			t.Errorf("%s should be terminal", p)
		}
	}
	for _, p := range nonTerminal {
		if p.IsTerminal() {
			t.Errorf("%s should not be terminal", p)
		}
	}
}

func TestAgentState_Immutability(t *testing.T) {
	original := NewAgentState("system", []provider.Message{
		{Role: "user", Content: "hello"},
	}, nil, map[string]bool{}, "gpt-5-mini")

	// WithPhase should not mutate original
	next := original.WithPhase(PhaseThinking)
	if original.Phase != PhaseInit {
		t.Error("original phase mutated")
	}
	if next.Phase != PhaseThinking {
		t.Error("next phase should be thinking")
	}

	// WithStep should not mutate original
	stepped := original.WithStep(5)
	if original.Step != 0 {
		t.Error("original step mutated")
	}
	if stepped.Step != 5 {
		t.Error("stepped should be 5")
	}

	// AppendMessage should not mutate original
	appended := original.AppendMessage(provider.Message{Role: "assistant", Content: "hi"})
	if len(original.Messages) != 1 {
		t.Error("original messages mutated")
	}
	if len(appended.Messages) != 2 {
		t.Error("appended should have 2 messages")
	}
}

func TestAgentState_RecordAPICall(t *testing.T) {
	state := NewAgentState("system", nil, nil, map[string]bool{}, "gpt-5-mini")

	next := state.RecordAPICall("gpt-5-mini", provider.Usage{InputTokens: 100, OutputTokens: 50})

	if state.LLMCalls != 0 {
		t.Error("original LLMCalls mutated")
	}
	if next.LLMCalls != 1 {
		t.Error("next should have 1 LLM call")
	}
	if next.TotalUsage.InputTokens != 100 {
		t.Errorf("expected 100 input tokens, got %d", next.TotalUsage.InputTokens)
	}
}

func TestAgentState_WithResult(t *testing.T) {
	state := NewAgentState("system", nil, nil, map[string]bool{}, "gpt-5-mini")

	done := state.WithResult("final answer")
	if done.Phase != PhaseDone {
		t.Error("result state should be done")
	}
	if done.Result != "final answer" {
		t.Error("result content wrong")
	}
	if !done.Phase.IsTerminal() {
		t.Error("done should be terminal")
	}
}

func TestAgentState_ToResult(t *testing.T) {
	state := NewAgentState("system", []provider.Message{
		{Role: "user", Content: "hello"},
	}, nil, map[string]bool{"web-search": true}, "gpt-5-mini")

	state = state.AppendMessage(provider.Message{Role: "assistant", Content: "hi"})
	state = state.RecordAPICall("gpt-5-mini", provider.Usage{InputTokens: 100, OutputTokens: 50})
	state = state.WithResult("done")

	result := state.ToResult("gpt-5-mini")
	if result.Content != "done" {
		t.Error("result content wrong")
	}
	if result.LLMCalls != 1 {
		t.Error("should have 1 LLM call")
	}
	if len(result.Messages) != 1 {
		t.Errorf("should have 1 new message, got %d", len(result.Messages))
	}
	if len(result.LoadedSkills) != 1 || result.LoadedSkills[0] != "web-search" {
		t.Error("loaded skills wrong")
	}
}

func TestAgentState_NewMessages(t *testing.T) {
	state := NewAgentState("system", []provider.Message{
		{Role: "user", Content: "hello"},
	}, nil, map[string]bool{}, "test")

	// Before any new messages
	if len(state.NewMessages()) != 0 {
		t.Error("should have 0 new messages initially")
	}

	// After appending
	state = state.AppendMessage(provider.Message{Role: "assistant", Content: "hi"})
	state = state.AppendMessage(provider.Message{Role: "user", Content: "bye"})

	newMsgs := state.NewMessages()
	if len(newMsgs) != 2 {
		t.Errorf("expected 2 new messages, got %d", len(newMsgs))
	}
}
