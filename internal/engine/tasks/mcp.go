package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"time"
	"unsafe"

	"tofi-core/internal/executor"
	"tofi-core/internal/models"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
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
	Type     string            `json:"type"`
	Function OpenAIFunctionDef `json:"function"`
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
		cfg.System = "You are a highly capable autonomous agent. Your goal is to fulfill the user's request using the available tools."
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

	// 4. Convert Tools to OpenAI Format (and add 'wait')
	openAITools := m.convertTools(toolList.Tools)
	ctx.Log("[Agent] Discovered %d tools", len(openAITools))

			// 5. Start ReAct Loop

			systemPrompt := cfg.System + "\n" + `

		You are an autonomous intelligent agent.

		

		### PROTOCOL:

		1. **THINK FIRST**: Start every response with a detailed analysis of the situation and your plan.

		2. **ADAPTABILITY**: If a tool fails, analyze the error and try a different strategy. Do not repeat failed actions.

		3. **VERIFICATION**: Verify the outcome of every action.

		4. **COMPLETION**: Continue until the goal is fully achieved and the system is stable.

		`

		
		messages := []map[string]interface{}{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": cfg.Prompt},
		}
	
		maxSteps := 30
		for step := 1; step <= maxSteps; step++ {
			// Call LLM
			respBody, err := m.callLLM(cfg, messages, openAITools)
			if err != nil {
				return "", fmt.Errorf("LLM call failed: %v", err)
			}
	
			// Parse Response
			// Support both standard 'content' and DeepSeek/OpenAI 'reasoning_content'
			content := gjson.Get(respBody, "choices.0.message.content").String()
			reasoning := gjson.Get(respBody, "choices.0.message.reasoning_content").String()
			toolCalls := gjson.Get(respBody, "choices.0.message.tool_calls")
	
			// Append Assistant Message
			assistantMsg := map[string]interface{}{
				"role":    "assistant",
				"content": content,
			}
			if reasoning != "" {
				assistantMsg["reasoning_content"] = reasoning
			}
			
			// Handle Tool Calls
			if toolCalls.Exists() {
				var tcInterface []interface{}
				if err := json.Unmarshal([]byte(toolCalls.Raw), &tcInterface); err == nil {
					assistantMsg["tool_calls"] = tcInterface
				}
			}
			messages = append(messages, assistantMsg)
	
					// Log Thinking
	
					if reasoning != "" {
	
						ctx.Log("<think>\n%s\n</think>", reasoning)
	
					}
	
					if content != "" {
	
						// If content is just a thought before a tool call, log it as thought
	
						// If it's the final answer (no tools), it will be logged as result later, but logging here is fine too.
	
						ctx.Log("<think>\n%s\n</think>", content)
	
					}
	
			
	
					// Check for Termination (No tools and we have content)
	
					if !toolCalls.Exists() {
	
						if content != "" {
	
							ctx.Log("[Agent] Finished.")
	
							return content, nil
	
						}
	
						return "", fmt.Errorf("LLM returned empty response without tool calls")
	
					}
	
			
	
					// Execute Tools
	
					for _, tc := range toolCalls.Array() {
	
						fnName := tc.Get("function.name").String()
	
						fnArgs := tc.Get("function.arguments").String()
	
						callID := tc.Get("id").String()
	
			
	
						// Log precise action
	
						ctx.Log("<tool_call name=\"%s\">\n%s\n</tool_call>", fnName, fnArgs)
	
			

			// Parse Args
			var argsMap map[string]interface{}
			if err := json.Unmarshal([]byte(fnArgs), &argsMap); err != nil {
				// Feed error back to LLM
				errMsg := fmt.Sprintf("Error parsing arguments for %s: %v", fnName, err)
				messages = append(messages, map[string]interface{}{
					"role":         "tool",
					"tool_call_id": callID,
					"name":         fnName,
					"content":      errMsg,
				})
				ctx.Log("[Error] %s", errMsg)
				continue
			}

			// --- Special Built-in Tool: wait ---
			if fnName == "wait" {
				secVal := 0.0
				if s, ok := argsMap["seconds"].(float64); ok {
					secVal = s
				}
				ctx.Log("[Wait] Sleeping for %.1f seconds...", secVal)
				time.Sleep(time.Duration(secVal * float64(time.Second)))

				messages = append(messages, map[string]interface{}{
					"role":         "tool",
					"tool_call_id": callID,
					"name":         fnName,
					"content":      fmt.Sprintf("Waited for %.1f seconds.", secVal),
				})
				continue
			}
			// -----------------------------------

			// Execute via MCP
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
					
									// Runtime Hint Injection for stubborn agents
									if strings.Contains(outputText, "Could not find option") {
										hint := "\n[SYSTEM HINT]: The 'fill' tool failed (it often requires select options). STOP using 'fill' on this element. Use 'evaluate_script' to set .value directly, or use 'click' and 'press_key'."
										outputText += hint
										ctx.Log("[Hint] Injected runtime guidance for failed fill.")
									}
					
									ctx.Log("[Result] %s", truncate(outputText, 100))
								}
								// Add Tool Output to History
			messages = append(messages, map[string]interface{}{
				"role":         "tool",
				"tool_call_id": callID,
				"name":         fnName,
				"content":      outputText,
			})
		}
	}

	// If we reach here, it means we hit maxSteps
	lastContent := ""
	if len(messages) > 0 {
		if lastMsg, ok := messages[len(messages)-1]["content"].(string); ok {
			lastContent = lastMsg
		}
	}

	if lastContent != "" {
		ctx.Log("[Agent] Max steps reached. Returning partial result.")
		return lastContent, nil
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
		tr := transport.NewStdio(cfg.Server.Command, os.Environ(), cfg.Server.Args...)
		
		if err := tr.Start(context.Background()); err != nil {
			return nil, nil, fmt.Errorf("failed to start stdio transport: %v", err)
		}

		cli := client.NewClient(tr)

		cleanup := func() {
			ctx.Log("[Debug] Closing MCP client...")
			
			// 1. Try graceful close with timeout
			done := make(chan error, 1)
			go func() {
				done <- cli.Close()
			}()

			select {
			case err := <-done:
				if err != nil {
					ctx.Log("[Warn] Failed to close MCP client gracefully: %v", err)
				}
			case <-time.After(1 * time.Second):
				ctx.Log("[Warn] MCP client close timed out, forcing kill...")
			}

			// 2. Force Kill via Reflection (to ensure subprocess is dead)
			// Access private field 'cmd' (*exec.Cmd) in transport.Stdio
			val := reflect.ValueOf(tr).Elem()
			cmdField := val.FieldByName("cmd")
			if cmdField.IsValid() {
				// Use unsafe to bypass unexported field restriction
				// reflect.NewAt creates a pointer to the field, allowing access even if unexported
				cmdPtr := reflect.NewAt(cmdField.Type(), unsafe.Pointer(cmdField.UnsafeAddr())).Elem().Interface()
				
				if cmd, ok := cmdPtr.(*exec.Cmd); ok {
					if cmd.Process != nil {
						if err := cmd.Process.Kill(); err != nil {
							// Ignore "process already finished" errors
							if !strings.Contains(err.Error(), "process already finished") && 
							   !strings.Contains(err.Error(), "no such process") {
								ctx.Log("[Warn] Failed to kill process: %v", err)
							}
						} else {
							ctx.Log("[Debug] Process killed.")
						}
					}
				}
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
		// Robust Sanitization: Force JSON round-trip to ensure map[string]interface{}
		var schemaMap map[string]interface{}

		// Round-trip to normalize types
		// Note: t.InputSchema is a struct, so it's never nil
		jsonBytes, err := json.Marshal(t.InputSchema)
		if err == nil {
			_ = json.Unmarshal(jsonBytes, &schemaMap)
		}

		if schemaMap == nil {
			// Fallback (unlikely)
			schemaMap = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
		}

		// Debug: Inspect list_pages
		if t.Name == "list_pages" {
			log.Printf("[Debug] Validated Schema for list_pages: %+v", schemaMap)
		}

		// Hot-patch 'fill' description to discourage misuse
		description := t.Description
		if t.Name == "fill" {
			description += " (WARNING: Often fails on search inputs. Prefer 'evaluate_script' or 'click'+'press_key' for text fields.)"
		}

		// Fix: Ensure type: object has properties
		typeVal, hasType := schemaMap["type"]
		// If no type, OpenAI assumes object. If type is object, must have properties.
		isObject := !hasType || (hasType && typeVal == "object")

		if isObject {
			if _, hasProps := schemaMap["properties"]; !hasProps {
				schemaMap["properties"] = map[string]interface{}{}
				// Explicitly set type to object to be unambiguous
				schemaMap["type"] = "object"
			}
		}

		result = append(result, OpenAITool{
			Type: "function",
			Function: OpenAIFunctionDef{
				Name:        t.Name,
				Description: description,
				Parameters:  schemaMap,
			},
		})
	}

	// Append Built-in Tools
	result = append(result, OpenAITool{
		Type: "function",
		Function: OpenAIFunctionDef{
			Name:        "wait",
			Description: "Wait for a specified number of seconds. Use this when waiting for page loads, animations, or dynamic content rendering.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"seconds": map[string]interface{}{
						"type":        "number",
						"description": "Number of seconds to wait (e.g., 2.5)",
					},
				},
				"required": []string{"seconds"},
			},
		},
	})

	return result
}

func (m *MCP) callLLM(cfg MCPConfig, messages []map[string]interface{}, tools []OpenAITool) (string, error) {
	headers := map[string]string{
		"Content-Type":  "application/json",
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
