package agent

import (
	"encoding/json"
	"sync"
	"time"

	"tofi-core/internal/provider"
)

// Trace records a structured execution log for an agent loop.
// Each step (API call, tool execution, compaction) is recorded with
// timing, token usage, and decision context for observability.
type Trace struct {
	mu      sync.Mutex
	entries []TraceEntry
	startAt time.Time
}

// TraceEntry represents a single observed event in the agent loop.
type TraceEntry struct {
	Step       int           `json:"step"`
	Phase      string        `json:"phase"`
	Timestamp  time.Time     `json:"timestamp"`
	DurationMs int64         `json:"duration_ms"`
	Event      string        `json:"event"`      // "api_call", "tool_exec", "compact", "cancelled", "error"
	Detail     TraceDetail   `json:"detail"`
}

// TraceDetail contains event-specific data.
type TraceDetail struct {
	// For api_call
	Model        string         `json:"model,omitempty"`
	InputTokens  int64          `json:"input_tokens,omitempty"`
	OutputTokens int64          `json:"output_tokens,omitempty"`
	HasToolCalls bool           `json:"has_tool_calls,omitempty"`
	ToolCount    int            `json:"tool_count,omitempty"`

	// For tool_exec
	ToolName     string         `json:"tool_name,omitempty"`
	InputPreview string         `json:"input_preview,omitempty"`  // first 200 chars of args
	OutputPreview string        `json:"output_preview,omitempty"` // first 200 chars of result
	Success      *bool          `json:"success,omitempty"`

	// For compact
	OriginalTokens  int         `json:"original_tokens,omitempty"`
	CompactedTokens int         `json:"compacted_tokens,omitempty"`
	OriginalMsgs    int         `json:"original_msgs,omitempty"`
	CompactedMsgs   int         `json:"compacted_msgs,omitempty"`

	// For error
	ErrorMessage string         `json:"error_message,omitempty"`
}

// NewTrace creates a new trace recorder.
func NewTrace() *Trace {
	return &Trace{
		startAt: time.Now(),
	}
}

// RecordAPICall records an LLM API call event.
func (t *Trace) RecordAPICall(step int, model string, usage provider.Usage, resp *provider.ChatResponse, duration time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	toolCount := 0
	hasCalls := false
	if resp != nil {
		toolCount = len(resp.ToolCalls)
		hasCalls = toolCount > 0
	}

	t.entries = append(t.entries, TraceEntry{
		Step:       step,
		Phase:      PhaseThinking.String(),
		Timestamp:  time.Now(),
		DurationMs: duration.Milliseconds(),
		Event:      "api_call",
		Detail: TraceDetail{
			Model:        model,
			InputTokens:  usage.InputTokens,
			OutputTokens: usage.OutputTokens,
			HasToolCalls: hasCalls,
			ToolCount:    toolCount,
		},
	})
}

// RecordToolExec records a tool execution event.
func (t *Trace) RecordToolExec(step int, toolName, input, output string, success bool, duration time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	inputPreview := input
	if len(inputPreview) > 200 {
		inputPreview = inputPreview[:200] + "..."
	}
	outputPreview := output
	if len(outputPreview) > 200 {
		outputPreview = outputPreview[:200] + "..."
	}

	t.entries = append(t.entries, TraceEntry{
		Step:       step,
		Phase:      PhaseExecuting.String(),
		Timestamp:  time.Now(),
		DurationMs: duration.Milliseconds(),
		Event:      "tool_exec",
		Detail: TraceDetail{
			ToolName:      toolName,
			InputPreview:  inputPreview,
			OutputPreview: outputPreview,
			Success:       &success,
		},
	})
}

// RecordCompact records a context compaction event.
func (t *Trace) RecordCompact(step int, origTokens, compactedTokens, origMsgs, compactedMsgs int, duration time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.entries = append(t.entries, TraceEntry{
		Step:       step,
		Phase:      PhaseCompacting.String(),
		Timestamp:  time.Now(),
		DurationMs: duration.Milliseconds(),
		Event:      "compact",
		Detail: TraceDetail{
			OriginalTokens:  origTokens,
			CompactedTokens: compactedTokens,
			OriginalMsgs:    origMsgs,
			CompactedMsgs:   compactedMsgs,
		},
	})
}

// RecordError records an error event.
func (t *Trace) RecordError(step int, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.entries = append(t.entries, TraceEntry{
		Step:       step,
		Phase:      PhaseError.String(),
		Timestamp:  time.Now(),
		Event:      "error",
		Detail: TraceDetail{
			ErrorMessage: err.Error(),
		},
	})
}

// Entries returns a copy of all trace entries.
func (t *Trace) Entries() []TraceEntry {
	t.mu.Lock()
	defer t.mu.Unlock()

	result := make([]TraceEntry, len(t.entries))
	copy(result, t.entries)
	return result
}

// TotalDuration returns the elapsed time since trace started.
func (t *Trace) TotalDuration() time.Duration {
	return time.Since(t.startAt)
}

// JSON returns the trace as a JSON byte slice.
func (t *Trace) JSON() ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	return json.MarshalIndent(t.entries, "", "  ")
}

// Summary returns a compact human-readable summary of the trace.
func (t *Trace) Summary() TraceSummary {
	t.mu.Lock()
	defer t.mu.Unlock()

	summary := TraceSummary{
		TotalDurationMs: time.Since(t.startAt).Milliseconds(),
	}

	for _, e := range t.entries {
		switch e.Event {
		case "api_call":
			summary.APICallCount++
			summary.TotalAPIMs += e.DurationMs
			summary.TotalInputTokens += e.Detail.InputTokens
			summary.TotalOutputTokens += e.Detail.OutputTokens
		case "tool_exec":
			summary.ToolExecCount++
			summary.TotalToolMs += e.DurationMs
			summary.ToolNames = appendUnique(summary.ToolNames, e.Detail.ToolName)
		case "compact":
			summary.CompactCount++
		case "error":
			summary.ErrorCount++
		}
	}

	return summary
}

// TraceSummary provides aggregate statistics from a trace.
type TraceSummary struct {
	TotalDurationMs  int64    `json:"total_duration_ms"`
	APICallCount     int      `json:"api_call_count"`
	TotalAPIMs       int64    `json:"total_api_ms"`
	TotalInputTokens int64    `json:"total_input_tokens"`
	TotalOutputTokens int64   `json:"total_output_tokens"`
	ToolExecCount    int      `json:"tool_exec_count"`
	TotalToolMs      int64    `json:"total_tool_ms"`
	ToolNames        []string `json:"tool_names"`
	CompactCount     int      `json:"compact_count"`
	ErrorCount       int      `json:"error_count"`
}

func appendUnique(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}
