package agent

import (
	"tofi-core/internal/provider"
)

// AgentPhase represents the current phase of the agent loop.
type AgentPhase int

const (
	// PhaseInit is the initial state before the first LLM call.
	PhaseInit AgentPhase = iota
	// PhaseThinking means the agent is waiting for or processing an LLM response.
	PhaseThinking
	// PhaseExecuting means the agent is executing tool calls.
	PhaseExecuting
	// PhaseCompacting means the agent is compacting context.
	PhaseCompacting
	// PhaseDone means the agent loop has completed.
	PhaseDone
	// PhaseCancelled means the agent loop was cancelled.
	PhaseCancelled
	// PhaseError means the agent loop encountered a fatal error.
	PhaseError
)

func (p AgentPhase) String() string {
	switch p {
	case PhaseInit:
		return "init"
	case PhaseThinking:
		return "thinking"
	case PhaseExecuting:
		return "executing"
	case PhaseCompacting:
		return "compacting"
	case PhaseDone:
		return "done"
	case PhaseCancelled:
		return "cancelled"
	case PhaseError:
		return "error"
	default:
		return "unknown"
	}
}

// IsTerminal returns true if the phase is a final state.
func (p AgentPhase) IsTerminal() bool {
	return p == PhaseDone || p == PhaseCancelled || p == PhaseError
}

// AgentState holds the complete state of an agent loop iteration.
// Each transition creates a new state (immutable pattern).
type AgentState struct {
	// Phase is the current execution phase.
	Phase AgentPhase

	// Step is the current iteration number (1-based).
	Step int

	// Messages is the conversation history.
	Messages []provider.Message

	// TotalUsage is the cumulative token usage across all API calls.
	TotalUsage provider.Usage

	// LLMCalls is the number of API calls made so far.
	LLMCalls int

	// Tracker provides per-model token/cost breakdown.
	Tracker *TokenTracker

	// LoadedSkills tracks which skills have been activated.
	LoadedSkills map[string]bool

	// AllTools is the current list of available tools (may grow as skills load).
	AllTools []provider.Tool

	// SystemPrompt is the system prompt for API calls.
	SystemPrompt string

	// InitialMsgCount marks where new messages start (for result slicing).
	InitialMsgCount int

	// Result holds the final content when Phase is Done.
	Result string

	// Error holds the error when Phase is Error.
	Err error
}

// NewAgentState creates the initial state for an agent loop.
func NewAgentState(
	systemPrompt string,
	messages []provider.Message,
	tools []provider.Tool,
	loadedSkills map[string]bool,
	model string,
) *AgentState {
	return &AgentState{
		Phase:           PhaseInit,
		Step:            0,
		Messages:        messages,
		TotalUsage:      provider.Usage{},
		LLMCalls:        0,
		Tracker:         NewTokenTracker(model),
		LoadedSkills:    loadedSkills,
		AllTools:        tools,
		SystemPrompt:    systemPrompt,
		InitialMsgCount: len(messages),
	}
}

// WithPhase returns a new state with the phase updated.
func (s *AgentState) WithPhase(phase AgentPhase) *AgentState {
	next := *s
	next.Phase = phase
	return &next
}

// WithStep returns a new state with the step incremented.
func (s *AgentState) WithStep(step int) *AgentState {
	next := *s
	next.Step = step
	return &next
}

// WithMessages returns a new state with updated messages.
func (s *AgentState) WithMessages(msgs []provider.Message) *AgentState {
	next := *s
	next.Messages = msgs
	return &next
}

// WithResult returns a terminal state with the final result.
func (s *AgentState) WithResult(content string) *AgentState {
	next := *s
	next.Phase = PhaseDone
	next.Result = content
	return &next
}

// WithError returns a terminal state with an error.
func (s *AgentState) WithError(err error) *AgentState {
	next := *s
	next.Phase = PhaseError
	next.Err = err
	return &next
}

// RecordAPICall records a successful API call's usage.
func (s *AgentState) RecordAPICall(model string, usage provider.Usage) *AgentState {
	next := *s
	next.LLMCalls++
	next.TotalUsage.Add(usage)
	next.Tracker.RecordUsage(model, usage)
	return &next
}

// AppendMessage returns a new state with a message appended.
func (s *AgentState) AppendMessage(msg provider.Message) *AgentState {
	next := *s
	newMsgs := make([]provider.Message, len(s.Messages)+1)
	copy(newMsgs, s.Messages)
	newMsgs[len(s.Messages)] = msg
	next.Messages = newMsgs
	return &next
}

// NewMessages returns only the messages added after the initial count.
func (s *AgentState) NewMessages() []provider.Message {
	if s.InitialMsgCount >= len(s.Messages) {
		return nil
	}
	return s.Messages[s.InitialMsgCount:]
}

// ToResult converts a terminal state to an AgentResult.
func (s *AgentState) ToResult(model string) *AgentResult {
	return &AgentResult{
		Content:        s.Result,
		TotalUsage:     s.TotalUsage,
		TotalCost:      s.Tracker.TotalCost(),
		Model:          model,
		LLMCalls:       s.LLMCalls,
		LoadedSkills:   mapKeys(s.LoadedSkills),
		Messages:       s.NewMessages(),
		ModelBreakdown: s.Tracker.ModelBreakdown(),
	}
}
