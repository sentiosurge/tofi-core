package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"time"

	"tofi-core/internal/capability"
	"tofi-core/internal/crypto"
	"tofi-core/internal/executor"
	"tofi-core/internal/mcp"
	"tofi-core/internal/models"
	"tofi-core/internal/provider"
	"tofi-core/internal/skills"
)

// handleTestChat provides a streaming SSE chat endpoint for testing capabilities.
// POST /api/v1/test/chat
func (s *Server) handleTestChat(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	var req struct {
		Message      string                   `json:"message"`
		Messages     []map[string]interface{}  `json:"messages"`      // conversation history [{role, content}, ...]
		Model        string                   `json:"model"`
		Skills       []string                 `json:"skills"`        // e.g. ["web-search"]
		Capabilities json.RawMessage          `json:"capabilities"`  // MCP servers etc (legacy)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	if req.Message == "" && len(req.Messages) == 0 {
		http.Error(w, "message or messages required", 400)
		return
	}

	// 1. Resolve model and API key
	model, apiKey, _, err := s.resolveModelAndKey(userID, req.Model)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	// 2. Parse capabilities (MCP servers only)
	capsJSON := "{}"
	if req.Capabilities != nil {
		capsJSON = string(req.Capabilities)
	}
	caps, err := capability.Parse(capsJSON)
	var capMCPServers []mcp.MCPServerConfig
	var extraTools []mcp.ExtraBuiltinTool
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

	// 3. Load requested skills + pre-flight secret validation
	var skillInstructions []string
	var skillTools []mcp.SkillTool
	secretEnv := make(map[string]string)
	var missingSecrets []string // "skill_name: SECRET_KEY" pairs
	localStore := skills.NewLocalStore(s.config.HomeDir)

	for _, skillName := range req.Skills {
		// Load from database — cross-scope lookup (user → public → system)
		rec, err := s.db.GetSkillByName(userID, skillName)
		if err != nil {
			log.Printf("[test-chat] skill %q not found: %v", skillName, err)
			continue
		}

		// Collect instructions (appended to system prompt)
		if rec.Instructions != "" {
			skillInstructions = append(skillInstructions, rec.Instructions)
		}

		// Build SkillTool for tool-calling support (like wish_handlers)
		st := mcp.SkillTool{
			ID:           rec.ID,
			Name:         rec.Name,
			Description:  rec.Description,
			Instructions: rec.Instructions,
		}
		if rec.HasScripts {
			skillDir := localStore.SkillDir(rec.Name)
			if abs, err := filepath.Abs(skillDir); err == nil {
				skillDir = abs
			}
			st.SkillDir = skillDir
		}
		skillTools = append(skillTools, st)

		// Resolve required secrets → env vars (with missing tracking)
		for _, secretName := range rec.RequiredSecretsList() {
			if _, ok := secretEnv[secretName]; ok {
				continue // already resolved
			}
			secretRec, err := s.db.GetSecret(userID, secretName)
			if err != nil {
				log.Printf("[test-chat] secret %q for skill %q not found", secretName, rec.Name)
				missingSecrets = append(missingSecrets, fmt.Sprintf("Skill '%s' requires secret '%s'", rec.Name, secretName))
				continue
			}
			val, err := crypto.Decrypt(secretRec.EncryptedValue)
			if err != nil {
				log.Printf("[test-chat] decrypt secret %q failed: %v", secretName, err)
				missingSecrets = append(missingSecrets, fmt.Sprintf("Skill '%s': failed to decrypt secret '%s'", rec.Name, secretName))
				continue
			}
			secretEnv[secretName] = val
		}
	}

	// 4. Set up SSE
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

	// Send connected event
	fmt.Fprintf(w, "event: connected\ndata: {}\n\n")
	flusher.Flush()

	// Pre-flight: check for missing secrets before wasting API credits
	if len(missingSecrets) > 0 {
		errMsg := "⚠️ Missing configuration:\n"
		for _, ms := range missingSecrets {
			errMsg += "• " + ms + "\n"
		}
		errMsg += "\nPlease configure the required secrets in Settings → Secrets, then try again."
		sendSSEError(w, flusher, errMsg)
		return
	}

	// 5. Create sandbox
	sandboxDir, err := s.executor.CreateSandbox(executor.SandboxConfig{
		HomeDir: s.config.HomeDir,
		UserID:  userID,
		CardID:  "test-chat",
	})
	if err != nil {
		sendSSEError(w, flusher, "Failed to create sandbox: "+err.Error())
		return
	}
	defer s.executor.Cleanup(sandboxDir)

	// 6. Build system prompt
	system := fmt.Sprintf(`You are a knowledgeable AI assistant with access to tools.
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
- **NEVER respond with "go do it yourself" or "visit website X manually".** You are an executor — if one approach fails, try another.
- **Always deliver SOMETHING useful.** If you got partial data, present what you have.
- **When a skill's commands fail, write your OWN code.** Don't just relay errors to the user.
- **Fallback chain**: skill command → fix the command → write simpler code yourself → try alternative approach → present partial results.

## Skill Error Handling
When a skill's commands fail:
1. Read the error carefully — install missing dependencies (python3 -m pip install, npm install)
2. If a secret/API key is missing, tell the user which secret to configure in Settings → Secrets
3. Write your OWN code as fallback — never just explain the error
4. If one skill doesn't work, try to accomplish the goal with sandbox_exec directly

Current time: %s`, time.Now().Format("2006-01-02 15:04:05 MST (Monday)"))

	// Append skill instructions
	for _, instructions := range skillInstructions {
		system += "\n\n---\n\n" + instructions
	}

	// 7. Convert messages to provider.Message format
	var providerMessages []provider.Message
	for _, m := range req.Messages {
		msg := provider.Message{
			Role: fmt.Sprint(m["role"]),
		}
		if c, ok := m["content"].(string); ok {
			msg.Content = c
		}
		providerMessages = append(providerMessages, msg)
	}

	// 8. Build AgentConfig
	agentCfg := mcp.AgentConfig{
		System:     system,
		Prompt:     req.Message,
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
			tc, _ := json.Marshal(map[string]interface{}{
				"tool":        toolName,
				"input":       input,
				"output":      output,
				"duration_ms": durationMs,
			})
			fmt.Fprintf(w, "event: tool_call\ndata: %s\n\n", tc)
			flusher.Flush()
		},
		OnContextCompact: func(summary string, originalTokens, compactedTokens int) {
			data, _ := json.Marshal(map[string]interface{}{
				"summary":          summary,
				"original_tokens":  originalTokens,
				"compacted_tokens": compactedTokens,
			})
			fmt.Fprintf(w, "event: context_compact\ndata: %s\n\n", data)
			flusher.Flush()
		},
	}

	// Create Provider instance
	p, err := provider.NewForModel(model, apiKey)
	if err != nil {
		sendSSEError(w, flusher, "Failed to create provider: "+err.Error())
		return
	}
	agentCfg.Provider = p
	agentCfg.Model = model

	// 9. Run agent loop
	ctx := models.NewExecutionContext("test", userID, s.config.HomeDir)
	ctx.DB = s.db
	defer ctx.Close()

	log.Printf("🧪 [test-chat] user=%s model=%s skills=%v caps=%s", userID, model, req.Skills, capsJSON)

	agentResult, err := mcp.RunAgentLoop(agentCfg, ctx)
	if err != nil {
		sendSSEError(w, flusher, "Agent error: "+err.Error())
		return
	}

	// 10. Send final result
	done, _ := json.Marshal(map[string]string{"result": agentResult.Content})
	fmt.Fprintf(w, "event: done\ndata: %s\n\n", done)
	flusher.Flush()
}

func sendSSEError(w http.ResponseWriter, flusher http.Flusher, msg string) {
	data, _ := json.Marshal(map[string]string{"error": msg})
	fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
	flusher.Flush()
}
