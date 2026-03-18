package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"tofi-core/internal/chat"
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

	result, err := s.executeChatSession(userID, idx.Scope, session, req.Message, onEvent)
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
func (s *Server) executeChatSession(userID, scope string, session *chat.Session, message string, onEvent func(eventType string, data any)) (*mcp.AgentResult, error) {
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

	// 3. Load skills
	var skillNames []string
	if session.Skills != "" {
		skillNames = strings.Split(session.Skills, ",")
	}
	skillTools, skillInstructions, secretEnv := s.buildSkillToolsFromNames(userID, skillNames)

	// Append skill instructions to system prompt
	for _, inst := range skillInstructions {
		systemPrompt += "\n\n---\n\n" + inst
	}

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

	// 8. Build AgentConfig
	agentCfg := mcp.AgentConfig{
		System:     systemPrompt,
		Messages:   providerMessages,
		MCPServers: capMCPServers,
		SkillTools: skillTools,
		ExtraTools: append(append(append(append(extraTools,
			s.buildChatWishTools(userID, sessionID, session, scope)...),
			s.buildMemoryTools(userID, "")...),
			s.buildBuiltinTools(userID)...)),
		SandboxDir: sandboxDir,
		UserDir:    userID,
		Executor:   s.executor,
		SecretEnv:  secretEnv,
		OnStreamChunk: func(_ string, delta string) {
			emit("chunk", map[string]string{"delta": delta})
		},
		OnToolCall: func(toolName, input, output string, durationMs int64) {
			emit("tool_call", map[string]any{
				"tool": toolName, "input": input,
				"output": output, "duration_ms": durationMs,
			})
		},
		OnContextCompact: func(summary string, originalTokens, compactedTokens int) {
			session.Summary = summary
			emit("context_compact", map[string]any{
				"summary": summary, "original_tokens": originalTokens,
				"compacted_tokens": compactedTokens,
			})
		},
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

	// Auto-generate title from first message (safe for multi-byte UTF-8)
	if session.Title == "" && len(session.Messages) > 0 {
		runes := []rune(message)
		if len(runes) > 50 {
			runes = runes[:50]
		}
		session.Title = string(runes)
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
			prompt += "\n\nCurrent time: " + time.Now().Format("2006-01-02 15:04:05 MST (Monday)")
			return prompt
		}
	}

	// User main chat: generic assistant prompt
	return fmt.Sprintf(`You are a knowledgeable AI assistant with access to tools.
Always respond in the same language as the user.
Provide thorough, well-structured answers with sufficient detail.

## Sandbox Environment
You have a sandbox shell with full system tools available.
- **Python packages**: ALWAYS use python3 -m pip install <pkg> (NEVER bare "pip")
- **Node packages**: use npm install
- **Multi-line Python**: For anything beyond a trivial one-liner, ALWAYS use heredoc:
  python3 <<'PYEOF'
  ...code...
  PYEOF
- If a command is not found, install it with python3 -m pip or npm
- ALWAYS execute commands and return real results

## CRITICAL: Never Give Up
- **NEVER respond with "go do it yourself" or "visit website X manually".**
- **Always deliver SOMETHING useful.** If you got partial data, present what you have.
- **When a skill's commands fail, write your OWN code.**
- **Fallback chain**: skill command → fix the command → write simpler code yourself → try alternative approach → present partial results.

## Self-Improvement
You have long-term memory (tofi_save_memory, tofi_recall_memory). Use it to learn and improve:
- **On error**: Fix it, then save the lesson — tofi_save_memory with tags "lesson,error,{topic}"
- **On user correction**: Apply it, then save — tofi_save_memory with tags "lesson,correction"
- **On useful pattern**: Save it — tofi_save_memory with tags "pattern,{topic}"
- **Before tasks**: Recall relevant context — tofi_recall_memory with task keywords
- **Never make the same mistake twice.**

Current time: %s`, time.Now().Format("2006-01-02 15:04:05 MST (Monday)"))
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
