package agent

// Hooks provides pre/post interception points in the agent loop.
// Each hook can inspect, modify, or block the operation.
type Hooks struct {
	// PreToolCall is called before a tool is executed.
	// Return modified input to transform args, or return an error to block execution.
	// If nil, tool executes with original input.
	PreToolCall func(toolName string, input map[string]interface{}) (map[string]interface{}, error)

	// PostToolCall is called after a tool executes successfully.
	// Return modified output to transform the result before appending to messages.
	// If nil, original output is used.
	PostToolCall func(toolName string, input map[string]interface{}, output string) (string, error)

	// PreAPICall is called before each LLM API call.
	// Can inspect or log the request. Return error to abort.
	PreAPICall func(step int, messageCount int, estimatedTokens int) error

	// PostAPICall is called after each LLM API call with usage info.
	PostAPICall func(step int, inputTokens, outputTokens int64, hasToolCalls bool)

	// PreCompact is called before context compaction starts.
	PreCompact func(messageCount int, estimatedTokens int)

	// PostCompact is called after compaction completes.
	PostCompact func(originalMsgs, compactedMsgs int, originalTokens, compactedTokens int)
}

// DefaultHooks returns a Hooks with no-op defaults (all nil = passthrough).
func DefaultHooks() *Hooks {
	return &Hooks{}
}

// callPreToolCall invokes the PreToolCall hook if set.
// Returns the (possibly modified) input and any error.
func (h *Hooks) callPreToolCall(toolName string, input map[string]interface{}) (map[string]interface{}, error) {
	if h == nil || h.PreToolCall == nil {
		return input, nil
	}
	return h.PreToolCall(toolName, input)
}

// callPostToolCall invokes the PostToolCall hook if set.
// Returns the (possibly modified) output.
func (h *Hooks) callPostToolCall(toolName string, input map[string]interface{}, output string) (string, error) {
	if h == nil || h.PostToolCall == nil {
		return output, nil
	}
	return h.PostToolCall(toolName, input, output)
}

// callPreAPICall invokes the PreAPICall hook if set.
func (h *Hooks) callPreAPICall(step, messageCount, estimatedTokens int) error {
	if h == nil || h.PreAPICall == nil {
		return nil
	}
	return h.PreAPICall(step, messageCount, estimatedTokens)
}

// callPostAPICall invokes the PostAPICall hook if set.
func (h *Hooks) callPostAPICall(step int, inputTokens, outputTokens int64, hasToolCalls bool) {
	if h == nil || h.PostAPICall == nil {
		return
	}
	h.PostAPICall(step, inputTokens, outputTokens, hasToolCalls)
}

// callPreCompact invokes the PreCompact hook if set.
func (h *Hooks) callPreCompact(messageCount, estimatedTokens int) {
	if h == nil || h.PreCompact == nil {
		return
	}
	h.PreCompact(messageCount, estimatedTokens)
}

// callPostCompact invokes the PostCompact hook if set.
func (h *Hooks) callPostCompact(originalMsgs, compactedMsgs, originalTokens, compactedTokens int) {
	if h == nil || h.PostCompact == nil {
		return
	}
	h.PostCompact(originalMsgs, compactedMsgs, originalTokens, compactedTokens)
}
