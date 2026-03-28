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
	"tofi-core/internal/provider"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// SkillTool represents an installed skill callable as a tool in the agent loop
type SkillTool struct {
	ID           string
	Name         string
	Description  string
	Instructions string
	SkillDir     string             // Absolute path to skill directory on disk (empty if no scripts)
	BundledTools []ExtraBuiltinTool // Tools that come with this skill — activated when skill is loaded
}

// ExtraBuiltinTool allows registering additional built-in tools with custom handlers
type ExtraBuiltinTool struct {
	Schema  provider.Tool
	Handler func(args map[string]interface{}) (string, error)
}

// AgentConfig holds the configuration required to run an autonomous agent
type AgentConfig struct {
	Ctx           context.Context    // Optional: cancellation context (nil = context.Background())
	Provider      provider.Provider  // LLM provider (handles all API format differences)
	Model         string             // Model name (for context window, cost calculation)
	System        string
	Prompt        string
	Messages      []provider.Message // Optional: full conversation history (overrides Prompt if non-empty)
	MCPServers    []MCPServerConfig  // Active MCP server connections
	SessionID     string             // Session/task identifier for streaming callbacks
	SkillTools      []SkillTool        // Installed skills (deferred — loaded on-demand via tofi_load_skill)
	PreloadedSkills []string           // Skills to pre-activate at start (from previous turns in same session)
	ExtraTools      []ExtraBuiltinTool // Core built-in tools (always available)
	SandboxDir    string             // Sandbox directory for shell command execution (optional)
	UserDir       string             // User persistent directory for installed tools (optional)
	Executor      executor.Executor  // Sandbox executor (nil = use legacy functions)
	SecretEnv     map[string]string  // Extra env vars injected into sandbox commands (skill secrets)
	OnStreamChunk    func(sessionID, delta string)                              // Optional: called with each content delta during streaming
	OnThinkingChunk  func(sessionID, delta string)                              // Optional: called with each reasoning/thinking delta during streaming
	OnToolCall       func(toolName, input, output string, durationMs int64)    // Optional: called after each tool execution
	MaxContextTokens int                                                       // 0 = auto-detect from model name
	OnContextCompact func(summary string, originalTokens, compactedTokens int) // Optional: called when context is compacted
	OnProgress       func(status string, progress int, message string)         // Generic progress update
	OnStepStart      func(toolName, args string)                               // Generic step start
	OnStepDone       func(toolName, result string, durationMs int64)           // Generic step done
	LiveUsage        *provider.Usage                                           // Optional: updated in real-time during agent loop for tools to read
}

type MCPServerConfig struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
}

// AgentResult holds the result of an agent loop execution.
type AgentResult struct {
	Content      string
	TotalUsage   provider.Usage
	TotalCost    float64
	Model        string
	LLMCalls     int
	LoadedSkills []string             // Skills that were loaded during this agent loop (for persistence)
	Messages     []provider.Message   // All new messages from this turn (assistant + tool calls + tool responses)
}

// RunAgentLoop executes the autonomous agent loop (ReAct)
// It manages MCP clients, tools, and the LLM interaction loop.
func RunAgentLoop(cfg AgentConfig, ctx *models.ExecutionContext) (*AgentResult, error) {
	if cfg.Provider == nil {
		return nil, fmt.Errorf("provider is required")
	}

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
			return nil, fmt.Errorf("failed to connect to MCP server '%s': %v", serverCfg.Name, err)
		}
		activeClients = append(activeClients, cli)
		cleanups = append(cleanups, cleanup)
	}

	// 2. Handshake & List Tools from ALL clients
	var allTools []provider.Tool
	clientMap := make(map[string]*client.Client) // Map tool name to client

	for i, cli := range activeClients {
		// Handshake
		initRequest := mcp.InitializeRequest{}
		initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
		initRequest.Params.ClientInfo = mcp.Implementation{Name: "tofi-agent", Version: "1.0.0"}
		initRequest.Params.Capabilities = mcp.ClientCapabilities{}

		_, err := cli.Initialize(context.Background(), initRequest)
		if err != nil {
			return nil, fmt.Errorf("MCP handshake failed for server %d: %v", i, err)
		}

		// List Tools
		toolList, err := cli.ListTools(context.Background(), mcp.ListToolsRequest{})
		if err != nil {
			return nil, fmt.Errorf("failed to list tools for server %d: %v", i, err)
		}

		// Convert and Register
		converted := convertTools(toolList.Tools)
		for _, t := range converted {
			clientMap[t.Name] = cli
			allTools = append(allTools, t)
		}
	}

	// Add built-in 'wait' tool
	allTools = append(allTools, provider.Tool{
		Name:        "tofi_wait",
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
	})

	// Add built-in 'update_progress' tool (if progress callback is configured)
	if cfg.OnProgress != nil {
		allTools = append(allTools, provider.Tool{
			Name:        "tofi_update_progress",
			Description: "Update the progress of the current task. Use this to report your progress as you work through the task.",
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
		})
	}

	// Register extra built-in tools and their handlers
	extraHandlers := make(map[string]func(args map[string]interface{}) (string, error))
	for _, et := range cfg.ExtraTools {
		allTools = append(allTools, et.Schema)
		extraHandlers[et.Schema.Name] = et.Handler
	}

	// Track which skills have been loaded (persisted across turns via session)
	loadedSkills := make(map[string]bool)

	// Register skill tools — deferred loading pattern (like Claude Code)
	// Skills are listed by name+description in <available-skills> section of system prompt.
	// Full Instructions loaded on-demand via tofi_load_skill tool.
	if len(cfg.SkillTools) > 0 {
		// Build skill lookup map
		skillMap := make(map[string]*SkillTool)
		for i := range cfg.SkillTools {
			skillMap[cfg.SkillTools[i].Name] = &cfg.SkillTools[i]
		}

		// Pre-activate skills from previous turns in the same session
		for _, preloadName := range cfg.PreloadedSkills {
			if skill, ok := skillMap[preloadName]; ok && !loadedSkills[preloadName] {
				loadedSkills[preloadName] = true
				// Activate bundled tools silently (AI already saw instructions in previous turns)
				for _, bt := range skill.BundledTools {
					alreadyRegistered := false
					for _, t := range allTools {
						if t.Name == bt.Schema.Name {
							alreadyRegistered = true
							break
						}
					}
					if !alreadyRegistered {
						allTools = append(allTools, bt.Schema)
						extraHandlers[bt.Schema.Name] = bt.Handler
					}
				}
				log.Printf("[chat] [Agent] Pre-activated skill '%s' (%d tools)", preloadName, len(skill.BundledTools))
			}
		}

		// Register tofi_load_skill tool — returns full Instructions for a skill
		allTools = append(allTools, provider.Tool{
			Name: "tofi_load_skill",
			Description: "Load the full instructions for a skill by name. " +
				"Skills are listed in <available-skills> with name and description only. " +
				"Call this to get detailed instructions before using the skill. " +
				"After loading, the skill's run_skill__<name> tool becomes available if it has scripts.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Skill name (from <available-skills> list)",
					},
				},
				"required": []string{"name"},
			},
		})

		extraHandlers["tofi_load_skill"] = func(args map[string]interface{}) (string, error) {
			name := strings.TrimSpace(fmt.Sprintf("%v", args["name"]))
			skill, ok := skillMap[name]
			if !ok {
				// Try fuzzy match
				for k, v := range skillMap {
					if strings.EqualFold(k, name) || strings.Contains(strings.ToLower(k), strings.ToLower(name)) {
						skill = v
						name = k
						ok = true
						break
					}
				}
			}
			if !ok {
				var available []string
				for k, v := range skillMap {
					available = append(available, fmt.Sprintf("- %s: %s", k, v.Description))
				}
				return "Skill not found: " + name + "\n\nAvailable skills:\n" + strings.Join(available, "\n"), nil
			}

			// Already loaded — return short confirmation instead of full instructions
			if loadedSkills[name] {
				return fmt.Sprintf("Skill '%s' is already loaded. Its tools are available — use them directly.", name), nil
			}
			loadedSkills[name] = true

			// Activate bundled tools (if any)
			var activatedTools []string
			for _, bt := range skill.BundledTools {
				// Skip if already registered
				alreadyRegistered := false
				for _, t := range allTools {
					if t.Name == bt.Schema.Name {
						alreadyRegistered = true
						break
					}
				}
				if !alreadyRegistered {
					allTools = append(allTools, bt.Schema)
					extraHandlers[bt.Schema.Name] = bt.Handler
					activatedTools = append(activatedTools, bt.Schema.Name)
				}
			}

			// If skill has scripts, create sandbox symlink + register the run_skill tool
			if skill.SkillDir != "" {
				// Create symlink NOW so tofi_shell can find scripts at skills/{name}/
				if cfg.SandboxDir != "" {
					symlinkDir := filepath.Join(cfg.SandboxDir, "skills")
					os.MkdirAll(symlinkDir, 0755)
					link := filepath.Join(symlinkDir, name)
					if _, err := os.Lstat(link); os.IsNotExist(err) {
						if err := os.Symlink(skill.SkillDir, link); err != nil {
							ctx.Log("[Skill:%s] Warning: failed to symlink scripts: %v", name, err)
						} else {
							ctx.Log("[Skill:%s] Symlinked scripts: skills/%s/ → %s", name, name, skill.SkillDir)
						}
					}
				}

				runToolName := "run_skill__" + sanitizeToolName(name)
				alreadyRegistered := false
				for _, t := range allTools {
					if t.Name == runToolName {
						alreadyRegistered = true
						break
					}
				}
				if !alreadyRegistered {
					allTools = append(allTools, provider.Tool{
						Name:        runToolName,
						Description: fmt.Sprintf("Execute the '%s' skill: %s", name, skill.Description),
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
					})
					activatedTools = append(activatedTools, runToolName)
				}
			}

			result := fmt.Sprintf("# Skill: %s\n\n%s", name, skill.Instructions)
			if len(activatedTools) > 0 {
				result += fmt.Sprintf("\n\n---\nActivated tools: %s\nThese tools are now callable.", strings.Join(activatedTools, ", "))
			}
			return result, nil
		}
	}

	// Register tofi_shell + file tools (if sandbox is configured)
	if cfg.SandboxDir != "" {
		allTools = append(allTools, provider.Tool{
			Name: "tofi_shell",
			Description: "Execute a shell command in an isolated sandbox directory (macOS). " +
				"Use this to run python3, node, npx, curl, git clone, etc. " +
				"Install packages with 'python3 -m pip install <pkg>' (NEVER bare 'pip'). " +
				"For multi-line Python use heredoc: python3 <<'PYEOF'\\n...\\nPYEOF. " +
				"The sandbox is isolated — packages persist across tasks.",
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
		})

		// Register file_read and file_write tools
		for _, ft := range buildFileTools(cfg.SandboxDir) {
			allTools = append(allTools, ft.Schema)
			extraHandlers[ft.Schema.Name] = ft.Handler
		}
	}

	// Validate all tools before use
	allTools = validateTools(allTools)

	// Log all registered tool names for debugging
	var toolNames []string
	for _, t := range allTools {
		toolNames = append(toolNames, t.Name)
	}
	ctx.Log("[Agent] Registered %d tools: %s", len(allTools), strings.Join(toolNames, ", "))
	ctx.Log("[Agent] Tools: %d core, %d skills (deferred)", len(cfg.ExtraTools), len(cfg.SkillTools))

	// 3. Prepare System Prompt
	if cfg.System == "" {
		cfg.System = "You are an autonomous intelligent agent."
	}
	systemPrompt := cfg.System

	// Append available skills to system prompt (name + description only)
	if len(cfg.SkillTools) > 0 {
		var skillLines []string
		for _, skill := range cfg.SkillTools {
			skillLines = append(skillLines, fmt.Sprintf("- %s: %s", skill.Name, skill.Description))
		}
		systemPrompt += "\n\n<available-skills>\n" + strings.Join(skillLines, "\n") + "\n</available-skills>\n"
		systemPrompt += `
## Skills
You have skills listed in <available-skills>. Call tofi_load_skill with the skill name to get instructions and activate its tools. Only load when the user's request requires it — not on every message. If you already loaded a skill or have the tools from earlier in the conversation, just use them directly. Never pretend to do something without the right tools.
`
	}

	// Build messages
	var messages []provider.Message
	if len(cfg.Messages) > 0 {
		messages = make([]provider.Message, len(cfg.Messages))
		copy(messages, cfg.Messages)
	} else {
		messages = []provider.Message{
			{Role: "user", Content: cfg.Prompt},
		}
	}

	// 4. Start Loop
	loopCtx := cfg.Ctx
	if loopCtx == nil {
		loopCtx = context.Background()
	}
	initialMsgCount := len(messages) // track where new messages start
	maxSteps := 30
	totalUsage := provider.Usage{}
	llmCalls := 0

	for step := 1; step <= maxSteps; step++ {
		// Check for cancellation before starting a new LLM call
		if loopCtx.Err() != nil {
			ctx.Log("[Agent] Cancelled by client.")
			return &AgentResult{
				Content:      "",
				TotalUsage:   totalUsage,
				TotalCost:    provider.CalculateCost(cfg.Model, totalUsage),
				Model:        cfg.Model,
				LLMCalls:     llmCalls,
				LoadedSkills: mapKeys(loadedSkills),
				Messages:     messages[initialMsgCount:],
			}, nil
		}

		req := &provider.ChatRequest{
			Model:    cfg.Model,
			System:   systemPrompt,
			Messages: messages,
			Tools:    allTools,
		}

		var resp *provider.ChatResponse
		var err error

		if cfg.OnStreamChunk != nil {
			// Streaming mode — wrap callback to filter out <think> blocks
			firstThinkTag := true
			filter := &thinkStreamFilter{
				forward: func(delta string) {
					cfg.OnStreamChunk(cfg.SessionID, delta)
				},
				onThinking: func(delta string) {
					if firstThinkTag {
						ctx.Log("[Agent] Received <think> tag stream")
						firstThinkTag = false
					}
					if cfg.OnThinkingChunk != nil {
						cfg.OnThinkingChunk(cfg.SessionID, delta)
					}
				},
			}
			firstReasoning := true
			resp, err = cfg.Provider.ChatStream(loopCtx, req, func(delta provider.StreamDelta) {
				if delta.Content != "" {
					filter.Write(delta.Content)
				}
				if delta.Reasoning != "" {
					if firstReasoning {
						ctx.Log("[Agent] Received reasoning/thinking stream")
						firstReasoning = false
					}
					if cfg.OnThinkingChunk != nil {
						cfg.OnThinkingChunk(cfg.SessionID, delta.Reasoning)
					}
				}
			})
		} else {
			// Non-streaming mode
			resp, err = cfg.Provider.Chat(loopCtx, req)
		}

		if err != nil {
			// If cancelled by client (ESC), return partial results instead of error
			if loopCtx.Err() != nil {
				ctx.Log("[Agent] Cancelled by client.")
				lastContent := ""
				if resp != nil {
					lastContent = resp.Content
					totalUsage.Add(resp.Usage)
				}
				return &AgentResult{
					Content:      lastContent,
					TotalUsage:   totalUsage,
					TotalCost:    provider.CalculateCost(cfg.Model, totalUsage),
					Model:        cfg.Model,
					LLMCalls:     llmCalls + 1,
					LoadedSkills: mapKeys(loadedSkills),
					Messages:     messages[initialMsgCount:],
				}, nil
			}
			return nil, fmt.Errorf("LLM call failed: %v", err)
		}

		llmCalls++
		totalUsage.Add(resp.Usage)
		if cfg.LiveUsage != nil {
			*cfg.LiveUsage = totalUsage
		}

		// Append Assistant Message
		assistantMsg := provider.Message{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		}
		messages = append(messages, assistantMsg)

		// Log Thinking
		if resp.Reasoning != "" {
			ctx.Log("<think>\n%s\n</think>", resp.Reasoning)
		}
		if resp.Content != "" {
			ctx.Log("<think>\n%s\n</think>", resp.Content)
		}

		// Check for Termination
		if !resp.HasToolCalls() {
			// Strip <think> tags — if the model only returned thinking, it's not a real answer
			cleanContent := stripThinkTags(resp.Content)

			if cleanContent != "" {
				// Record the final "Generating Result" step
				if cfg.OnStepStart != nil {
					cfg.OnStepStart("Generating Result", "")
				}
				ctx.Log("[Agent] Finished.")
				return &AgentResult{
					Content:      cleanContent,
					TotalUsage:   totalUsage,
					TotalCost:    provider.CalculateCost(cfg.Model, totalUsage),
					Model:        cfg.Model,
					LLMCalls:     llmCalls,
					LoadedSkills: mapKeys(loadedSkills),
					Messages:     messages[initialMsgCount:],
				}, nil
			}

			// Content was only <think> tags (model was reasoning but didn't produce a response)
			// Re-prompt the model to continue
			ctx.Log("[Agent] Model returned only <think> content, prompting to continue...")
			messages = append(messages, provider.Message{
				Role:    "user",
				Content: "Please continue. Use the available tools to get the information needed, then provide your answer.",
			})
			continue
		}

		// Execute Tools
		for _, tc := range resp.ToolCalls {
			fnName := tc.Name
			fnArgs := tc.Arguments
			callID := tc.ID

			ctx.Log("<tool_call name=\" %s \">\n%s\n</tool_call>", fnName, fnArgs)

			// Log step start (skip internal tools like wait and update_progress)
			toolStartTime := time.Now()
			if fnName != "tofi_wait" && fnName != "tofi_update_progress" && cfg.OnStepStart != nil {
				argsStr := fnArgs
				if len(argsStr) > 1000 {
					argsStr = argsStr[:1000] + "..."
				}
				cfg.OnStepStart(fnName, argsStr)
			}

			// Parse Args
			var argsMap map[string]interface{}
			if err := json.Unmarshal([]byte(fnArgs), &argsMap); err != nil {
				errMsg := fmt.Sprintf("Error parsing arguments for %s: %v", fnName, err)
				messages = append(messages, provider.Message{
					Role:       "tool",
					Content:    errMsg,
					ToolCallID: callID,
					ToolName:   fnName,
				})
				ctx.Log("[Error] %s", errMsg)
				continue
			}

			// markStepDone is a helper to update the step status after tool execution
			markStepDone := func(result string) {
				durationMs := time.Since(toolStartTime).Milliseconds()
				if fnName != "tofi_wait" && fnName != "tofi_update_progress" && cfg.OnStepDone != nil {
					cfg.OnStepDone(fnName, result, durationMs)
				}
				if cfg.OnToolCall != nil {
					cfg.OnToolCall(fnName, fnArgs, result, durationMs)
				}
			}

			// Handle Built-in 'wait'
			if fnName == "tofi_wait" {
				secVal := 0.0
				if s, ok := argsMap["seconds"].(float64); ok {
					secVal = s
				}
				ctx.Log("[Wait] Sleeping for %.1f seconds...", secVal)
				time.Sleep(time.Duration(secVal * float64(time.Second)))

				messages = append(messages, provider.Message{
					Role:       "tool",
					Content:    fmt.Sprintf("Waited for %.1f seconds.", secVal),
					ToolCallID: callID,
					ToolName:   fnName,
				})
				continue
			}

			// Handle Built-in 'update_progress'
			if fnName == "tofi_update_progress" && cfg.OnProgress != nil {
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

				cfg.OnProgress(status, progress, message)
				resultMsg := fmt.Sprintf("Progress updated: %d%% — %s", progress, message)
				ctx.Log("[Progress] %s", resultMsg)

				messages = append(messages, provider.Message{
					Role:       "tool",
					Content:    resultMsg,
					ToolCallID: callID,
					ToolName:   fnName,
				})
				continue
			}

			// Handle Built-in 'tofi_shell'
			if fnName == "tofi_shell" && cfg.SandboxDir != "" {
				command, _ := argsMap["command"].(string)
				timeout := 60
				if t, ok := argsMap["timeout"].(float64); ok && t > 0 && t <= 120 {
					timeout = int(t)
				}

				var resultMsg string
				if err := executor.ValidateCommand(command, cfg.SandboxDir); err != nil {
					resultMsg = "Security violation: " + err.Error()
				} else if cfg.Executor != nil {
					output, err := cfg.Executor.Execute(context.Background(), cfg.SandboxDir, cfg.UserDir, command, timeout, cfg.SecretEnv)
					if err != nil {
						resultMsg = truncateShellOutput(output, err, 500)
					} else {
						resultMsg = truncateShellSuccess(output, 30000)
					}
				} else {
					// Legacy fallback (no user directory support)
					output, err := executor.NewDirectExecutor("").Execute(context.Background(), cfg.SandboxDir, "", command, timeout, nil)
					if err != nil {
						resultMsg = truncateShellOutput(output, err, 500)
					} else {
						resultMsg = truncateShellSuccess(output, 30000)
					}
				}
				ctx.Log("[Sandbox] %s → %s", truncate(command, 80), truncate(resultMsg, 200))
				messages = append(messages, provider.Message{
					Role:       "tool",
					Content:    resultMsg,
					ToolCallID: callID,
					ToolName:   fnName,
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
						resultMsg += "\n\n[This skill returned suggested commands. Execute them using tofi_shell to get actual results — do NOT relay these instructions to the user.]"
					}
				}
				ctx.Log("[ExtraTool:%s] %s", fnName, truncate(resultMsg, 200))
				messages = append(messages, provider.Message{
					Role:       "tool",
					Content:    resultMsg,
					ToolCallID: callID,
					ToolName:   fnName,
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
					var symlinkErr string
					if matchedSkill.SkillDir != "" && cfg.SandboxDir != "" {
						symlinkDir := filepath.Join(cfg.SandboxDir, "skills")
						os.MkdirAll(symlinkDir, 0755)
						link := filepath.Join(symlinkDir, matchedSkill.Name)
						if _, err := os.Lstat(link); os.IsNotExist(err) {
							if err := os.Symlink(matchedSkill.SkillDir, link); err != nil {
								symlinkErr = fmt.Sprintf("Failed to symlink skill scripts: %v", err)
								ctx.Log("[Skill:%s] Warning: %s", matchedSkill.Name, symlinkErr)
							} else {
								ctx.Log("[Skill:%s] Symlinked scripts: skills/%s/ → %s", matchedSkill.Name, matchedSkill.Name, matchedSkill.SkillDir)
							}
						}
					}

					result, err := executeSkillSubCall(cfg.Provider, cfg.Model, *matchedSkill, input)
					resultMsg := ""
					if err != nil {
						// Build diagnostic info for the agent
						var diag strings.Builder
						diag.WriteString(fmt.Sprintf("Skill '%s' execution failed: %v\n", matchedSkill.Name, err))
						diag.WriteString("\nDiagnostics:\n")
						// Check scripts directory
						if matchedSkill.SkillDir != "" {
							scriptsDir := filepath.Join(matchedSkill.SkillDir, "scripts")
							if _, statErr := os.Stat(scriptsDir); statErr != nil {
								diag.WriteString(fmt.Sprintf("- Scripts directory: MISSING (%s)\n", scriptsDir))
							} else {
								diag.WriteString(fmt.Sprintf("- Scripts directory: exists (%s)\n", scriptsDir))
							}
						} else {
							diag.WriteString("- Scripts directory: N/A (no bundled scripts)\n")
						}
						if symlinkErr != "" {
							diag.WriteString(fmt.Sprintf("- Symlink: FAILED (%s)\n", symlinkErr))
						}
						diag.WriteString("\nSuggestion: Try installing missing dependencies with tofi_shell, or write your own code to accomplish the goal.")
						resultMsg = diag.String()
					} else {
						resultMsg = result
					}
					ctx.Log("[Skill:%s] Result: %s", matchedSkill.Name, truncate(resultMsg, 200))
					messages = append(messages, provider.Message{
						Role:       "tool",
						Content:    resultMsg,
						ToolCallID: callID,
						ToolName:   fnName,
					})
					markStepDone(resultMsg)
				} else {
					messages = append(messages, provider.Message{
						Role:       "tool",
						Content:    fmt.Sprintf("Skill '%s' not found", skillKey),
						ToolCallID: callID,
						ToolName:   fnName,
					})
				}
				continue
			}

			// Find appropriate MCP client
			cli, exists := clientMap[fnName]
			if !exists {
				errMsg := fmt.Sprintf("Tool '%s' not found.", fnName)
				messages = append(messages, provider.Message{
					Role:       "tool",
					Content:    errMsg,
					ToolCallID: callID,
					ToolName:   fnName,
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

			messages = append(messages, provider.Message{
				Role:       "tool",
				Content:    outputText,
				ToolCallID: callID,
				ToolName:   fnName,
			})
			markStepDone(outputText)
		}

		// Context compaction check — after tool execution, before next iteration
		contextWindow := cfg.MaxContextTokens
		if contextWindow == 0 {
			contextWindow = provider.GetContextWindow(cfg.Model)
		}
		compactThreshold := int64(float64(contextWindow) * 0.80)

		if resp.Usage.InputTokens > compactThreshold && len(messages) > 4 {
			ctx.Log("[Agent] Context compaction triggered: %d tokens > %d threshold", resp.Usage.InputTokens, compactThreshold)

			summary, compactErr := compactMessages(cfg.Provider, cfg.Model, messages)
			if compactErr != nil {
				ctx.Log("[Agent] Compaction failed: %v", compactErr)
			} else {
				originalCount := len(messages)
				originalTokens := int(resp.Usage.InputTokens)

				// Find the safe cut point: we need to keep complete tool call/result
				// sequences intact. Walk backwards to find the last assistant message
				// with tool calls, and keep everything from there onwards.
				keepFrom := len(messages) - 2
				if keepFrom < 1 {
					keepFrom = 1
				}
				// Walk backwards to find an assistant message with tool calls
				// and ensure all its tool results are included
				for i := keepFrom; i >= 1; i-- {
					if messages[i].Role == "tool" {
						// This is a tool result — we need to find its parent assistant message
						// Keep walking back to find the assistant message with tool calls
						for j := i - 1; j >= 1; j-- {
							if messages[j].Role == "assistant" && len(messages[j].ToolCalls) > 0 {
								keepFrom = j
								break
							}
						}
						break
					} else if messages[i].Role == "assistant" && len(messages[i].ToolCalls) > 0 {
						// Found an assistant with tool calls — check if all results follow
						keepFrom = i
						break
					}
				}

				kept := make([]provider.Message, len(messages[keepFrom:]))
				copy(kept, messages[keepFrom:])
				messages = []provider.Message{
					{Role: "user", Content: fmt.Sprintf("<context_summary>\n%s\n</context_summary>\n\nThe above is a summary of our conversation so far. Please continue from where we left off.", summary)},
				}
				messages = append(messages, kept...)

				compactedTokens := estimateTokens(messages)
				if cfg.OnContextCompact != nil {
					cfg.OnContextCompact(summary, originalTokens, compactedTokens)
				}
				ctx.Log("[Agent] Compacted: %d messages → %d messages (%d → ~%d tokens)", originalCount, len(messages), originalTokens, compactedTokens)
			}
		}
	}

	// Max steps fallback
	lastContent := ""
	if len(messages) > 0 {
		lastMsg := messages[len(messages)-1]
		if lastMsg.Content != "" {
			lastContent = lastMsg.Content
		}
	}
	if lastContent != "" {
		ctx.Log("[Agent] Max steps reached. Returning partial result.")
		return &AgentResult{
			Content:      lastContent,
			TotalUsage:   totalUsage,
			TotalCost:    provider.CalculateCost(cfg.Model, totalUsage),
			Model:        cfg.Model,
			LLMCalls:     llmCalls,
			LoadedSkills: mapKeys(loadedSkills),
			Messages:     messages[initialMsgCount:],
		}, nil
	}

	return nil, fmt.Errorf("max steps (%d) reached without final answer", maxSteps)
}

// compactMessages uses the same LLM to generate a concise summary of the conversation.
func compactMessages(p provider.Provider, model string, messages []provider.Message) (string, error) {
	var conversationText strings.Builder
	for _, msg := range messages {
		if msg.Content == "" {
			continue
		}
		conversationText.WriteString(fmt.Sprintf("[%s]: %s\n\n", msg.Role, msg.Content))
	}

	req := &provider.ChatRequest{
		Model:  model,
		System: "You are a helpful assistant that creates concise conversation summaries.",
		Messages: []provider.Message{
			{Role: "user", Content: fmt.Sprintf(
				"Summarize the following conversation concisely. Preserve:\n"+
					"1. Key decisions and conclusions\n"+
					"2. Important facts, data, and code snippets mentioned\n"+
					"3. Current task context and what was being worked on\n"+
					"4. Any pending questions or next steps\n\n"+
					"Conversation:\n%s", conversationText.String())},
		},
	}

	resp, err := p.Chat(context.Background(), req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

// estimateTokens provides a rough token count estimate for messages.
func estimateTokens(messages []provider.Message) int {
	total := 0
	for _, msg := range messages {
		total += len(msg.Content) / 4
	}
	return total
}

// stripThinkTags removes <think>...</think> blocks from LLM content.
// Some models emit chain-of-thought in <think> tags which should not be shown to users.
func stripThinkTags(s string) string {
	for {
		start := strings.Index(s, "<think>")
		if start == -1 {
			break
		}
		end := strings.Index(s[start:], "</think>")
		if end == -1 {
			// Unclosed tag — strip from <think> to end
			s = s[:start]
			break
		}
		s = s[:start] + s[start+end+len("</think>"):]
	}
	return strings.TrimSpace(s)
}

// thinkStreamFilter wraps a streaming callback to suppress <think> blocks in real-time,
// redirecting thinking content to onThinking instead.
type thinkStreamFilter struct {
	forward    func(string)
	onThinking func(string) // called with content inside <think> blocks
	buf        strings.Builder
	inside     bool
}

func (f *thinkStreamFilter) Write(delta string) {
	f.buf.WriteString(delta)
	text := f.buf.String()

	for {
		if f.inside {
			end := strings.Index(text, "</think>")
			if end == -1 {
				// Still inside think block
				// Check for partial closing tag at end (e.g., "</thi")
				holdback := partialTagSuffix(text, "</think>")
				toForward := text[:len(text)-len(holdback)]
				if toForward != "" && f.onThinking != nil {
					f.onThinking(toForward)
				}
				f.buf.Reset()
				f.buf.WriteString(holdback)
				return
			}
			// Forward thinking content before </think>
			if end > 0 && f.onThinking != nil {
				f.onThinking(text[:end])
			}
			text = text[end+len("</think>"):]
			f.inside = false
		}

		start := strings.Index(text, "<think>")
		if start == -1 {
			// No think tag — but check for partial opening tag at end (e.g., "<thi")
			holdback := partialTagSuffix(text, "<think>")
			toForward := text[:len(text)-len(holdback)]
			if toForward != "" {
				f.forward(toForward)
			}
			f.buf.Reset()
			f.buf.WriteString(holdback)
			return
		}

		// Forward content before <think>
		if start > 0 {
			f.forward(text[:start])
		}
		text = text[start+len("<think>"):]
		f.inside = true
	}
}

// partialTagSuffix checks if text ends with a partial prefix of tag.
// e.g., text="hello<thi", tag="<think>" → returns "<thi"
func partialTagSuffix(text, tag string) string {
	for i := 1; i < len(tag); i++ {
		suffix := tag[:i]
		if strings.HasSuffix(text, suffix) {
			return suffix
		}
	}
	return ""
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

func convertTools(mcpTools []mcp.Tool) []provider.Tool {
	var result []provider.Tool
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

		// Sanitize tool name for compatibility (a-z, 0-9, _, -)
		name := sanitizeToolName(t.Name)
		if name == "" {
			log.Printf("[Warn] Skipping tool with empty name (original: %q)", t.Name)
			continue
		}
		// Max function name length is 64
		if len(name) > 64 {
			name = name[:64]
		}

		// Ensure description is not empty
		desc := t.Description
		if desc == "" {
			desc = "Tool: " + name
		}
		// Truncate overly long descriptions
		if len(desc) > 1024 {
			desc = desc[:1021] + "..."
		}

		result = append(result, provider.Tool{
			Name:        name,
			Description: desc,
			Parameters:  schemaMap,
		})
	}
	return result
}

// validateTools checks and fixes tool definitions before use.
func validateTools(tools []provider.Tool) []provider.Tool {
	var valid []provider.Tool
	for _, t := range tools {
		// Ensure name is valid
		if t.Name == "" {
			continue
		}
		if len(t.Name) > 64 {
			t.Name = t.Name[:64]
		}

		// Ensure parameters is a valid object schema
		if t.Parameters == nil {
			t.Parameters = map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			}
		}
		if params, ok := t.Parameters.(map[string]interface{}); ok {
			if _, hasType := params["type"]; !hasType {
				params["type"] = "object"
			}
			if _, hasProps := params["properties"]; !hasProps {
				params["properties"] = map[string]interface{}{}
			}
		}

		// Ensure description exists
		if t.Description == "" {
			t.Description = "Tool: " + t.Name
		}
		if len(t.Description) > 1024 {
			t.Description = t.Description[:1021] + "..."
		}

		valid = append(valid, t)
	}
	return valid
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// truncateShellOutput formats a failed command's output, capping at maxChars.
func truncateShellOutput(output string, err error, maxChars int) string {
	combined := output
	if len(combined) > maxChars {
		combined = combined[:maxChars] + "... [output truncated]"
	}
	return fmt.Sprintf("Command failed (%v): %s", err, combined)
}

// truncateShellSuccess caps successful command output at maxChars.
func truncateShellSuccess(output string, maxChars int) string {
	if len(output) > maxChars {
		return output[:maxChars] + "\n\n[output truncated at " + fmt.Sprintf("%d", maxChars) + " chars]"
	}
	return output
}

// sanitizeToolName converts a skill name to a valid tool function name
func mapKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

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
func executeSkillSubCall(p provider.Provider, model string, skill SkillTool, input string) (string, error) {
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
- You may also use inline code (python3 <<'PYEOF'...PYEOF for multi-line, python3 -c "..." for trivial one-liners only)
- The skills/ directory is READ-ONLY — do NOT write files into it`, skill.Name, skill.Name)
	} else {
		constraintBlock = `
CRITICAL constraints on commands:
- NO script files (no "python3 xxx.py", no "node script.js") — you have NO local files
- Only use: curl, python3 <<'PYEOF', python3 -c "...", node -e "...", jq, sed, grep -E, xmllint, etc.
- For multi-line Python: ALWAYS use heredoc (python3 <<'PYEOF'...PYEOF), NEVER cram complex code into python3 -c "..."
- Install packages with: python3 -m pip install <pkg> (NEVER bare "pip")
- For web scraping: prefer RSS feeds (curl + xmllint/sed/grep) or python3 with urllib`
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

	req := &provider.ChatRequest{
		Model:  model,
		System: systemPrompt,
		Messages: []provider.Message{
			{Role: "user", Content: input},
		},
	}

	resp, err := p.Chat(context.Background(), req)
	if err != nil {
		return "", fmt.Errorf("skill sub-call LLM failed for '%s': %v. The skill's sub-LLM call could not complete. Try accomplishing the goal with tofi_shell directly", skill.Name, err)
	}
	content := resp.Content
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

	var parsed struct {
		Message  string   `json:"message"`
		Commands []string `json:"commands"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err == nil && (parsed.Message != "" || len(parsed.Commands) > 0) {
		// Successfully parsed structured response
		var result strings.Builder
		if parsed.Message != "" {
			result.WriteString(parsed.Message)
		}
		if len(parsed.Commands) > 0 {
			if result.Len() > 0 {
				result.WriteString("\n\n")
			}
			result.WriteString("[COMMANDS TO EXECUTE]\n")
			for _, cmd := range parsed.Commands {
				result.WriteString("$ " + cmd + "\n")
			}
			result.WriteString("\n[Execute these commands using tofi_shell to get actual results.]")
		}
		return result.String(), nil
	}

	// Fallback: LLM didn't return valid JSON, return raw content with hint
	if strings.Contains(content, "```") {
		return content + "\n\n[This skill returned suggested commands. Execute them using tofi_shell to get actual results.]", nil
	}
	return content, nil
}
