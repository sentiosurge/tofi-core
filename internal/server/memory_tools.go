package server

import (
	"encoding/json"
	"fmt"
	"log"
	"tofi-core/internal/mcp"
	"tofi-core/internal/provider"
	"tofi-core/internal/storage"
)

// buildMemoryTools creates memory_save and memory_recall ExtraBuiltinTools
// that are injected into the Agent via the ExtraTools mechanism.
func (s *Server) buildMemoryTools(userID, cardID string) []mcp.ExtraBuiltinTool {
	return []mcp.ExtraBuiltinTool{
		{
			Schema: provider.Tool{
				Name:        "memory_save",
				Description: "Save information to long-term memory for future reference. Use this to remember user preferences, task outcomes, learned patterns, error solutions, or any knowledge worth retaining across tasks.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"content": map[string]interface{}{
							"type":        "string",
							"description": "What to remember. Be descriptive and use searchable keywords.",
						},
						"tags": map[string]interface{}{
							"type":        "string",
							"description": "Comma-separated tags for categorization (e.g. 'user-preference,python,scripting').",
						},
					},
					"required": []string{"content"},
				},
			},
			Handler: func(args map[string]interface{}) (string, error) {
				content, _ := args["content"].(string)
				if content == "" {
					return "", fmt.Errorf("content is required")
				}
				tags, _ := args["tags"].(string)

				id, err := s.db.SaveMemory(userID, content, tags, "agent", cardID)
				if err != nil {
					return "", fmt.Errorf("failed to save memory: %w", err)
				}

				log.Printf("🧠 [memory] saved #%d for user %s: %.60s", id, userID, content)
				return fmt.Sprintf("Memory saved (id: %d). This information will be available for future tasks.", id), nil
			},
		},
		{
			Schema: provider.Tool{
				Name:        "memory_recall",
				Description: "Search long-term memory for relevant information. Use this at the start of a task to recall user preferences, past learnings, or relevant context.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": "Search keywords to find relevant memories (e.g. 'python preference', 'email setup').",
						},
						"limit": map[string]interface{}{
							"type":        "integer",
							"description": "Maximum number of results to return (default: 5, max: 20).",
						},
					},
					"required": []string{"query"},
				},
			},
			Handler: func(args map[string]interface{}) (string, error) {
				query, _ := args["query"].(string)
				if query == "" {
					return "", fmt.Errorf("query is required")
				}

				limit := 5
				if l, ok := args["limit"].(float64); ok && l > 0 {
					limit = int(l)
				}
				if limit > 20 {
					limit = 20
				}

				memories, err := s.db.RecallMemories(userID, query, limit)
				if err != nil {
					return "", fmt.Errorf("failed to recall memories: %w", err)
				}

				log.Printf("🧠 [memory] recall for user %s: %q → %d results", userID, query, len(memories))

				if len(memories) == 0 {
					return "No relevant memories found.", nil
				}

				// Format memories as JSON for the agent
				result, err := json.Marshal(memories)
				if err != nil {
					return storage.FormatMemoriesForAgent(memories), nil
				}
				return string(result), nil
			},
		},
	}
}
