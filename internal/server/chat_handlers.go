package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"tofi-core/internal/bridge"
	"tofi-core/internal/chat"
	"tofi-core/internal/connect"
	"tofi-core/internal/executor"
	"tofi-core/internal/mcp"
	"tofi-core/internal/models"
	"tofi-core/internal/provider"
	"tofi-core/internal/skills"
	"tofi-core/internal/storage"

	"github.com/google/uuid"
)

// --- POST /api/v1/chat/sessions ---

func (s *Server) handleCreateChatSession(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	var req struct {
		Scope  string   `json:"scope"`  // "" or "agent:{name}"
		Model  string   `json:"model"`
		Skills []string `json:"skills"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Allow empty body — all fields optional
		req = struct {
			Scope  string   `json:"scope"`
			Model  string   `json:"model"`
			Skills []string `json:"skills"`
		}{}
	}

	skillsStr := strings.Join(req.Skills, ",")
	sessionID := "s_" + uuid.New().String()[:12]
	session := chat.NewSession(sessionID, req.Model, skillsStr)

	if err := s.chatStore.Save(userID, req.Scope, session); err != nil {
		http.Error(w, "failed to create session: "+err.Error(), 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"id":      session.ID,
		"scope":   req.Scope,
		"model":   session.Model,
		"skills":  req.Skills,
		"created": session.Created,
	})
}

// --- GET /api/v1/chat/sessions ---

func (s *Server) handleListChatSessions(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	scope := r.URL.Query().Get("scope")
	// If scope param is absent entirely, list all scopes.
	// If scope param is present but empty (?scope=), filter by empty scope (user main chat).
	if !r.URL.Query().Has("scope") {
		scope = "*"
	}

	sessions, err := s.chatStore.List(userID, scope, 50)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// Ensure we return [] not null for empty results
	if sessions == nil {
		sessions = []*storage.ChatSessionIndex{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}

// --- GET /api/v1/chat/sessions/{id} ---

func (s *Server) handleGetChatSession(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	sessionID := r.PathValue("id")

	// Ownership check via index
	idx, err := s.chatStore.GetIndex(sessionID)
	if err != nil {
		http.Error(w, "session not found", 404)
		return
	}
	if idx.UserID != userID {
		http.Error(w, "forbidden", 403)
		return
	}

	session, err := s.chatStore.LoadByID(sessionID)
	if err != nil {
		http.Error(w, "session not found: "+err.Error(), 404)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(session)
}

// --- DELETE /api/v1/chat/sessions/{id} ---

func (s *Server) handleDeleteChatSession(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	sessionID := r.PathValue("id")

	idx, err := s.chatStore.GetIndex(sessionID)
	if err != nil {
		http.Error(w, "session not found", 404)
		return
	}
	if idx.UserID != userID {
		http.Error(w, "forbidden", 403)
		return
	}

	if err := s.chatStore.Delete(userID, idx.Scope, sessionID); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- PATCH /api/v1/chat/sessions/{id} ---

func (s *Server) handleUpdateChatSession(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	sessionID := r.PathValue("id")

	idx, err := s.chatStore.GetIndex(sessionID)
	if err != nil {
		http.Error(w, "session not found", 404)
		return
	}
	if idx.UserID != userID {
		http.Error(w, "forbidden", 403)
		return
	}

	session, err := s.chatStore.LoadByID(sessionID)
	if err != nil {
		http.Error(w, "failed to load session: "+err.Error(), 500)
		return
	}

	var req struct {
		Model  *string  `json:"model"`
		Skills []string `json:"skills"`
		Title  *string  `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", 400)
		return
	}

	if req.Model != nil {
		session.Model = *req.Model
	}
	if req.Skills != nil {
		session.Skills = strings.Join(req.Skills, ",")
	}
	if req.Title != nil {
		session.Title = *req.Title
	}
	session.Updated = time.Now().UTC().Format(time.RFC3339)

	if err := s.chatStore.Save(userID, idx.Scope, session); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	var skillsList []string
	if session.Skills != "" {
		skillsList = strings.Split(session.Skills, ",")
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"id":     session.ID,
		"model":  session.Model,
		"skills": skillsList,
		"title":  session.Title,
	})
}

// --- POST /api/v1/chat/sessions/{id}/messages ---

func (s *Server) handleChatMessage(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	sessionID := r.PathValue("id")

	var req struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Message == "" {
		http.Error(w, "message is required", 400)
		return
	}

	// 1. Load session index for ownership check + scope
	idx, err := s.chatStore.GetIndex(sessionID)
	if err != nil {
		http.Error(w, "session not found", 404)
		return
	}
	if idx.UserID != userID {
		http.Error(w, "forbidden", 403)
		return
	}

	// 2. Load full session from XML
	session, err := s.chatStore.LoadByID(sessionID)
	if err != nil {
		http.Error(w, "failed to load session: "+err.Error(), 500)
		return
	}

	// 3. Set up SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	rc := http.NewResponseController(w)
	rc.SetWriteDeadline(time.Time{})
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", 500)
		return
	}

	connData, _ := json.Marshal(map[string]string{"session_id": sessionID})
	fmt.Fprintf(w, "event: connected\ndata: %s\n\n", connData)
	flusher.Flush()

	// 4. Execute via shared method with SSE-based callbacks
	onEvent := func(eventType string, data any) {
		sendSSEEvent(w, flusher, eventType, data)
	}

	result, err := s.executeChatSession(userID, idx.Scope, session, req.Message, onEvent, nil)
	if err != nil {
		sendSSEEvent(w, flusher, "error", map[string]string{"error": err.Error()})
		return
	}

	// 5. Send done event
	contextPct := chat.ContextUsagePercent(result.TotalUsage.InputTokens, result.Model)
	done, _ := json.Marshal(map[string]any{
		"result":                result.Content,
		"model":                 result.Model,
		"total_input_tokens":    result.TotalUsage.InputTokens,
		"total_output_tokens":   result.TotalUsage.OutputTokens,
		"total_cost":            result.TotalCost,
		"llm_calls":             result.LLMCalls,
		"context_usage_percent": contextPct,
	})
	fmt.Fprintf(w, "event: done\ndata: %s\n\n", done)
	flusher.Flush()
}

// executeChatSession runs an agent loop for a chat session.
// Used by both the HTTP handler (SSE) and the app scheduler.
// The onEvent callback receives events: "chunk", "tool_call", "context_compact", "error".
// If onEvent is nil, events are silently discarded.
func (s *Server) executeChatSession(userID, scope string, session *chat.Session, message string, onEvent func(eventType string, data any), opts *bridge.ExecuteOptions) (*mcp.AgentResult, error) {
	sessionID := session.ID

	emit := func(eventType string, data any) {
		if onEvent != nil {
			onEvent(eventType, data)
		}
	}

	// 1. Resolve model and API key
	resolvedModel, apiKey, _, err := s.resolveModelAndKey(userID, session.Model)
	if err != nil {
		return nil, fmt.Errorf("model resolution failed: %w", err)
	}

	// 2. Build system prompt based on scope
	systemPrompt := s.buildChatSystemPrompt(userID, scope)

	// 3. Load skills (only those explicitly set on the session)
	// Skills are loaded in deferred mode: only name+description in system prompt,
	// full Instructions loaded on-demand via tofi_load_skill tool.
	var skillNames []string
	if session.Skills != "" {
		skillNames = strings.Split(session.Skills, ",")
	}
	skillTools, _, secretEnv := s.buildSkillToolsFromNames(userID, skillNames)

	// 4. Build provider messages from session history
	providerMessages := chat.BuildProviderMessages(session, message, resolvedModel)

	// 5. Parse capabilities for agent scope
	var capMCPServers []mcp.MCPServerConfig
	var extraTools []mcp.ExtraBuiltinTool
	if agentName, ok := strings.CutPrefix(scope, chat.ScopeAgentPrefix); ok {
		if agentDef, err := s.workspace.ReadAgent(userID, agentName); err == nil {
			if agentDef.Config.Capabilities != nil {
				capMCPServers, extraTools = s.buildCapabilitiesFromMap(userID, agentDef.Config.Capabilities)
			}
		}
	}

	// 6. Create sandbox
	sandboxDir, err := s.executor.CreateSandbox(executor.SandboxConfig{
		HomeDir: s.config.HomeDir,
		UserID:  userID,
		CardID:  "chat-" + sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create sandbox: %w", err)
	}
	defer s.executor.Cleanup(sandboxDir)

	// 7. Mark session as running
	session.Status = "running"
	if err := s.chatStore.Save(userID, scope, session); err != nil {
		log.Printf("⚠️  [chat:%s] failed to save running state: %v", sessionID[:8], err)
	}

	// 8. Build AgentConfig — split tools into core (always available) and deferred (on-demand)
	var liveUsage provider.Usage // real-time usage updated during agent loop

	// Core tools: always available in every chat session
	coreTools := append(extraTools)
	// Skill search/install tools only in interactive chat — App runs must not pause for user input
	isAppRun := strings.HasPrefix(scope, chat.ScopeAgentPrefix+"app-")
	if !isAppRun {
		coreTools = append(coreTools, s.buildChatWishTools(userID, sessionID, session, scope)...)
	}
	coreTools = append(coreTools, s.buildMemoryTools(userID, "")...)
	coreTools = append(coreTools, s.buildBuiltinTools(userID)...)
	coreTools = append(coreTools, buildSessionInfoTool(session, resolvedModel, &liveUsage))

	// Deferred tools: only activated via tofi_tool_search
	deferredTools := s.buildAppTools(userID)

	// Resolve app ID from agent scope (e.g. "agent:app-daily-ip" → find matching app)
	notifyAppID := ""
	if agentName, ok := strings.CutPrefix(scope, chat.ScopeAgentPrefix); ok {
		// Agent scope format: "app-{id_prefix}" — try to find matching app
		if appIDPrefix, ok := strings.CutPrefix(agentName, "app-"); ok {
			// Try exact match first, then prefix match
			if app, err := s.db.GetApp(appIDPrefix); err == nil {
				notifyAppID = app.ID
			} else {
				// Prefix match: scheduler truncates ID to 8 chars
				apps, _ := s.db.ListApps(userID)
				for _, a := range apps {
					if strings.HasPrefix(a.ID, appIDPrefix) {
						notifyAppID = a.ID
						break
					}
				}
			}
		}
	}

	// Inject tofi_notify into deferred tools (for interactive chat use)
	// App runs don't need this — the runtime auto-sends notifications after completion
	notifyDeps := connect.NotifyDeps{
		ListConnectorsByApp:    s.db.ListConnectorsByApp,
		ListConnectors:         s.db.ListConnectors,
		ListConnectorReceivers: s.db.ListConnectorReceivers,
	}
	deferredTools = connect.InjectNotifyTool(deferredTools, userID, notifyAppID, notifyDeps)

	agentCfg := mcp.AgentConfig{
		System:        systemPrompt,
		Messages:      providerMessages,
		MCPServers:    capMCPServers,
		SkillTools:    skillTools,
		ExtraTools:    coreTools,
		DeferredTools: deferredTools,
		LiveUsage:  &liveUsage,
		SandboxDir: sandboxDir,
		UserDir:    userID,
		Executor:   s.executor,
		SecretEnv:  secretEnv,
	}

	// Configure agent callbacks
	if opts != nil && opts.OnStreamChunk != nil {
		agentCfg.OnStreamChunk = opts.OnStreamChunk
	} else {
		agentCfg.OnStreamChunk = func(_ string, delta string) {
			emit("chunk", map[string]string{"delta": delta})
		}
	}

	// Thinking/reasoning chunks (always emit via SSE)
	agentCfg.OnThinkingChunk = func(_ string, delta string) {
		emit("thinking", map[string]string{"delta": delta})
	}

	if opts != nil && opts.OnToolCall != nil {
		agentCfg.OnToolCall = opts.OnToolCall
	} else {
		agentCfg.OnToolCall = func(toolName, input, output string, durationMs int64) {
			emit("tool_call", map[string]any{
				"tool":        toolName,
				"input":       input,
				"output":      output,
				"duration_ms": durationMs,
			})
		}
	}

	if opts != nil && opts.OnContextCompact != nil {
		agentCfg.OnContextCompact = opts.OnContextCompact
	} else {
		agentCfg.OnContextCompact = func(summary string, originalTokens, compactedTokens int) {
			session.Summary = summary
			emit("context_compact", map[string]any{
				"summary":          summary,
				"original_tokens":  originalTokens,
				"compacted_tokens": compactedTokens,
			})
		}
	}

	if opts != nil && opts.OnStepStart != nil {
		agentCfg.OnStepStart = opts.OnStepStart
	}
	if opts != nil && opts.OnStepDone != nil {
		agentCfg.OnStepDone = opts.OnStepDone
	}

	p, err := provider.NewForModel(resolvedModel, apiKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create provider: %w", err)
	}
	agentCfg.Provider = p
	agentCfg.Model = resolvedModel

	// 9. Run agent loop
	ctx := models.NewExecutionContext("chat", userID, s.config.HomeDir)
	ctx.DB = s.db
	defer ctx.Close()

	log.Printf("💬 [chat:%s] user=%s model=%s skills=%v scope=%s",
		sessionID[:8], userID, resolvedModel, skillNames, scope)

	agentResult, err := mcp.RunAgentLoop(agentCfg, ctx)
	if err != nil {
		session.Status = ""
		s.chatStore.Save(userID, scope, session)
		return nil, fmt.Errorf("agent error: %w", err)
	}

	// 10. Persist messages to session XML
	session.AddMessage(chat.Message{
		Role:    "user",
		Content: message,
	})

	session.AddMessage(chat.Message{
		Role:    "assistant",
		Content: agentResult.Content,
		Tokens:  int(agentResult.TotalUsage.OutputTokens),
	})

	// Update usage
	session.Usage.InputTokens += agentResult.TotalUsage.InputTokens
	session.Usage.OutputTokens += agentResult.TotalUsage.OutputTokens
	session.Usage.Cost += agentResult.TotalCost

	// Auto-generate title from first user message
	firstTitle := session.Title == ""
	if firstTitle && len(session.Messages) > 0 {
		// Immediate: truncate first message as temporary title
		runes := []rune(message)
		if len(runes) > 50 {
			runes = runes[:50]
		}
		session.Title = string(runes)

		// Async: use AI to generate a better title (same model, no extra provider needed)
		go s.generateSessionTitle(userID, scope, sessionID, resolvedModel, apiKey, message)
	}

	// Update model if it was auto-resolved
	if session.Model == "" {
		session.Model = resolvedModel
	}

	// Mark session as idle
	session.Status = ""
	session.HoldInfo = nil

	// Save session
	if err := s.chatStore.Save(userID, scope, session); err != nil {
		log.Printf("⚠️  [chat:%s] failed to save session: %v", sessionID[:8], err)
	}

	return agentResult, nil
}

// buildChatSystemPrompt creates the system prompt based on scope.
func (s *Server) buildChatSystemPrompt(userID, scope string) string {
	// Agent scope: load from workspace
	if agentName, ok := strings.CutPrefix(scope, chat.ScopeAgentPrefix); ok {
		if agentDef, err := s.workspace.ReadAgent(userID, agentName); err == nil {
			prompt := agentDef.SystemPrompt()
			if prompt == "" {
				prompt = "You are " + agentName + ", an AI agent."
			}
			// Add operational instructions from AGENTS.md
			if agentDef.AgentsMD != "" {
				prompt += "\n\n## Operational Instructions\n" + agentDef.AgentsMD
			}
			// App runs: AI output IS the deliverable — runtime handles notification delivery
			if strings.HasPrefix(agentName, "app-") {
				prompt += `

## Output Rules
Your text output is your ONLY deliverable. The platform runtime captures your final output and automatically delivers it to the configured notification channels (Telegram, Slack, Email, etc.).
- Do NOT mention or reference any notification channel (Telegram, Slack, etc.) in your output.
- Do NOT say "sending to..." or "delivered to...". Just produce the content.
- Write your output as if it will be read directly by the user — clean, concise, ready to consume.`
			}
			prompt += "\n\nCurrent time: " + time.Now().Format("2006-01-02 15:04:05 MST (Monday)")
			return prompt
		}
	}

	// User main chat: minimal system prompt — all detailed instructions live in skills
	prompt := fmt.Sprintf(`You are Tofi, a capable AI assistant.
Respond in the same language as the user. Be concise and helpful.
Think before acting. If a tool fails, try a different approach. Always deliver real results, never give up.

Current time: %s`, time.Now().Format("2006-01-02 15:04:05 MST (Monday)"))

	return prompt
}

// appendSystemSkills adds system skill names to the list, avoiding duplicates.
func appendSystemSkills(skillNames []string, db *storage.DB) []string {
	sysNames, err := db.ListSystemSkillNames()
	if err != nil {
		return skillNames
	}

	existing := make(map[string]bool, len(skillNames))
	for _, n := range skillNames {
		existing[strings.TrimSpace(n)] = true
	}

	for _, name := range sysNames {
		if !existing[name] {
			skillNames = append(skillNames, name)
		}
	}
	return skillNames
}

// --- POST /api/v1/chat/sessions/{id}/continue ---

func (s *Server) handleChatSessionContinue(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	if !s.signalHold(sessionID, "continue") {
		http.Error(w, "no hold channel found for session", http.StatusConflict)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "continued"})
}

// --- POST /api/v1/chat/sessions/{id}/abort ---

func (s *Server) handleChatSessionAbort(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	if !s.signalHold(sessionID, "abort") {
		http.Error(w, "no hold channel found for session", http.StatusConflict)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "aborted"})
}

// sendSSEEvent is a helper to send a named SSE event with JSON data.
func sendSSEEvent(w http.ResponseWriter, flusher http.Flusher, event string, data any) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	flusher.Flush()
}

// sendSSEError sends an error event over SSE.
func sendSSEError(w http.ResponseWriter, flusher http.Flusher, errMsg string) {
	sendSSEEvent(w, flusher, "error", map[string]string{"error": errMsg})
}

// buildChatWishTools builds skill search + suggest_install tools for chat sessions.
// Uses sessionID as the hold channel key and updates session status/holdInfo.
func (s *Server) buildChatWishTools(userID, sessionID string, session *chat.Session, scope string) []mcp.ExtraBuiltinTool {
	return []mcp.ExtraBuiltinTool{
		{
			Schema: provider.Tool{
				Name:        "tofi_search",
				Description: "Search for skills on the skills.sh marketplace. Use this when you need a capability that isn't already installed.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": "Search query (e.g., 'react testing', 'code review', 'summarize')",
						},
					},
					"required": []string{"query"},
				},
			},
			Handler: func(args map[string]interface{}) (string, error) {
				query, _ := args["query"].(string)
				if query == "" {
					return "Error: query is required", nil
				}
				client := skills.NewRegistryClient("")
				result, err := client.Search(query, 5)
				if err != nil {
					return fmt.Sprintf("Search failed: %v", err), nil
				}
				if len(result.Skills) == 0 {
					return "No skills found for query: " + query, nil
				}
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("Found %d skills:\n\n", len(result.Skills)))
				for _, sk := range result.Skills {
					sb.WriteString(fmt.Sprintf("- **%s** (%s): %s\n", sk.Name, sk.Source, sk.Description))
					sb.WriteString(fmt.Sprintf("  Install: use tofi_suggest_install with skill_id=\"%s\"\n", sk.ID))
				}
				return sb.String(), nil
			},
		},
		{
			Schema: provider.Tool{
				Name: "tofi_suggest_install",
				Description: "Suggest installing a skill. Execution will PAUSE until the user installs and clicks Continue, or skips. " +
					"Use this after tofi_search finds a useful skill that isn't installed yet.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"skill_id": map[string]interface{}{
							"type":        "string",
							"description": "Full skill ID from search results (e.g., 'owner/repo@skill-name')",
						},
						"skill_name": map[string]interface{}{
							"type":        "string",
							"description": "Human-readable skill name",
						},
						"reason": map[string]interface{}{
							"type":        "string",
							"description": "Why this skill would be useful for the current task",
						},
					},
					"required": []string{"skill_id", "skill_name"},
				},
			},
			Handler: func(args map[string]interface{}) (string, error) {
				skillID, _ := args["skill_id"].(string)
				skillName, _ := args["skill_name"].(string)
				reason, _ := args["reason"].(string)

				if skillID == "" || skillName == "" {
					return "Error: skill_id and skill_name are required", nil
				}

				// 1. Update session to "hold" with HoldInfo
				session.Status = "hold"
				session.HoldInfo = &chat.HoldInfo{
					Type:    "skill_install",
					SkillID: skillID,
					Name:    skillName,
					Reason:  reason,
				}
				if err := s.chatStore.Save(userID, scope, session); err != nil {
					log.Printf("⚠️  [chat:%s] Failed to save hold state: %v", sessionID[:8], err)
				}

				log.Printf("⏸ [chat:%s] Agent paused — waiting for user action on skill: %s", sessionID[:8], skillName)

				// 2. Block until user clicks Continue/Skip or timeout
				holdCh := s.createHoldChannel(sessionID)
				timeout := time.After(10 * time.Minute)

				select {
				case signal := <-holdCh:
					// Clear hold state
					session.Status = "running"
					session.HoldInfo = nil
					if err := s.chatStore.Save(userID, scope, session); err != nil {
						log.Printf("⚠️  [chat:%s] Failed to clear hold state: %v", sessionID[:8], err)
					}

					if signal.Action == "abort" {
						log.Printf("⏭ [chat:%s] User skipped skill install, resuming agent", sessionID[:8])
						return fmt.Sprintf("User chose to skip installing '%s'. Continue without it.", skillName), nil
					}
					// "continue" — user installed and clicked Continue
					log.Printf("▶ [chat:%s] User continued after skill install, resuming agent", sessionID[:8])
					return fmt.Sprintf("Skill '%s' has been installed successfully and is now available. You can use it to complete the task.", skillName), nil

				case <-timeout:
					log.Printf("⏰ [chat:%s] Hold timed out after 10 minutes, auto-skipping", sessionID[:8])
					s.removeHoldChannel(sessionID)
					session.Status = "running"
					session.HoldInfo = nil
					if err := s.chatStore.Save(userID, scope, session); err != nil {
						log.Printf("⚠️  [chat:%s] Failed to clear hold state: %v", sessionID[:8], err)
					}
					return fmt.Sprintf("Installation of '%s' timed out. Continuing without it.", skillName), nil
				}
			},
		},
	}
}

// buildSessionInfoTool creates a tool that lets the agent query current session info.
// liveUsage provides real-time token counts from the current agent loop iteration,
// so the total includes both historical usage and the in-progress loop.
func buildSessionInfoTool(session *chat.Session, model string, liveUsage *provider.Usage) mcp.ExtraBuiltinTool {
	return mcp.ExtraBuiltinTool{
		Schema: provider.Tool{
			Name:        "tofi_session_info",
			Description: "Get token usage, cost, model name, and message count for the current chat. ALWAYS use this tool when the user asks about usage, tokens, cost, billing, session info, or statistics. Returns: session ID, model, message count, input/output tokens, total cost, active skills.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		Handler: func(args map[string]any) (string, error) {
			msgCount := len(session.Messages)
			// Combine historical session usage with in-progress agent loop usage
			inTok := session.Usage.InputTokens
			outTok := session.Usage.OutputTokens
			cost := session.Usage.Cost
			if liveUsage != nil {
				inTok += liveUsage.InputTokens
				outTok += liveUsage.OutputTokens
				cost += provider.CalculateCost(model, *liveUsage)
			}
			info := fmt.Sprintf(
				"Session: %s\nModel: %s\nMessages: %d\nInput tokens: %d\nOutput tokens: %d\nTotal cost: $%.4f",
				session.ID, model, msgCount, inTok, outTok, cost,
			)
			if session.Skills != "" {
				info += "\nSkills: " + session.Skills
			}
			if session.Title != "" {
				info += "\nTitle: " + session.Title
			}
			return info, nil
		},
	}
}

// generateSessionTitle uses AI to create a concise session title from the first user message.
// Runs asynchronously — updates the session title in storage when done.
func (s *Server) generateSessionTitle(userID, scope, sessionID, model, apiKey, firstMessage string) {
	p, err := provider.NewForModel(model, apiKey)
	if err != nil {
		log.Printf("⚠️  [title:%s] failed to create provider: %v", sessionID[:8], err)
		return
	}

	// Truncate long messages to save tokens
	msgRunes := []rune(firstMessage)
	if len(msgRunes) > 200 {
		msgRunes = msgRunes[:200]
	}

	req := &provider.ChatRequest{
		Model:  model,
		System: "Generate a short title (max 20 characters) for a chat session based on the user's first message. Reply with ONLY the title, no quotes, no punctuation, no explanation. Use the SAME language as the user's message.",
		Messages: []provider.Message{
			{Role: "user", Content: string(msgRunes)},
		},
	}

	resp, err := p.Chat(context.Background(), req)
	if err != nil {
		log.Printf("⚠️  [title:%s] AI title generation failed: %v", sessionID[:8], err)
		return
	}

	title := strings.TrimSpace(resp.Content)
	if title == "" {
		return
	}

	// Enforce max length
	titleRunes := []rune(title)
	if len(titleRunes) > 30 {
		titleRunes = titleRunes[:30]
	}
	title = string(titleRunes)

	// Load → update title → save
	session, err := s.chatStore.Load(userID, scope, sessionID)
	if err != nil {
		log.Printf("⚠️  [title:%s] failed to load session: %v", sessionID[:8], err)
		return
	}
	session.Title = title
	if err := s.chatStore.Save(userID, scope, session); err != nil {
		log.Printf("⚠️  [title:%s] failed to save title: %v", sessionID[:8], err)
		return
	}
	log.Printf("✅ [title:%s] %s", sessionID[:8], title)
}
