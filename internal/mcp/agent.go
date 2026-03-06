package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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

// KanbanUpdater 定义更新看板卡片的接口（避免循环引用 storage 包）
type KanbanUpdater interface {
	UpdateKanbanCardBySystem(id string, status string, progress int, result string) error
	AppendKanbanStep(id string, step map[string]interface{}) error
	UpdateKanbanStep(id string, toolName string, status string, result string, durationMs int64) error
}

// SkillTool represents an installed skill callable as a tool in the agent loop
type SkillTool struct {
	ID           string
	Name         string
	Description  string
	Instructions string
	SkillDir     string // Absolute path to skill directory on disk (empty if no scripts)
}

// ExtraBuiltinTool allows registering additional built-in tools with custom handlers
type ExtraBuiltinTool struct {
	Schema  OpenAITool
	Handler func(args map[string]interface{}) (string, error)
}

// AgentConfig holds the configuration required to run an autonomous agent
type AgentConfig struct {
	AI struct {
		Provider string
		Model    string
		Endpoint string
		APIKey   string
	}
	System        string
	Prompt        string
	MCPServers    []MCPServerConfig  // Active MCP server connections
	KanbanCardID  string             // 关联的看板卡片 ID（可选）
	KanbanUpdater KanbanUpdater      // 看板更新器（可选）
	SkillTools    []SkillTool        // Installed skills as callable tools
	ExtraTools    []ExtraBuiltinTool // Additional built-in tools (search_skills, etc.)
	SandboxDir    string             // Sandbox directory for shell command execution (optional)
	UserDir       string             // User persistent directory for installed tools (optional)
	Executor      executor.Executor  // Sandbox executor (nil = use legacy functions)
	OnStreamChunk func(cardID, delta string) // Optional: called with each content delta during streaming
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

	// Add built-in 'update_kanban' tool (if kanban card is associated)
	if cfg.KanbanCardID != "" && cfg.KanbanUpdater != nil {
		allTools = append(allTools, OpenAITool{
			Type: "function",
			Function: OpenAIFunctionDef{
				Name:        "update_kanban",
				Description: "Update the progress of the current task on the Kanban board. Use this to report your progress as you work through the task.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"progress": map[string]interface{}{
							"type":        "number",
							"description": "Progress percentage (0-100)",
						},
						"status": map[string]interface{}{
							"type":        "string",
							"description": "Task status: 'working', 'done', or 'failed'",
							"enum":        []string{"working", "done", "failed"},
						},
						"message": map[string]interface{}{
							"type":        "string",
							"description": "Brief status message or result summary",
						},
					},
					"required": []string{"progress"},
				},
			},
		})
	}

	// Register skill tools (installed skills as callable functions)
	for _, skill := range cfg.SkillTools {
		toolName := "run_skill__" + sanitizeToolName(skill.Name)
		allTools = append(allTools, OpenAITool{
			Type: "function",
			Function: OpenAIFunctionDef{
				Name:        toolName,
				Description: fmt.Sprintf("Execute the '%s' skill: %s", skill.Name, skill.Description),
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"input": map[string]interface{}{
							"type":        "string",
							"description": "The input/request to send to this skill",
						},
					},
					"required": []string{"input"},
				},
			},
		})
	}

	// Register extra built-in tools and their handlers
	extraHandlers := make(map[string]func(args map[string]interface{}) (string, error))
	for _, et := range cfg.ExtraTools {
		allTools = append(allTools, et.Schema)
		extraHandlers[et.Schema.Function.Name] = et.Handler
	}

	// Register sandbox_exec tool (if sandbox is configured)
	if cfg.SandboxDir != "" {
		allTools = append(allTools, OpenAITool{
			Type: "function",
			Function: OpenAIFunctionDef{
				Name: "sandbox_exec",
				Description: "Execute a shell command in an isolated sandbox directory. " +
					"Use this to run npx, uv, pip, node, python, curl, git clone, etc. " +
					"The sandbox is isolated — you cannot access files outside of it. " +
					"Packages installed here (npm, pip) are local to the sandbox.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"command": map[string]interface{}{
							"type":        "string",
							"description": "Shell command to execute (e.g., 'npx create-react-app myapp', 'uv run script.py')",
						},
						"timeout": map[string]interface{}{
							"type":        "number",
							"description": "Timeout in seconds (default: 60, max: 120)",
						},
					},
					"required": []string{"command"},
				},
			},
		})
	}

	// Validate all tools before use
	allTools = validateTools(allTools)

	ctx.Log("[Agent] Discovered %d tools across %d servers (+%d skills, +%d extra)",
		len(allTools)-len(cfg.SkillTools)-len(cfg.ExtraTools), len(activeClients),
		len(cfg.SkillTools), len(cfg.ExtraTools))

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
		var content, reasoning string
		var hasToolCalls bool
		var toolCallsRaw string
		var inputTokens, outputTokens int64

		if cfg.OnStreamChunk != nil {
			// Streaming mode
			sr, err := callLLMStreaming(cfg, messages, allTools, func(delta string) {
				cfg.OnStreamChunk(cfg.KanbanCardID, delta)
			})
			if err != nil {
				return "", fmt.Errorf("LLM call failed: %v", err)
			}
			content = sr.Content
			reasoning = sr.Reasoning
			hasToolCalls = sr.HasToolCalls
			toolCallsRaw = sr.ToolCallsRaw
			inputTokens = sr.InputTokens
			outputTokens = sr.OutputTokens
		} else {
			// Non-streaming fallback
			respBody, err := callLLM(cfg, messages, allTools)
			if err != nil {
				return "", fmt.Errorf("LLM call failed: %v", err)
			}
			content = gjson.Get(respBody, "choices.0.message.content").String()
			reasoning = gjson.Get(respBody, "choices.0.message.reasoning_content").String()
			inputTokens = gjson.Get(respBody, "usage.prompt_tokens").Int()
			outputTokens = gjson.Get(respBody, "usage.completion_tokens").Int()

			tc := gjson.Get(respBody, "choices.0.message.tool_calls")
			if tc.Exists() {
				hasToolCalls = true
				toolCallsRaw = tc.Raw
			}
		}

		// Append Assistant Message
		assistantMsg := map[string]interface{}{
			"role":    "assistant",
			"content": content,
		}
		if reasoning != "" {
			assistantMsg["reasoning_content"] = reasoning
		}
		if hasToolCalls {
			var tcInterface []interface{}
			if err := json.Unmarshal([]byte(toolCallsRaw), &tcInterface); err == nil {
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
		if !hasToolCalls {
			if content != "" {
				// Record the final "Generating Result" step
				if cfg.KanbanCardID != "" && cfg.KanbanUpdater != nil {
					stepData := map[string]interface{}{
						"name":   "Generating Result",
						"status": "done",
					}
					if inputTokens > 0 || outputTokens > 0 {
						stepData["input_tokens"] = inputTokens
						stepData["output_tokens"] = outputTokens
					}
					cfg.KanbanUpdater.AppendKanbanStep(cfg.KanbanCardID, stepData)
				}
				ctx.Log("[Agent] Finished.")
				return content, nil
			}
			return "", fmt.Errorf("LLM returned empty response without tool calls")
		}

		// Execute Tools
		for _, tc := range gjson.Parse(toolCallsRaw).Array() {
			fnName := tc.Get("function.name").String()
			fnArgs := tc.Get("function.arguments").String()
			callID := tc.Get("id").String()

			ctx.Log("<tool_call name=\" %s \">\n%s\n</tool_call>", fnName, fnArgs)

			// Log step to kanban (skip internal tools like wait and update_kanban)
			toolStartTime := time.Now()
			if fnName != "wait" && fnName != "update_kanban" && cfg.KanbanCardID != "" && cfg.KanbanUpdater != nil {
				stepData := map[string]interface{}{
					"name":       fnName,
					"status":     "running",
					"started_at": toolStartTime.UTC().Format("2006-01-02T15:04:05Z"),
				}
				// Include truncated args for display
				if len(fnArgs) > 0 && fnArgs != "{}" {
					argsStr := fnArgs
					if len(argsStr) > 1000 {
						argsStr = argsStr[:1000] + "..."
					}
					stepData["args"] = argsStr
				}
				if inputTokens > 0 || outputTokens > 0 {
					stepData["input_tokens"] = inputTokens
					stepData["output_tokens"] = outputTokens
				}
				cfg.KanbanUpdater.AppendKanbanStep(cfg.KanbanCardID, stepData)
			}

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

			// markStepDone is a helper to update the step status after tool execution
			markStepDone := func(result string) {
				if fnName != "wait" && fnName != "update_kanban" && cfg.KanbanCardID != "" && cfg.KanbanUpdater != nil {
					durationMs := time.Since(toolStartTime).Milliseconds()
					cfg.KanbanUpdater.UpdateKanbanStep(cfg.KanbanCardID, fnName, "done", result, durationMs)
				}
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

			// Handle Built-in 'update_kanban'
			if fnName == "update_kanban" && cfg.KanbanCardID != "" && cfg.KanbanUpdater != nil {
				progress := 0
				if p, ok := argsMap["progress"].(float64); ok {
					progress = int(p)
				}
				status := "working"
				if s, ok := argsMap["status"].(string); ok && s != "" {
					status = s
				}
				message := ""
				if m, ok := argsMap["message"].(string); ok {
					message = m
				}

				err := cfg.KanbanUpdater.UpdateKanbanCardBySystem(cfg.KanbanCardID, status, progress, message)
				resultMsg := fmt.Sprintf("Kanban card updated: status=%s, progress=%d%%", status, progress)
				if err != nil {
					resultMsg = fmt.Sprintf("Failed to update kanban card: %v", err)
				}
				ctx.Log("[Kanban] %s", resultMsg)

				messages = append(messages, map[string]interface{}{
					"role":         "tool",
					"tool_call_id": callID,
					"name":         fnName,
					"content":      resultMsg,
				})
				continue
			}

			// Handle Built-in 'sandbox_exec'
			if fnName == "sandbox_exec" && cfg.SandboxDir != "" {
				command, _ := argsMap["command"].(string)
				timeout := 60
				if t, ok := argsMap["timeout"].(float64); ok && t > 0 && t <= 120 {
					timeout = int(t)
				}

				var resultMsg string
				if err := executor.ValidateCommand(command, cfg.SandboxDir); err != nil {
					resultMsg = "Security violation: " + err.Error()
				} else if cfg.Executor != nil {
					output, err := cfg.Executor.Execute(context.Background(), cfg.SandboxDir, cfg.UserDir, command, timeout)
					if err != nil {
						resultMsg = fmt.Sprintf("Command error: %v\nOutput: %s", err, output)
					} else {
						resultMsg = output
					}
				} else {
					// Legacy fallback (no user directory support)
					output, err := executor.ExecuteInSandbox(context.Background(), cfg.SandboxDir, command, timeout)
					if err != nil {
						resultMsg = fmt.Sprintf("Command error: %v\nOutput: %s", err, output)
					} else {
						resultMsg = output
					}
				}
				ctx.Log("[Sandbox] %s → %s", truncate(command, 80), truncate(resultMsg, 200))
				messages = append(messages, map[string]interface{}{
					"role":         "tool",
					"tool_call_id": callID,
					"name":         fnName,
					"content":      resultMsg,
				})
				markStepDone(resultMsg)
				continue
			}

			// Handle extra built-in tools (search_skills, install_skill, etc.)
			if handler, ok := extraHandlers[fnName]; ok {
				result, err := handler(argsMap)
				resultMsg := ""
				if err != nil {
					resultMsg = fmt.Sprintf("Tool error: %v", err)
				} else {
					resultMsg = result
					// If skill returned commands (code blocks), hint agent to execute them
					if strings.Contains(result, "```") {
						resultMsg += "\n\n[This skill returned suggested commands. Execute them using sandbox_exec to get actual results — do NOT relay these instructions to the user.]"
					}
				}
				ctx.Log("[ExtraTool:%s] %s", fnName, truncate(resultMsg, 200))
				messages = append(messages, map[string]interface{}{
					"role":         "tool",
					"tool_call_id": callID,
					"name":         fnName,
					"content":      resultMsg,
				})
				markStepDone(resultMsg)
				continue
			}

			// Handle skill tools (sub-LLM call with skill instructions)
			if strings.HasPrefix(fnName, "run_skill__") {
				skillKey := strings.TrimPrefix(fnName, "run_skill__")
				var matchedSkill *SkillTool
				for i := range cfg.SkillTools {
					if sanitizeToolName(cfg.SkillTools[i].Name) == skillKey {
						matchedSkill = &cfg.SkillTools[i]
						break
					}
				}
				if matchedSkill != nil {
					input, _ := argsMap["input"].(string)
					ctx.Log("[Skill:%s] Executing with input: %s", matchedSkill.Name, truncate(input, 100))

					// 如果 skill 有脚本目录，在沙箱中创建 symlink
					if matchedSkill.SkillDir != "" && cfg.SandboxDir != "" {
						symlinkDir := filepath.Join(cfg.SandboxDir, "skills")
						os.MkdirAll(symlinkDir, 0755)
						link := filepath.Join(symlinkDir, matchedSkill.Name)
						if _, err := os.Lstat(link); os.IsNotExist(err) {
							if err := os.Symlink(matchedSkill.SkillDir, link); err != nil {
								ctx.Log("[Skill:%s] Warning: failed to create symlink: %v", matchedSkill.Name, err)
							} else {
								ctx.Log("[Skill:%s] Symlinked scripts: skills/%s/ → %s", matchedSkill.Name, matchedSkill.Name, matchedSkill.SkillDir)
							}
						}
					}

					result, err := executeSkillSubCall(cfg, *matchedSkill, input)
					resultMsg := ""
					if err != nil {
						resultMsg = fmt.Sprintf("Skill execution failed: %v", err)
					} else {
						resultMsg = result
					}
					ctx.Log("[Skill:%s] Result: %s", matchedSkill.Name, truncate(resultMsg, 200))
					messages = append(messages, map[string]interface{}{
						"role":         "tool",
						"tool_call_id": callID,
						"name":         fnName,
						"content":      resultMsg,
					})
					markStepDone(resultMsg)
				} else {
					messages = append(messages, map[string]interface{}{
						"role":         "tool",
						"tool_call_id": callID,
						"name":         fnName,
						"content":      fmt.Sprintf("Skill '%s' not found", skillKey),
					})
				}
				continue
			}

			// Find appropriate MCP client
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
			markStepDone(outputText)
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
	// Ensure workspace exists (Artifacts directory)
	// Many MCP servers (like fs-server) will fail to start if the root directory is missing.
	if err := os.MkdirAll(ctx.Paths.Artifacts, 0755); err != nil {
		return nil, nil, fmt.Errorf("failed to create artifacts directory: %v", err)
	}

	workspacePath, _ := filepath.Abs(ctx.Paths.Artifacts)

	processedArgs := make([]string, len(cfg.Args))
	for i, arg := range cfg.Args {
		processedArgs[i] = strings.ReplaceAll(arg, "{{workspace}}", workspacePath)
	}

	// Construct environment variables
	env := os.Environ()
	for k, v := range cfg.Env {
		processedVal := strings.ReplaceAll(v, "{{workspace}}", workspacePath)
		env = append(env, fmt.Sprintf("%s=%s", k, processedVal))
	}

	// Create Transport
	tr := transport.NewStdio(cfg.Command, env, processedArgs...)

	// Note: transport.Stdio doesn't expose a way to set Dir directly easily 
	// because it manages its own *exec.Cmd. 
	// However, we can use reflection hack again or just rely on the absolute path injection.
	// Most MCP servers take the root as an argument.

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

		typeVal, hasType := schemaMap["type"]
		isObject := !hasType || (hasType && typeVal == "object")

		if isObject {
			if _, hasProps := schemaMap["properties"]; !hasProps {
				schemaMap["properties"] = map[string]interface{}{}
				schemaMap["type"] = "object"
			}
		}

		// Sanitize tool name for OpenAI compatibility (a-z, 0-9, _, -)
		name := sanitizeToolName(t.Name)
		if name == "" {
			log.Printf("[Warn] Skipping tool with empty name (original: %q)", t.Name)
			continue
		}
		// OpenAI max function name length is 64
		if len(name) > 64 {
			name = name[:64]
		}

		// Ensure description is not empty
		desc := t.Description
		if desc == "" {
			desc = "Tool: " + name
		}
		// Truncate overly long descriptions (OpenAI has limits)
		if len(desc) > 1024 {
			desc = desc[:1021] + "..."
		}

		result = append(result, OpenAITool{
			Type: "function",
			Function: OpenAIFunctionDef{
				Name:        name,
				Description: desc,
				Parameters:  schemaMap,
			},
		})
	}
	return result
}

// validateTools checks and fixes tool definitions before sending to OpenAI.
func validateTools(tools []OpenAITool) []OpenAITool {
	var valid []OpenAITool
	for _, t := range tools {
		// Ensure function name is valid
		if t.Function.Name == "" {
			continue
		}
		if len(t.Function.Name) > 64 {
			t.Function.Name = t.Function.Name[:64]
		}

		// Ensure parameters is a valid object schema
		if t.Function.Parameters == nil {
			t.Function.Parameters = map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			}
		}
		if params, ok := t.Function.Parameters.(map[string]interface{}); ok {
			if _, hasType := params["type"]; !hasType {
				params["type"] = "object"
			}
			if _, hasProps := params["properties"]; !hasProps {
				params["properties"] = map[string]interface{}{}
			}
		}

		// Ensure description exists
		if t.Function.Description == "" {
			t.Function.Description = "Tool: " + t.Function.Name
		}
		if len(t.Function.Description) > 1024 {
			t.Function.Description = t.Function.Description[:1021] + "..."
		}

		valid = append(valid, t)
	}
	return valid
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
		// Validate tools before sending
		tools = validateTools(tools)
		payload["tools"] = tools
		payload["parallel_tool_calls"] = false // Force sequential execution
	}

	resp, err := executor.PostJSON(cfg.AI.Endpoint, headers, payload, 120)
	if err != nil && strings.Contains(err.Error(), "400") {
		// Log tool names for debugging Invalid tools errors
		var toolNames []string
		for _, t := range tools {
			toolNames = append(toolNames, t.Function.Name)
		}
		log.Printf("[LLM Error 400] Tools sent: %v", toolNames)
		log.Printf("[LLM Error 400] Full error: %s", err.Error())
	}
	return resp, err
}

// StreamingResult holds the aggregated result from a streaming LLM call
type StreamingResult struct {
	Content      string
	Reasoning    string
	ToolCallsRaw string // JSON array of tool_calls (empty if none)
	HasToolCalls bool
	InputTokens  int64
	OutputTokens int64
}

// callLLMStreaming calls the LLM with stream:true and returns aggregated results.
// onChunk is called with each content delta (only when no tool_calls detected).
func callLLMStreaming(cfg AgentConfig, messages []map[string]interface{}, tools []OpenAITool, onChunk func(string)) (*StreamingResult, error) {
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + cfg.AI.APIKey,
	}

	payload := map[string]interface{}{
		"model":    cfg.AI.Model,
		"messages": messages,
		"stream":   true,
		"stream_options": map[string]interface{}{
			"include_usage": true,
		},
	}

	if len(tools) > 0 {
		tools = validateTools(tools)
		payload["tools"] = tools
		payload["parallel_tool_calls"] = false
	}

	result := &StreamingResult{}
	var contentBuf strings.Builder

	// Tool call accumulation: index → {id, name, argsBuf}
	type tcAccum struct {
		ID   string
		Name string
		Args strings.Builder
	}
	toolCallMap := make(map[int]*tcAccum)

	err := executor.PostJSONStream(cfg.AI.Endpoint, headers, payload, 120, func(chunk string) error {
		delta := gjson.Get(chunk, "choices.0.delta")

		// Content delta
		if cd := delta.Get("content"); cd.Exists() && cd.String() != "" {
			text := cd.String()
			contentBuf.WriteString(text)
			if !result.HasToolCalls && onChunk != nil {
				onChunk(text)
			}
		}

		// Reasoning delta
		if rd := delta.Get("reasoning_content"); rd.Exists() && rd.String() != "" {
			result.Reasoning += rd.String()
		}

		// Tool calls delta
		if tc := delta.Get("tool_calls"); tc.Exists() {
			result.HasToolCalls = true
			for _, call := range tc.Array() {
				idx := int(call.Get("index").Int())
				acc, ok := toolCallMap[idx]
				if !ok {
					acc = &tcAccum{}
					toolCallMap[idx] = acc
				}
				if id := call.Get("id"); id.Exists() && id.String() != "" {
					acc.ID = id.String()
				}
				if fn := call.Get("function.name"); fn.Exists() && fn.String() != "" {
					acc.Name = fn.String()
				}
				if args := call.Get("function.arguments"); args.Exists() {
					acc.Args.WriteString(args.String())
				}
			}
		}

		// Usage (last chunk)
		if usage := gjson.Get(chunk, "usage"); usage.Exists() {
			result.InputTokens = usage.Get("prompt_tokens").Int()
			result.OutputTokens = usage.Get("completion_tokens").Int()
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("streaming LLM call failed: %v", err)
	}

	result.Content = contentBuf.String()

	// Reconstruct tool_calls JSON array for compatibility with non-streaming code path
	if result.HasToolCalls && len(toolCallMap) > 0 {
		var tcArray []map[string]interface{}
		for i := 0; i < len(toolCallMap); i++ {
			acc := toolCallMap[i]
			if acc == nil {
				continue
			}
			tcArray = append(tcArray, map[string]interface{}{
				"id":   acc.ID,
				"type": "function",
				"function": map[string]interface{}{
					"name":      acc.Name,
					"arguments": acc.Args.String(),
				},
			})
		}
		raw, _ := json.Marshal(tcArray)
		result.ToolCallsRaw = string(raw)
	}

	return result, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// sanitizeToolName converts a skill name to a valid tool function name
func sanitizeToolName(name string) string {
	result := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, name)
	return strings.ToLower(result)
}

// executeSkillSubCall runs a skill by doing a sub-LLM call with the skill's instructions.
// Returns structured output: message (informational) + commands (to execute in sandbox).
func executeSkillSubCall(cfg AgentConfig, skill SkillTool, input string) (string, error) {
	instructions := skill.Instructions

	// 展开 {baseDir} 占位符为沙箱内的相对路径
	if skill.SkillDir != "" {
		relativePath := "skills/" + skill.Name
		instructions = strings.ReplaceAll(instructions, "{baseDir}", relativePath)
	}

	// 根据是否有脚本目录，生成不同的约束块
	var constraintBlock string
	if skill.SkillDir != "" {
		constraintBlock = fmt.Sprintf(`
SKILL SCRIPTS AVAILABLE at: skills/%s/
- This skill has bundled scripts. Reference them using the relative path above.
  Example: python3 skills/%s/scripts/xxx.py --help
- You may also use inline code (python3 -c "..." or python3 <<'EOF')
- The skills/ directory is READ-ONLY — do NOT write files into it`, skill.Name, skill.Name)
	} else {
		constraintBlock = `
CRITICAL constraints on commands:
- NO script files (no "python3 xxx.py", no "node script.js") — you have NO local files
- Only use: curl, python3 -c "...", python3 <<'EOF', node -e "...", jq, sed, grep -E, xmllint, etc.
- For web scraping: prefer RSS feeds (curl + xmllint/sed/grep) or python3 -c with urllib`
	}

	systemPrompt := instructions + `

---
You are being invoked as a skill by an agent with shell execution capability on macOS.
You MUST respond with a JSON object in this exact format:
{"message": "brief explanation", "commands": ["cmd1", "cmd2"]}

Rules:
- "message": Short description of the result or what the commands do
- "commands": Array of shell commands to execute. Empty [] if you can answer directly.
- Each command must be a single, self-contained shell command
- Return raw JSON only — no markdown code blocks
- NO placeholder API keys (no "YOUR_API_KEY", no "YOUR_TOKEN") — only use free/public endpoints
- NO grep -P (Perl regex) — macOS grep does not support it. Use grep -E or sed instead
` + constraintBlock

	messages := []map[string]interface{}{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": input},
	}
	resp, err := callLLM(cfg, messages, nil)
	if err != nil {
		return "", fmt.Errorf("skill sub-call failed: %v", err)
	}
	content := gjson.Get(resp, "choices.0.message.content").String()
	if content == "" {
		return "Skill returned empty response", nil
	}

	// Try to parse as JSON — extract message and commands
	content = strings.TrimSpace(content)
	// Strip markdown code fences if LLM wrapped it anyway
	if strings.HasPrefix(content, "```") {
		lines := strings.Split(content, "\n")
		if len(lines) >= 3 {
			// Remove first and last lines (``` markers)
			content = strings.Join(lines[1:len(lines)-1], "\n")
			content = strings.TrimSpace(content)
		}
	}

	msg := gjson.Get(content, "message").String()
	cmds := gjson.Get(content, "commands")

	if msg != "" || cmds.Exists() {
		// Successfully parsed structured response
		var result strings.Builder
		if msg != "" {
			result.WriteString(msg)
		}
		if cmds.IsArray() && len(cmds.Array()) > 0 {
			if result.Len() > 0 {
				result.WriteString("\n\n")
			}
			result.WriteString("[COMMANDS TO EXECUTE]\n")
			for _, cmd := range cmds.Array() {
				result.WriteString("$ " + cmd.String() + "\n")
			}
			result.WriteString("\n[Execute these commands using sandbox_exec to get actual results.]")
		}
		return result.String(), nil
	}

	// Fallback: LLM didn't return valid JSON, return raw content with hint
	if strings.Contains(content, "```") {
		return content + "\n\n[This skill returned suggested commands. Execute them using sandbox_exec to get actual results.]", nil
	}
	return content, nil
}
