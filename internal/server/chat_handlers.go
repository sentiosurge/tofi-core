package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"tofi-core/internal/capability"
	"tofi-core/internal/chat"
	"tofi-core/internal/crypto"
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

	// 3. Resolve model and API key
	model := session.Model
	if model == "" {
		model = idx.Model
	}
	resolvedModel, apiKey, _, err := s.resolveModelAndKey(userID, model)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	// 4. Build system prompt based on scope
	systemPrompt := s.buildChatSystemPrompt(userID, idx.Scope)

	// 5. Load skills
	var skillNames []string
	if session.Skills != "" {
		skillNames = strings.Split(session.Skills, ",")
	}
	var skillInstructions []string
	var skillTools []mcp.SkillTool
	secretEnv := make(map[string]string)
	localStore := skills.NewLocalStore(s.config.HomeDir)

	for _, skillName := range skillNames {
		skillName = strings.TrimSpace(skillName)
		if skillName == "" {
			continue
		}
		rec, err := s.db.GetSkillByName(userID, skillName)
		if err != nil {
			log.Printf("[chat] skill %q not found: %v", skillName, err)
			continue
		}
		if rec.Instructions != "" {
			skillInstructions = append(skillInstructions, rec.Instructions)
		}
		st := mcp.SkillTool{
			ID: rec.ID, Name: rec.Name,
			Description: rec.Description, Instructions: rec.Instructions,
		}
		if rec.HasScripts {
			skillDir := localStore.SkillDir(rec.Name)
			if abs, err := filepath.Abs(skillDir); err == nil {
				skillDir = abs
			}
			st.SkillDir = skillDir
		}
		skillTools = append(skillTools, st)

		// Resolve secrets
		for _, secretName := range rec.RequiredSecretsList() {
			if _, ok := secretEnv[secretName]; ok {
				continue
			}
			secretRec, err := s.db.GetSecret(userID, secretName)
			if err != nil {
				continue
			}
			val, err := crypto.Decrypt(secretRec.EncryptedValue)
			if err != nil {
				continue
			}
			secretEnv[secretName] = val
		}
	}

	// Append skill instructions to system prompt
	for _, inst := range skillInstructions {
		systemPrompt += "\n\n---\n\n" + inst
	}

	// 6. Build provider messages from session history
	providerMessages := chat.BuildProviderMessages(session, req.Message, resolvedModel)

	// 7. Set up SSE
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

	// 8. Parse capabilities for agent scope
	var capMCPServers []mcp.MCPServerConfig
	var extraTools []mcp.ExtraBuiltinTool
	if agentName, ok := strings.CutPrefix(idx.Scope, chat.ScopeAgentPrefix); ok {
		if agentDef, err := s.workspace.ReadAgent(userID, agentName); err == nil {
			if agentDef.Config.Capabilities != nil {
				capsJSON, _ := json.Marshal(agentDef.Config.Capabilities)
				caps, err := capability.Parse(string(capsJSON))
				if err == nil && caps != nil {
					secretGetter := func(name string) (string, error) {
						rec, err := s.db.GetSecret(userID, name)
						if err != nil {
							return "", err
						}
						return crypto.Decrypt(rec.EncryptedValue)
					}
					_ = capability.ResolveSecrets(caps, secretGetter)
					capMCPServers = capability.BuildMCPServers(caps)
					extraTools = capability.BuildExtraTools(caps, secretGetter)
				}
			}
		}
	}

	// 9. Create sandbox
	sandboxDir, err := s.executor.CreateSandbox(executor.SandboxConfig{
		HomeDir: s.config.HomeDir,
		UserID:  userID,
		CardID:  "chat-" + sessionID,
	})
	if err != nil {
		sendSSEEvent(w, flusher, "error", map[string]string{"error": "Failed to create sandbox: " + err.Error()})
		return
	}
	defer s.executor.Cleanup(sandboxDir)

	// 10. Build AgentConfig
	agentCfg := mcp.AgentConfig{
		System:     systemPrompt,
		Messages:   providerMessages,
		MCPServers: capMCPServers,
		SkillTools: skillTools,
		ExtraTools: append(extraTools, s.buildMemoryTools(userID, "")...),
		SandboxDir: sandboxDir,
		UserDir:    userID,
		Executor:   s.executor,
		SecretEnv:  secretEnv,
		OnStreamChunk: func(cardID, delta string) {
			chunk, _ := json.Marshal(map[string]string{"delta": delta})
			fmt.Fprintf(w, "event: chunk\ndata: %s\n\n", chunk)
			flusher.Flush()
		},
		OnToolCall: func(toolName, input, output string, durationMs int64) {
			tc, _ := json.Marshal(map[string]any{
				"tool": toolName, "input": input,
				"output": output, "duration_ms": durationMs,
			})
			fmt.Fprintf(w, "event: tool_call\ndata: %s\n\n", tc)
			flusher.Flush()
		},
		OnContextCompact: func(summary string, originalTokens, compactedTokens int) {
			session.Summary = summary
			data, _ := json.Marshal(map[string]any{
				"summary": summary, "original_tokens": originalTokens,
				"compacted_tokens": compactedTokens,
			})
			fmt.Fprintf(w, "event: context_compact\ndata: %s\n\n", data)
			flusher.Flush()
		},
	}

	p, err := provider.NewForModel(resolvedModel, apiKey)
	if err != nil {
		sendSSEEvent(w, flusher, "error", map[string]string{"error": "Failed to create provider: " + err.Error()})
		return
	}
	agentCfg.Provider = p
	agentCfg.Model = resolvedModel

	// 11. Run agent loop
	ctx := models.NewExecutionContext("chat", userID, s.config.HomeDir)
	ctx.DB = s.db
	defer ctx.Close()

	log.Printf("💬 [chat:%s] user=%s model=%s skills=%v scope=%s",
		sessionID[:8], userID, resolvedModel, skillNames, idx.Scope)

	agentResult, err := mcp.RunAgentLoop(agentCfg, ctx)
	if err != nil {
		sendSSEEvent(w, flusher, "error", map[string]string{"error": "Agent error: " + err.Error()})
		return
	}

	// 12. Persist messages to session XML
	session.AddMessage(chat.Message{
		Role:    "user",
		Content: req.Message,
	})

	// Add assistant response + any tool messages from the agent result
	// The agent loop returns the final content; intermediate tool calls
	// are captured via the Messages field in AgentConfig
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
		runes := []rune(req.Message)
		if len(runes) > 50 {
			runes = runes[:50]
		}
		session.Title = string(runes)
	}

	// Update model if it was auto-resolved
	if session.Model == "" {
		session.Model = resolvedModel
	}

	// Save session
	if err := s.chatStore.Save(userID, idx.Scope, session); err != nil {
		log.Printf("⚠️  [chat:%s] failed to save session: %v", sessionID[:8], err)
	}

	// 13. Send done event
	contextPct := chat.ContextUsagePercent(agentResult.TotalUsage.InputTokens, resolvedModel)
	done, _ := json.Marshal(map[string]any{
		"result":                agentResult.Content,
		"model":                 agentResult.Model,
		"total_input_tokens":    agentResult.TotalUsage.InputTokens,
		"total_output_tokens":   agentResult.TotalUsage.OutputTokens,
		"total_cost":            agentResult.TotalCost,
		"llm_calls":             agentResult.LLMCalls,
		"context_usage_percent": contextPct,
	})
	fmt.Fprintf(w, "event: done\ndata: %s\n\n", done)
	flusher.Flush()
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

Current time: %s`, time.Now().Format("2006-01-02 15:04:05 MST (Monday)"))
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
