package agent

import (
	"tofi-core/internal/provider"
)

// AgentPhase represents the current phase of the agent loop.
type AgentPhase int

const (
	PhaseInit       AgentPhase = iota // Initial state before the first LLM call.
	PhaseThinking                     // Waiting for or processing an LLM response.
	PhaseExecuting                    // Executing tool calls.
	PhaseCompacting                   // Compacting context.
	PhaseDone                         // Loop completed successfully.
	PhaseCancelled                    // Loop was cancelled by client.
	PhaseError                        // Loop encountered a fatal error.
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

// AgentState holds the complete session state of an agent loop.
// Transition methods return new copies (immutable pattern).
type AgentState struct {
	Phase           AgentPhase
	Step            int
	Messages        []provider.Message
	TotalUsage      provider.Usage
	LLMCalls        int
	Tracker         *TokenTracker
	LoadedSkills    map[string]bool
	SystemPrompt    string
	InitialMsgCount int
	Result          string
	Err             error
	Trace           *Trace
	Transcript      *Transcript
}

// NewAgentState creates the initial state for an agent loop.
func NewAgentState(
	systemPrompt string,
	messages []provider.Message,
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
		SystemPrompt:    systemPrompt,
		InitialMsgCount: len(messages),
		Trace:           NewTrace(),
	}
}

// --- Transition methods (each returns a new state) ---

func (s *AgentState) WithPhase(phase AgentPhase) *AgentState {
	next := *s
	next.Phase = phase
	return &next
}

func (s *AgentState) WithStep(step int) *AgentState {
	next := *s
	next.Step = step
	return &next
}

func (s *AgentState) WithMessages(msgs []provider.Message) *AgentState {
	next := *s
	next.Messages = msgs
	return &next
}

func (s *AgentState) WithResult(content string) *AgentState {
	next := *s
	next.Phase = PhaseDone
	next.Result = content
	return &next
}

func (s *AgentState) WithError(err error) *AgentState {
	next := *s
	next.Phase = PhaseError
	next.Err = err
	return &next
}

func (s *AgentState) WithCancelled() *AgentState {
	next := *s
	next.Phase = PhaseCancelled
	return &next
}

func (s *AgentState) WithTranscript(t *Transcript) *AgentState {
	next := *s
	next.Transcript = t
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
		Trace:          s.Trace,
	}
}
