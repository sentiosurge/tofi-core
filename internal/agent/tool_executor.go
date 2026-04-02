package agent

import (
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
	"tofi-core/internal/provider"
)

// ToolResult holds the result of a single tool execution.
type ToolResult struct {
	CallID   string
	ToolName string
	Content  string
	Index    int // preserves original order
}

// toolCallClassifier determines whether a tool call can be executed concurrently.
// Tools that modify shared state (filesystem writes, skill loading, shell commands)
// must run sequentially. Read-only or independent API tools can run in parallel.
func isToolConcurrencySafe(name string) bool {
	// Sequential-only tools (mutate state or have side effects)
	switch {
	case name == "tofi_shell":
		return false // shell commands can interfere
	case name == "tofi_wait":
		return false // timing-dependent
	case name == "tofi_load_skill":
		return false // mutates shared tool list
	case name == "file_write":
		return false // filesystem writes
	case strings.HasPrefix(name, "run_skill__"):
		return false // sub-LLM calls that may use sandbox
	}

	// Concurrency-safe tools
	switch {
	case name == "file_read":
		return true
	case name == "file_list":
		return true
	case name == "tofi_update_progress":
		return true
	}

	// MCP tools and extra handlers are generally safe (independent API calls)
	return true
}

// canExecuteInParallel returns true if ALL tool calls in the batch are concurrency-safe.
func canExecuteInParallel(toolCalls []provider.ToolCall) bool {
	for _, tc := range toolCalls {
		if !isToolConcurrencySafe(tc.Name) {
			return false
		}
	}
	return len(toolCalls) > 1
}

// executeToolsParallel runs multiple tool calls concurrently using errgroup.
// Each tool call is executed by the provided executor function.
// Results are returned in the original order of tool calls.
// maxConcurrency limits the number of goroutines (0 = default 5).
func executeToolsParallel(
	toolCalls []provider.ToolCall,
	executor func(tc provider.ToolCall) (string, error),
	maxConcurrency int,
) []ToolResult {
	if maxConcurrency <= 0 {
		maxConcurrency = 5
	}

	results := make([]ToolResult, len(toolCalls))
	var mu sync.Mutex
	g := new(errgroup.Group)
	g.SetLimit(maxConcurrency)

	for i, tc := range toolCalls {
		i, tc := i, tc // capture loop vars
		g.Go(func() error {
			content, err := executor(tc)
			if err != nil {
				content = "Tool error: " + err.Error()
			}

			mu.Lock()
			results[i] = ToolResult{
				CallID:   tc.ID,
				ToolName: tc.Name,
				Content:  content,
				Index:    i,
			}
			mu.Unlock()
			return nil // don't fail the group on tool errors
		})
	}

	g.Wait()
	return results
}
