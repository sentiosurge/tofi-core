package mcp

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

// AgentConfig holds the configuration required to run an autonomous agent
type AgentConfig struct {
	AI struct {
		Provider string
		Model    string
		Endpoint string
		APIKey   string
	}
	System     string
	Prompt     string
	MCPServers []MCPServerConfig // Active MCP server connections
}

type MCPServerConfig struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
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

// RunAgentLoop executes the autonomous agent loop (ReAct)
// It manages MCP clients, tools, and the LLM interaction loop.
func RunAgentLoop(cfg AgentConfig, ctx *models.ExecutionContext) (string, error) {
	// 1. Initialize MCP Clients
	var activeClients []*client.Client
	var cleanups []func()

	// Cleanup all clients on exit
	defer func() {
		for _, cleanup := range cleanups {
			cleanup()
		}
	}()

	for _, serverCfg := range cfg.MCPServers {
		ctx.Log("[Agent] Connecting to MCP server: %s", serverCfg.Name)
		cli, cleanup, err := setupClient(serverCfg, ctx)
		if err != nil {
			return "", fmt.Errorf("failed to connect to MCP server '%s': %v", serverCfg.Name, err)
		}
		activeClients = append(activeClients, cli)
		cleanups = append(cleanups, cleanup)
	}

	// 2. Handshake & List Tools from ALL clients
	var allTools []OpenAITool
	clientMap := make(map[string]*client.Client) // Map tool name to client

	for i, cli := range activeClients {
		// Handshake
		initRequest := mcp.InitializeRequest{}
		initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
		initRequest.Params.ClientInfo = mcp.Implementation{Name: "tofi-agent", Version: "1.0.0"}
		initRequest.Params.Capabilities = mcp.ClientCapabilities{}

		_, err := cli.Initialize(context.Background(), initRequest)
		if err != nil {
			return "", fmt.Errorf("MCP handshake failed for server %d: %v", i, err)
		}

		// List Tools
		toolList, err := cli.ListTools(context.Background(), mcp.ListToolsRequest{})
		if err != nil {
			return "", fmt.Errorf("failed to list tools for server %d: %v", i, err)
		}

		// Convert and Register
		converted := convertTools(toolList.Tools)
		for _, t := range converted {
			// Check for name collisions? For now, assume unique names or last-win.
			// TODO: Add namespace prefixes if needed (e.g. "chrome__click")
			clientMap[t.Function.Name] = cli
			allTools = append(allTools, t)
		}
	}

	// Add built-in 'wait' tool
	allTools = append(allTools, OpenAITool{
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

	ctx.Log("[Agent] Discovered %d tools across %d servers", len(allTools), len(activeClients))

	// 3. Prepare System Prompt
	if cfg.System == "" {
		cfg.System = "You are an autonomous intelligent agent."
	}
	systemPrompt := cfg.System + "\n" + `
### PROTOCOL:
1. **THINK FIRST**: Start every response with a detailed analysis of the situation and your plan.
   - **INTERNAL MONOLOGUE ONLY**: The content inside <think> is for your internal reasoning. Do NOT address the user or use conversational filler. Keep it analytical and objective.
2. **ADAPTABILITY**: If a tool fails, analyze the error and try a different strategy. Do not repeat failed actions.
3. **VERIFICATION**: Verify the outcome of every action.
4. **COMPLETION**: Continue until the goal is fully achieved and the system is stable.

### DOMAIN KNOWLEDGE:
- **WEB AUTOMATION**: Modern websites often use complex, non-standard input fields that confuse standard 'fill' tools. If 'fill' fails (especially with "option not found"), assume the tool is incompatible. Immediately switch to 'evaluate_script' (to set .value) or 'click' + 'press_key'.
`

	messages := []map[string]interface{}{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": cfg.Prompt},
	}

	// 4. Start Loop
	maxSteps := 30
	for step := 1; step <= maxSteps; step++ {
		// Call LLM
		respBody, err := callLLM(cfg, messages, allTools)
		if err != nil {
			return "", fmt.Errorf("LLM call failed: %v", err)
		}

		// Parse Response
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
			ctx.Log("<think>\n%s\n</think>", content)
		}

		// Check for Termination
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

			ctx.Log("<tool_call name=\" %s \">\n%s\n</tool_call>", fnName, fnArgs)

			// Parse Args
			var argsMap map[string]interface{}
			if err := json.Unmarshal([]byte(fnArgs), &argsMap); err != nil {
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

			// Handle Built-in 'wait'
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

			// Find appropriate client
			cli, exists := clientMap[fnName]
			if !exists {
				errMsg := fmt.Sprintf("Tool '%s' not found.", fnName)
				messages = append(messages, map[string]interface{}{
					"role":         "tool",
					"tool_call_id": callID,
					"name":         fnName,
					"content":      errMsg,
				})
				ctx.Log("[Error] %s", errMsg)
				continue
			}

			// Execute via MCP Client
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
				
				            messages = append(messages, map[string]interface{}{
				
				"role":         "tool",
				"tool_call_id": callID,
				"name":         fnName,
				"content":      outputText,
			})
		}
	}

	// Max steps fallback
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

func setupClient(cfg MCPServerConfig, ctx *models.ExecutionContext) (*client.Client, func(), error) {
	// Construct environment variables
	env := os.Environ()
	for k, v := range cfg.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	tr := transport.NewStdio(cfg.Command, env, cfg.Args...)

	if err := tr.Start(context.Background()); err != nil {
		return nil, nil, fmt.Errorf("failed to start stdio transport: %v", err)
	}

	cli := client.NewClient(tr)

	cleanup := func() {
		ctx.Log("[Debug] Closing MCP client (%s)...", cfg.Name)

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

		// 2. Force Kill via Reflection
		val := reflect.ValueOf(tr).Elem()
		cmdField := val.FieldByName("cmd")
		if cmdField.IsValid() {
			// Use unsafe to bypass unexported field restriction
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
}

func convertTools(mcpTools []mcp.Tool) []OpenAITool {
	var result []OpenAITool
	for _, t := range mcpTools {
		var schemaMap map[string]interface{}
		jsonBytes, err := json.Marshal(t.InputSchema)
		if err == nil {
			_ = json.Unmarshal(jsonBytes, &schemaMap)
		}
		if schemaMap == nil {
			schemaMap = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
		}

		// Debug: Inspect list_pages
		if t.Name == "list_pages" {
			log.Printf("[Debug] Validated Schema for list_pages: %+v", schemaMap)
		}

		typeVal, hasType := schemaMap["type"]
		isObject := !hasType || (hasType && typeVal == "object")

		if isObject {
			if _, hasProps := schemaMap["properties"]; !hasProps {
				schemaMap["properties"] = map[string]interface{}{}
				schemaMap["type"] = "object"
			}
		}

		result = append(result, OpenAITool{
			Type: "function",
			Function: OpenAIFunctionDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  schemaMap,
			},
		})
	}
	return result
}

func callLLM(cfg AgentConfig, messages []map[string]interface{}, tools []OpenAITool) (string, error) {
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + cfg.AI.APIKey,
	}

	payload := map[string]interface{}{
		"model":    cfg.AI.Model,
		"messages": messages,
	}

	if len(tools) > 0 {
		payload["tools"] = tools
	}

	return executor.PostJSON(cfg.AI.Endpoint, headers, payload, 120)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
