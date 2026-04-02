package chat

import (
	"fmt"
	"tofi-core/internal/provider"
)

// BuildProviderMessages converts a session's messages into provider.Message format
// for sending to the LLM. If the session has a summary, it prepends a summary
// message and only includes recent messages to stay within token budget.
func BuildProviderMessages(session *Session, newMessage string, model string) []provider.Message {
	var messages []provider.Message

	// If session has a compacted summary, use it as context prefix
	if session.Summary != "" {
		messages = append(messages, provider.Message{
			Role: "user",
			Content: fmt.Sprintf("<context_summary>\n%s\n</context_summary>\n\n"+
				"The above is a summary of our earlier conversation. Continue naturally.", session.Summary),
		})
		// Add a synthetic assistant acknowledgment so the conversation flows
		messages = append(messages, provider.Message{
			Role:    "assistant",
			Content: "Understood, I have the context from our earlier conversation. How can I help?",
		})
	}

	// Convert stored messages to provider format
	storedMessages := convertMessages(session.Messages)

	// Include all messages if within budget; otherwise trim to fit.
	// When a summary exists, it covers old context — recent messages still
	// need to be trimmed to leave room for the new message and response.
	if !ShouldCompact(session.Messages, model) {
		messages = append(messages, storedMessages...)
	} else {
		recent := trimToTokenBudget(storedMessages, session.Messages, model)
		messages = append(messages, recent...)
	}

	// Append the new user message
	if newMessage != "" {
		messages = append(messages, provider.Message{
			Role:    "user",
			Content: newMessage,
		})
	}

	// Validate and repair tool_use/tool_result pairing before sending to API
	messages = repairToolPairing(messages)

	return messages
}

// convertMessages transforms chat.Message slice to provider.Message slice.
func convertMessages(msgs []Message) []provider.Message {
	result := make([]provider.Message, 0, len(msgs))
	for _, msg := range msgs {
		pm := provider.Message{
			Role:       msg.Role,
			Content:    msg.Content,
			ToolCallID: msg.CallID,
			ToolName:   msg.Name,
		}
		for _, tc := range msg.ToolCalls {
			pm.ToolCalls = append(pm.ToolCalls, provider.ToolCall{
				ID:        tc.ID,
				Name:      tc.Name,
				Arguments: tc.Input,
			})
		}
		result = append(result, pm)
	}
	return result
}

// trimToTokenBudget keeps only the most recent messages that fit within
// the session compaction threshold. Ensures tool call/result pairs stay intact.
func trimToTokenBudget(providerMsgs []provider.Message, chatMsgs []Message, model string) []provider.Message {
	budget := ContextBudget(model)
	if budget == 0 {
		return providerMsgs
	}
	targetTokens := int(float64(budget) * SessionCompactThreshold * 0.8) // Leave headroom

	// Walk backwards to find how many messages fit
	total := 0
	cutoff := len(chatMsgs)
	for i := len(chatMsgs) - 1; i >= 0; i-- {
		msgTokens := len(chatMsgs[i].Content) / 4
		for _, tc := range chatMsgs[i].ToolCalls {
			msgTokens += len(tc.Input) / 4
		}
		if total+msgTokens > targetTokens {
			break
		}
		total += msgTokens
		cutoff = i
	}

	// Ensure we don't start in the middle of a tool call sequence
	// Walk backwards from cutoff to find a clean boundary (user or standalone assistant)
	for cutoff > 0 && cutoff < len(chatMsgs) {
		if chatMsgs[cutoff].Role == "user" {
			break
		}
		if chatMsgs[cutoff].Role == "assistant" && len(chatMsgs[cutoff].ToolCalls) == 0 {
			break
		}
		cutoff--
	}

	if cutoff >= len(providerMsgs) {
		// Can't fit anything meaningful — return last 2 messages at minimum
		if len(providerMsgs) > 2 {
			return providerMsgs[len(providerMsgs)-2:]
		}
		return providerMsgs
	}

	return providerMsgs[cutoff:]
}

// repairToolPairing ensures every tool_use (assistant message with ToolCalls)
// has matching tool_result messages, and every tool_result has a corresponding
// tool_use. This prevents API rejections from providers like Anthropic that
// strictly validate pairing.
func repairToolPairing(messages []provider.Message) []provider.Message {
	// Collect all tool call IDs from assistant messages
	expectedResults := make(map[string]bool)
	for _, msg := range messages {
		if msg.Role == "assistant" {
			for _, tc := range msg.ToolCalls {
				if tc.ID != "" {
					expectedResults[tc.ID] = false // not yet seen
				}
			}
		}
	}

	// Mark which results we've seen
	for _, msg := range messages {
		if msg.Role == "tool" && msg.ToolCallID != "" {
			expectedResults[msg.ToolCallID] = true
		}
	}

	// Check if all results are present
	allPaired := true
	for _, seen := range expectedResults {
		if !seen {
			allPaired = false
			break
		}
	}

	if allPaired {
		return messages // no repair needed
	}

	// Repair: add empty tool results for orphaned tool calls
	var result []provider.Message
	for _, msg := range messages {
		result = append(result, msg)

		if msg.Role == "assistant" {
			for _, tc := range msg.ToolCalls {
				if tc.ID != "" && !expectedResults[tc.ID] {
					// This tool call has no result — add a placeholder
					result = append(result, provider.Message{
						Role:       "tool",
						Content:    "[Tool result unavailable — execution was interrupted]",
						ToolCallID: tc.ID,
						ToolName:   tc.Name,
					})
				}
			}
		}
	}

	return result
}
