package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"tofi-core/internal/executor"
	"tofi-core/internal/models"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/tidwall/gjson"
)

type MCP struct{}

// Config Structures
type MCPConfig struct {
	Server struct {
		Transport string   `json:"transport"` // stdio, sse
		Command   string   `json:"command"`
		Args      []string `json:"args"`
		URL       string   `json:"url"`
	} `json:"server"`
	AI struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		BaseURL  string `json:"base_url"`
		APIKey   string `json:"api_key"`
	} `json:"ai"`
	System string `json:"system"`
	Prompt string `json:"prompt"`
}

// OpenAI Tool Schema Definitions
type OpenAITool struct {
	Type     string             `json:"type"`
	Function OpenAIFunctionDef  `json:"function"`
}

type OpenAIFunctionDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters"` // JSON Schema
}

// ReAct Loop Logic
func (m *MCP) Execute(rawConfig map[string]interface{}, ctx *models.ExecutionContext) (string, error) {
	// 1. Parse Config
	var cfg MCPConfig
	configBytes, _ := json.Marshal(rawConfig) // Quick map->struct using json
	if err := json.Unmarshal(configBytes, &cfg); err != nil {
		return "", fmt.Errorf("invalid mcp config: %v", err)
	}

	// Default AI Config
	if cfg.AI.BaseURL == "" {
		if cfg.AI.Provider == "openai" {
			cfg.AI.BaseURL = "https://api.openai.com/v1"
		} else if cfg.AI.Provider == "anthropic" {
			cfg.AI.BaseURL = "https://api.anthropic.com/v1"
		} else if cfg.AI.Provider == "gemini" {
			// handled in loop logic specifically due to different API shape, 
			// but we will stick to OpenAI-compatible interface for now or adapt.
			// Let's assume OpenAI compatible for this initial implementation.
		}
	}
	if cfg.System == "" {
		cfg.System = "You are a helpful assistant with access to external tools. Analyze the user's request, use tools step-by-step, and provide a final answer."
	}

	// 2. Initialize MCP Client
	cli, cleanup, err := m.setupClient(cfg, ctx)
	if err != nil {
		return "", err
	}
	defer cleanup()

	// 3. Handshake & List Tools
	initRequest := mcp.InitializeRequest{}
	initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initRequest.Params.ClientInfo = mcp.Implementation{Name: "tofi-mcp-client", Version: "1.0.0"}
	initRequest.Params.Capabilities = mcp.ClientCapabilities{}

	_, err = cli.Initialize(context.Background(), initRequest)
	if err != nil {
		return "", fmt.Errorf("MCP handshake failed: %v", err)
	}

	toolList, err := cli.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		return "", fmt.Errorf("failed to list tools: %v", err)
	}

	// 4. Convert Tools to OpenAI Format
	openAITools := m.convertTools(toolList.Tools)
	ctx.Log("[Agent] Discovered %d tools", len(openAITools))

	// 5. Start ReAct Loop
	messages := []map[string]interface{}{
		{"role": "system", "content": cfg.System},
		{"role": "user", "content": cfg.Prompt},
	}

	maxSteps := 15
	for step := 1; step <= maxSteps; step++ {
		// Log Thinking
		// ctx.Log("[Think] Step %d/%d...", step, maxSteps) // 稍微减少啰嗦，只在工具调用时输出

		// Call LLM
		respBody, err := m.callLLM(cfg, messages, openAITools)
		if err != nil {
			return "", fmt.Errorf("LLM call failed: %v", err)
		}

		// Parse Response
		// We expect OpenAI format: choices[0].message with optional tool_calls
		content := gjson.Get(respBody, "choices.0.message.content").String()
		toolCalls := gjson.Get(respBody, "choices.0.message.tool_calls")

		// Append Assistant Message
		assistantMsg := map[string]interface{}{
			"role":    "assistant",
			"content": content,
		}
		
		// Handle Tool Calls (Raw JSON to preserve structure for history)
		if toolCalls.Exists() {
			var tcInterface []interface{}
			if err := json.Unmarshal([]byte(toolCalls.Raw), &tcInterface); err == nil {
				assistantMsg["tool_calls"] = tcInterface
			}
		}
		messages = append(messages, assistantMsg)

		// Check for Termination (Content exists, no tools, or explicit stop)
		if !toolCalls.Exists() {
			// If we have content, that's likely the final answer
			if content != "" {
				ctx.Log("[Agent] Finished: %s", truncate(content, 50))
				return content, nil
			}
			// Empty content and no tools? Weird.
			return "", fmt.Errorf("LLM returned empty response without tool calls")
		}

		// Print Reasoning if available
		if content != "" {
			ctx.Log("[Think] %s", content)
		}

		// Execute Tools
		for _, tc := range toolCalls.Array() {
			fnName := tc.Get("function.name").String()
			fnArgs := tc.Get("function.arguments").String()
			callID := tc.Get("id").String()

			// Log precise action
			ctx.Log("[Tool:%s] args=%s", fnName, truncate(fnArgs, 100))

			// Execute via MCP
			var argsMap map[string]interface{}
			if err := json.Unmarshal([]byte(fnArgs), &argsMap); err != nil {
				// Feed error back to LLM
				errMsg := fmt.Sprintf("Error parsing arguments for %s: %v", fnName, err)
				messages = append(messages, map[string]interface{}{
					"role":       "tool",
					"tool_call_id": callID,
					"name":       fnName,
					"content":    errMsg,
				})
				ctx.Log("[Error] %s", errMsg)
				continue
			}

			toolResult, err := cli.CallTool(context.Background(), mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Name:      fnName,
					Arguments: argsMap,
				},
			})

			var outputText string
			if err != nil {
				outputText = fmt.Sprintf("Tool execution error: %v", err)
				ctx.Log("[Result] Error: %v", err)
			} else {
							// MCP returns content list (text/image)
							var sb strings.Builder
							for _, c := range toolResult.Content {
								switch v := c.(type) {
								case mcp.TextContent:
									sb.WriteString(v.Text)
								case mcp.ImageContent:
									sb.WriteString(fmt.Sprintf("[Image: %s]", v.MIMEType))
								case mcp.EmbeddedResource:
									sb.WriteString(fmt.Sprintf("[Resource: %s]", v.Type))
								default:
									sb.WriteString("[Unknown Content]")
								}
							}
				
				outputText = sb.String()
				ctx.Log("[Result] %s", truncate(outputText, 100))
			}

			// Add Tool Output to History
			messages = append(messages, map[string]interface{}{
				"role":       "tool",
				"tool_call_id": callID,
				"name":       fnName,
				"content":    outputText,
			})
		}
	}

	return "", fmt.Errorf("max steps (%d) reached without final answer", maxSteps)
}

// ---------------- Helpers ----------------

func (m *MCP) Validate(node *models.Node) error {
	// Simple validation
	return nil
}

func (m *MCP) setupClient(cfg MCPConfig, ctx *models.ExecutionContext) (*client.Client, func(), error) {
	if cfg.Server.Transport == "stdio" {
		cli, err := client.NewStdioMCPClient(cfg.Server.Command, os.Environ(), cfg.Server.Args...)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create stdio client: %v", err)
		}

		cleanup := func() {
			if err := cli.Close(); err != nil {
				ctx.Log("[Warn] Failed to close MCP client: %v", err)
			}
		}
		return cli, cleanup, nil
	} else if cfg.Server.Transport == "sse" {
		// Placeholder for SSE
		return nil, nil, fmt.Errorf("sse transport not fully implemented in this MVP")
	}

	return nil, nil, fmt.Errorf("unknown transport: %s", cfg.Server.Transport)
}

func (m *MCP) convertTools(mcpTools []mcp.Tool) []OpenAITool {
	var result []OpenAITool
	for _, t := range mcpTools {
		result = append(result, OpenAITool{
			Type: "function",
			Function: OpenAIFunctionDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}
	return result
}

func (m *MCP) callLLM(cfg MCPConfig, messages []map[string]interface{}, tools []OpenAITool) (string, error) {
	headers := map[string]string{
		"Content-Type": "application/json",
		"Authorization": "Bearer " + cfg.AI.APIKey,
	}

	payload := map[string]interface{}{
		"model":    cfg.AI.Model,
		"messages": messages,
	}

	// Only add tools if available
	if len(tools) > 0 {
		payload["tools"] = tools
	}

	url := fmt.Sprintf("%s/chat/completions", strings.TrimRight(cfg.AI.BaseURL, "/"))
	
	// Use executor.PostJSON (standard timeouts apply)
	return executor.PostJSON(url, headers, payload, 120) // 120s timeout for reasoning
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
