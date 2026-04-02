package agent

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
	SkillDir     string                 // Absolute path to skill directory on disk (empty if no scripts)
	BundledTools []ExtraBuiltinTool     // Tools that come with this skill — activated when skill is loaded
	DirectTools  []models.SkillToolDef  // Direct tool definitions from manifest (skip sub-LLM, execute scripts directly)
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
	Hooks            *Hooks                                                    // Optional: pre/post hooks for tool calls, API calls, compaction
}

type MCPServerConfig struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
}

// AgentResult holds the result of an agent loop execution.
type AgentResult struct {
	Content        string
	TotalUsage     provider.Usage
	TotalCost      float64
	Model          string
	LLMCalls       int
	LoadedSkills   []string              // Skills that were loaded during this agent loop (for persistence)
	Messages       []provider.Message    // All new messages from this turn (assistant + tool calls + tool responses)
	ModelBreakdown map[string]ModelUsage // Per-model token/cost breakdown
	Trace          *Trace                // Execution trace for observability (nil if not recorded)
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

			// If skill has scripts, create sandbox symlink + register tools
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

				if len(skill.DirectTools) > 0 {
					// Direct tool registration — each tool maps to a script, no sub-LLM needed
					for _, toolDef := range skill.DirectTools {
						// Skip if already registered
						alreadyRegistered := false
						for _, t := range allTools {
							if t.Name == toolDef.Name {
								alreadyRegistered = true
								break
							}
						}
						if alreadyRegistered {
							continue
						}

						// Build JSON Schema from params
						properties := map[string]interface{}{}
						var required []string
						for paramName, param := range toolDef.Params {
							prop := map[string]interface{}{
								"type":        param.Type,
								"description": param.Description,
							}
							if param.Default != nil {
								prop["default"] = param.Default
							}
							properties[paramName] = prop
							if param.Required {
								required = append(required, paramName)
							}
						}

						schema := provider.Tool{
							Name:        toolDef.Name,
							Description: toolDef.Description,
							Parameters: map[string]interface{}{
								"type":       "object",
								"properties": properties,
								"required":   required,
							},
						}

						// Capture for closure
						capturedScript := filepath.Join(skill.SkillDir, toolDef.Script)
						capturedName := toolDef.Name
						capturedParams := toolDef.Params

						allTools = append(allTools, schema)
						extraHandlers[capturedName] = func(args map[string]interface{}) (string, error) {
							cmdParts := []string{"python3", capturedScript}

							// First positional arg: "query" or "url"
							if q, ok := args["query"].(string); ok {
								cmdParts = append(cmdParts, shellQuote(q))
							} else if u, ok := args["url"].(string); ok {
								cmdParts = append(cmdParts, shellQuote(u))
							}

							// Named params as flags
							for paramName, paramDef := range capturedParams {
								if paramName == "query" || paramName == "url" {
									continue
								}
								val, exists := args[paramName]
								if !exists {
									continue
								}
								flagName := strings.ReplaceAll(paramName, "_", "-")
								switch paramDef.Type {
								case "boolean":
									if b, ok := val.(bool); ok && b {
										cmdParts = append(cmdParts, "--"+flagName)
									}
								case "integer":
									if n, ok := val.(float64); ok {
										cmdParts = append(cmdParts, fmt.Sprintf("--%s", flagName), fmt.Sprintf("%d", int(n)))
									}
								default: // string
									if s, ok := val.(string); ok && s != "" {
										cmdParts = append(cmdParts, "--"+flagName, shellQuote(s))
									}
								}
							}

							cmd := strings.Join(cmdParts, " ")
							timeoutSec := classifyTimeout(cmd, 0)
							execInstance := cfg.Executor
							if execInstance == nil {
								execInstance = executor.NewDirectExecutor("")
							}
							output, execErr := execInstance.Execute(
								context.Background(),
								cfg.SandboxDir,
								cfg.UserDir,
								cmd,
								timeoutSec,
								cfg.SecretEnv,
							)
							result := ShellResult{Stdout: output}
							if execErr != nil {
								result.Stderr = execErr.Error()
								result.ExitCode = 1
							}
							result.Interpretation = interpretExitCode(cmd, result.ExitCode)
							return smartTruncate(result.FormatForAgent(), 4000), nil
						}
						activatedTools = append(activatedTools, capturedName)
						ctx.Log("[Skill:%s] Registered direct tool: %s → %s", name, capturedName, capturedScript)
					}
				} else {
					// Fallback: register run_skill__ for skills without direct tools
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
			}

			// Replace relative script paths with absolute paths so AI doesn't need to guess
		instructions := skill.Instructions
		if skill.SkillDir != "" {
			// Only replace the relative prefix "skills/{name}/" — single pass to avoid double-replace
			relativePrefix := "skills/" + name + "/"
			absolutePrefix := skill.SkillDir + "/"
			instructions = strings.ReplaceAll(instructions, relativePrefix, absolutePrefix)
		}

		result := fmt.Sprintf("# Skill: %s\n\n%s", name, instructions)
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

## Tool Usage Rules
- NEVER use tofi_shell to fetch web pages (curl, wget, python requests, httpx, etc). Always use the web-fetch skill for fetching URLs — it extracts clean text and saves tokens.
- NEVER use tofi_shell to run python scripts with requests/httpx/urllib for web scraping. Use web-fetch instead.
- tofi_shell output is smart-truncated (head + tail preserved). Install/build commands get extended timeout (5min). Long commands auto-background after 15s.
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
	tracker := NewTokenTracker(cfg.Model)
	trace := NewTrace()

	// Transcript for crash recovery (best-effort — don't fail if it can't write)
	// Uses UserDir for per-user isolation when available
	var transcript *Transcript
	if cfg.SessionID != "" {
		if t, err := NewTranscript(cfg.SessionID, cfg.UserDir); err == nil {
			transcript = t
			defer func() {
				// Clean up transcript on successful completion
				if transcript != nil {
					transcript.Clean()
				}
			}()
		}
	}

	// Background task manager for auto-backgrounding long shell commands
	bgManager := NewBackgroundTaskManager()

	for step := 1; step <= maxSteps; step++ {
		// Check for cancellation before starting a new LLM call
		if loopCtx.Err() != nil {
			ctx.Log("[Agent] Cancelled by client.")
			return &AgentResult{
				Content:        "",
				TotalUsage:     totalUsage,
				TotalCost:      tracker.TotalCost(),
				Model:          cfg.Model,
				LLMCalls:       llmCalls,
				LoadedSkills:   mapKeys(loadedSkills),
				Messages:       messages[initialMsgCount:],
				ModelBreakdown: tracker.ModelBreakdown(),
				Trace:          trace,
			}, nil
		}

		// Micro-compact: trim old tool results that LLM has already consumed
		if len(messages) > 8 {
			messages = microCompact(messages, 6)
		}

		// Pre-call context budget check — compact proactively before hitting the limit
		estimatedInput := EstimateContextUsage(systemPrompt, messages, allTools)
		if tracker.ShouldCompact(estimatedInput, 0.80) && len(messages) > 4 {
			ctx.Log("[Agent] Pre-call compaction triggered: estimated %d tokens > 80%% of %d window", estimatedInput, tracker.ContextWindow())
			summary, compactErr := compactMessages(cfg.Provider, cfg.Model, messages)
			if compactErr != nil {
				ctx.Log("[Agent] Pre-call compaction failed: %v", compactErr)
			} else {
				messages = compactAndRebuild(messages, summary)
				ctx.Log("[Agent] Pre-call compacted to %d messages (~%d tokens)", len(messages), EstimateContextUsage(systemPrompt, messages, allTools))
			}
		}

		// Pre-API hook
		if err := cfg.Hooks.callPreAPICall(step, len(messages), estimatedInput); err != nil {
			ctx.Log("[Agent] PreAPICall hook blocked: %v", err)
			return nil, fmt.Errorf("pre-API hook: %w", err)
		}

		// Checkpoint before API call (crash recovery)
		if transcript != nil {
			transcript.Checkpoint(step, PhaseThinking, messages, totalUsage, llmCalls)
		}

		req := &provider.ChatRequest{
			Model:    cfg.Model,
			System:   systemPrompt,
			Messages: messages,
			Tools:    allTools,
		}

		apiStart := time.Now()
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
					Content:        lastContent,
					TotalUsage:     totalUsage,
					TotalCost:      tracker.TotalCost(),
					Model:          cfg.Model,
					LLMCalls:       llmCalls + 1,
					LoadedSkills:   mapKeys(loadedSkills),
					Messages:       messages[initialMsgCount:],
					ModelBreakdown: tracker.ModelBreakdown(),
					Trace:          trace,
				}, nil
			}
			trace.RecordError(step, err)
			return nil, fmt.Errorf("LLM call failed: %v", err)
		}

		apiDuration := time.Since(apiStart)
		llmCalls++
		totalUsage.Add(resp.Usage)
		tracker.RecordUsage(cfg.Model, resp.Usage)
		trace.RecordAPICall(step, cfg.Model, resp.Usage, resp, apiDuration)
		cfg.Hooks.callPostAPICall(step, resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.HasToolCalls())
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
					Content:        cleanContent,
					TotalUsage:     totalUsage,
					TotalCost:      tracker.TotalCost(),
					Model:          cfg.Model,
					LLMCalls:       llmCalls,
					LoadedSkills:   mapKeys(loadedSkills),
					Messages:       messages[initialMsgCount:],
					ModelBreakdown: tracker.ModelBreakdown(),
					Trace:          trace,
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

		// Execute Tools — try parallel path for concurrency-safe batches
		if canExecuteInParallel(resp.ToolCalls) {
			ctx.Log("[Agent] Executing %d tools in parallel", len(resp.ToolCalls))
			results := executeToolsParallel(resp.ToolCalls, func(tc provider.ToolCall) (string, error) {
				var argsMap map[string]interface{}
				if err := json.Unmarshal([]byte(tc.Arguments), &argsMap); err != nil {
					return fmt.Sprintf("Error parsing arguments for %s: %v", tc.Name, err), nil
				}

				// Extra handlers (custom tools)
				if handler, ok := extraHandlers[tc.Name]; ok {
					result, err := handler(argsMap)
					if err != nil {
						return fmt.Sprintf("Tool error: %v", err), nil
					}
					return result, nil
				}

				// MCP tools
				cli, exists := clientMap[tc.Name]
				if !exists {
					return fmt.Sprintf("Tool '%s' not found.", tc.Name), nil
				}

				toolResult, err := cli.CallTool(context.Background(), mcp.CallToolRequest{
					Params: mcp.CallToolParams{
						Name:      tc.Name,
						Arguments: argsMap,
					},
				})
				if err != nil {
					return fmt.Sprintf("Tool execution error: %v", err), nil
				}

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
				return sb.String(), nil
			}, 5)

			// Append results in order + fire callbacks
			for _, r := range results {
				messages = append(messages, provider.Message{
					Role:       "tool",
					Content:    r.Content,
					ToolCallID: r.CallID,
					ToolName:   r.ToolName,
				})
				ctx.Log("[Parallel:%s] %s", r.ToolName, truncate(r.Content, 200))
				if cfg.OnToolCall != nil {
					cfg.OnToolCall(r.ToolName, "", r.Content, 0)
				}
			}
		} else {
			// Sequential execution (original path) for non-concurrent-safe tools
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

			// PreToolCall hook — can modify args or block execution
			if modifiedArgs, hookErr := cfg.Hooks.callPreToolCall(fnName, argsMap); hookErr != nil {
				errMsg := fmt.Sprintf("PreToolCall hook blocked %s: %v", fnName, hookErr)
				messages = append(messages, provider.Message{
					Role: "tool", Content: errMsg, ToolCallID: callID, ToolName: fnName,
				})
				ctx.Log("[Hook] %s", errMsg)
				continue
			} else {
				argsMap = modifiedArgs
			}

			// markStepDone is a helper to update the step status after tool execution
			markStepDone := func(result string) {
				durationMs := time.Since(toolStartTime).Milliseconds()
				// PostToolCall hook — can modify output
				if modified, hookErr := cfg.Hooks.callPostToolCall(fnName, argsMap, result); hookErr != nil {
					ctx.Log("[Hook] PostToolCall error for %s: %v", fnName, hookErr)
				} else {
					result = modified
				}
				// Record in trace
				trace.RecordToolExec(step, fnName, fnArgs, result, true, time.Since(toolStartTime))
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

				// Detect skill script in command → override display name for OnToolCall
				if displayName := detectSkillFromCommand(command); displayName != "" {
					origMarkStepDone := markStepDone
					markStepDone = func(result string) {
						durationMs := time.Since(toolStartTime).Milliseconds()
						if cfg.OnStepDone != nil {
							cfg.OnStepDone(displayName, result, durationMs)
						}
						if cfg.OnToolCall != nil {
							cfg.OnToolCall(displayName, fnArgs, result, durationMs)
						}
					}
					_ = origMarkStepDone // suppress unused warning
				}

				// Destructive command detection (AST-based)
				destructLevel, destructWarning := DetectDestructiveAST(command)
				if destructLevel >= DestructiveCommand {
					ctx.Log("[Shell] ⚠️  Destructive command detected: %s", destructWarning)
					// TODO: in Chat mode, ask user for confirmation via Hooks
					// For now, log the warning and proceed (App Run is unattended)
				}

				// Security validation (blocklist + regex patterns)
				if err := executor.ValidateCommand(command, cfg.SandboxDir); err != nil {
					resultMsg := "Security violation: " + err.Error()
					messages = append(messages, provider.Message{
						Role: "tool", Content: resultMsg, ToolCallID: callID, ToolName: fnName,
					})
					markStepDone(resultMsg)
					continue
				}

				// Classify timeout based on command type
				requestedTimeout := 0
				if t, ok := argsMap["timeout"].(float64); ok && t > 0 {
					requestedTimeout = int(t)
				}
				timeout := classifyTimeout(command, requestedTimeout)

				// Execute with auto-backgrounding for long commands
				var shellResult ShellResult
				execInstance := cfg.Executor
				if execInstance == nil {
					execInstance = executor.NewDirectExecutor("")
				}

				if bgManager != nil {
					shellResult = bgManager.RunWithAutoBackground(
						loopCtx, execInstance,
						cfg.SandboxDir, cfg.UserDir, command, timeout, cfg.SecretEnv,
						func(status string) {
							if cfg.OnProgress != nil {
								cfg.OnProgress("running", 0, status)
							}
						},
					)
				} else {
					// Direct execution (no background manager)
					output, execErr := execInstance.Execute(loopCtx, cfg.SandboxDir, cfg.UserDir, command, timeout, cfg.SecretEnv)
					shellResult = ShellResult{
						Stdout:     output,
						DurationMs: time.Since(toolStartTime).Milliseconds(),
					}
					if execErr != nil {
						shellResult.Stderr = execErr.Error()
						shellResult.ExitCode = 1
						if strings.Contains(execErr.Error(), "timed out") {
							shellResult.TimedOut = true
						}
					}
					shellResult.Interpretation = interpretExitCode(command, shellResult.ExitCode)
				}

				// Format result with smart truncation
				resultMsg := shellResult.FormatForAgent()
				resultMsg = smartTruncate(resultMsg, 4000)

				ctx.Log("[Shell] %s → %s (exit=%d, %dms)", truncate(command, 80), truncate(resultMsg, 200), shellResult.ExitCode, shellResult.DurationMs)
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
		} // end sequential execution else block

		// Post-call context compaction — use actual API-reported token count
		if resp.Usage.InputTokens > int64(float64(tracker.ContextWindow())*0.80) && len(messages) > 4 {
			ctx.Log("[Agent] Post-call compaction triggered: %d tokens > 80%% of %d window", resp.Usage.InputTokens, tracker.ContextWindow())

			summary, compactErr := compactMessages(cfg.Provider, cfg.Model, messages)
			if compactErr != nil {
				ctx.Log("[Agent] Compaction failed: %v", compactErr)
			} else {
				originalCount := len(messages)
				originalTokens := int(resp.Usage.InputTokens)
				messages = compactAndRebuild(messages, summary)

				compactedTokens := EstimateContextUsage(systemPrompt, messages, allTools)
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
			Content:        lastContent,
			TotalUsage:     totalUsage,
			TotalCost:      tracker.TotalCost(),
			Model:          cfg.Model,
			LLMCalls:       llmCalls,
			LoadedSkills:   mapKeys(loadedSkills),
			Messages:       messages[initialMsgCount:],
			ModelBreakdown: tracker.ModelBreakdown(),
			Trace:          trace,
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
		// For compaction input, truncate long tool results to save context
		content := msg.Content
		if msg.Role == "tool" && len(content) > 500 {
			content = content[:500] + "\n[... truncated for summarization]"
		}
		conversationText.WriteString(fmt.Sprintf("[%s]: %s\n\n", msg.Role, content))
	}

	req := &provider.ChatRequest{
		Model:  model,
		System: "You are a precise assistant that creates structured conversation summaries. Output in the same language as the conversation.",
		Messages: []provider.Message{
			{Role: "user", Content: fmt.Sprintf(
				"Summarize the following conversation. You MUST preserve:\n"+
					"1. The current task goal and what the user originally asked for\n"+
					"2. Key decisions made and their reasoning\n"+
					"3. Important results, data, file paths, and code outputs\n"+
					"4. What was accomplished so far (completed steps)\n"+
					"5. What still needs to be done (pending steps)\n"+
					"6. Any errors encountered and how they were resolved\n\n"+
					"Format as structured sections. Be concise but complete.\n\n"+
					"Conversation:\n%s", conversationText.String())},
		},
	}

	resp, err := p.Chat(context.Background(), req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

// microCompact performs lightweight in-place trimming of old tool results
// that the LLM has already seen and acted upon. This reduces context usage
// without needing a full LLM-powered summarization pass.
//
// Strategy: tool results older than the last N messages get truncated to
// a short summary (first 200 chars + "[full output was N chars]").
// The LLM has already consumed and responded to these results,
// so the full text is no longer needed.
func microCompact(messages []provider.Message, keepRecentCount int) []provider.Message {
	if keepRecentCount <= 0 {
		keepRecentCount = 6 // keep last 3 pairs (assistant + tool) intact
	}

	if len(messages) <= keepRecentCount {
		return messages
	}

	result := make([]provider.Message, len(messages))
	copy(result, messages)

	cutoff := len(messages) - keepRecentCount

	for i := 0; i < cutoff; i++ {
		msg := &result[i]
		if msg.Role != "tool" {
			continue
		}
		if len(msg.Content) <= 300 {
			continue
		}

		// Preserve first 200 chars as a preview
		preview := msg.Content[:200]
		// Find a clean break point (newline)
		if idx := strings.LastIndex(preview, "\n"); idx > 100 {
			preview = preview[:idx]
		}
		msg.Content = fmt.Sprintf("%s\n\n[... %d chars of output omitted — already processed by assistant above]",
			preview, len(msg.Content))
	}

	return result
}

// compactAndRebuild takes the full message list and a summary, finds a safe cut point
// that preserves complete tool call/result sequences, and rebuilds the message list
// with the summary prepended.
func compactAndRebuild(messages []provider.Message, summary string) []provider.Message {
	keepFrom := len(messages) - 2
	if keepFrom < 1 {
		keepFrom = 1
	}
	for i := keepFrom; i >= 1; i-- {
		if messages[i].Role == "tool" {
			for j := i - 1; j >= 1; j-- {
				if messages[j].Role == "assistant" && len(messages[j].ToolCalls) > 0 {
					keepFrom = j
					break
				}
			}
			break
		} else if messages[i].Role == "assistant" && len(messages[i].ToolCalls) > 0 {
			keepFrom = i
			break
		}
	}

	kept := make([]provider.Message, len(messages[keepFrom:]))
	copy(kept, messages[keepFrom:])
	result := []provider.Message{
		{Role: "user", Content: fmt.Sprintf("<context_summary>\n%s\n</context_summary>\n\nThe above is a summary of our conversation so far. Please continue from where we left off.", summary)},
	}
	return append(result, kept...)
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

// smartTruncate intelligently truncates command output by preserving
// the head and tail (most useful parts) and summarizing the middle.
func smartTruncate(output string, maxChars int) string {
	if len(output) <= maxChars {
		return output
	}

	lines := strings.Split(output, "\n")

	// For very few lines that are just long, do simple truncation
	if len(lines) <= 10 {
		return output[:maxChars] + "\n\n[truncated, total " + fmt.Sprintf("%d", len(output)) + " chars]"
	}

	// Keep first 30% of budget for head, last 30% for tail, 40% buffer
	headBudget := maxChars * 3 / 10
	tailBudget := maxChars * 3 / 10

	// Build head: take lines from the start until budget exhausted
	var headLines []string
	headUsed := 0
	for _, line := range lines {
		if headUsed+len(line)+1 > headBudget {
			break
		}
		headLines = append(headLines, line)
		headUsed += len(line) + 1
	}

	// Build tail: take lines from the end until budget exhausted
	var tailLines []string
	tailUsed := 0
	for i := len(lines) - 1; i >= len(headLines); i-- {
		if tailUsed+len(lines[i])+1 > tailBudget {
			break
		}
		tailLines = append([]string{lines[i]}, tailLines...)
		tailUsed += len(lines[i]) + 1
	}

	omitted := len(lines) - len(headLines) - len(tailLines)
	if omitted <= 0 {
		// Budgets covered everything, just truncate normally
		return output[:maxChars] + "\n\n[truncated, total " + fmt.Sprintf("%d", len(output)) + " chars]"
	}

	var sb strings.Builder
	sb.WriteString(strings.Join(headLines, "\n"))
	sb.WriteString(fmt.Sprintf("\n\n... [%d lines omitted, %d total lines, %d total chars] ...\n\n", omitted, len(lines), len(output)))
	sb.WriteString(strings.Join(tailLines, "\n"))
	return sb.String()
}

// detectSkillFromCommand checks if a shell command runs a skill script
// and returns a display name like "web-search" or "web-fetch".
// Returns empty string if the command is not a skill script.
func detectSkillFromCommand(command string) string {
	// Match patterns like:
	//   python3 /path/to/skills/web-search/scripts/search.py ...
	//   python3 skills/web-search/scripts/news.py ...
	idx := strings.Index(command, "skills/")
	if idx == -1 {
		return ""
	}
	rest := command[idx+len("skills/"):]
	slashIdx := strings.Index(rest, "/")
	if slashIdx == -1 {
		return ""
	}
	return rest[:slashIdx]
}

// sanitizeToolName converts a skill name to a valid tool function name
func mapKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// shellQuote wraps a string in single quotes with proper escaping for shell safety.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
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
