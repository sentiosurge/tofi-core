package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"tofi-core/internal/capability"
	"tofi-core/internal/crypto"
	"tofi-core/internal/executor"
	"tofi-core/internal/mcp"
	"tofi-core/internal/models"
	"tofi-core/internal/skills"
)

// handleTestChat provides a streaming SSE chat endpoint for testing capabilities.
// POST /api/v1/test/chat
func (s *Server) handleTestChat(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	var req struct {
		Message      string          `json:"message"`
		Model        string          `json:"model"`
		Skills       []string        `json:"skills"`        // e.g. ["web-search"]
		Capabilities json.RawMessage `json:"capabilities"`  // MCP servers etc (legacy)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	if req.Message == "" {
		http.Error(w, "message required", 400)
		return
	}

	// 1. Resolve model and API key
	model, apiKey, provider, err := s.resolveModelAndKey(userID, req.Model)
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

	// 3. Load requested skills
	var skillInstructions []string
	secretEnv := make(map[string]string)
	localStore := skills.NewLocalStore(s.config.HomeDir)

	for _, skillName := range req.Skills {
		// Load from database
		skillID := "system/" + skillName
		rec, err := s.db.GetSkill(skillID)
		if err != nil {
			log.Printf("[test-chat] skill %q not found: %v", skillName, err)
			continue
		}

		// Collect instructions
		if rec.Instructions != "" {
			skillInstructions = append(skillInstructions, rec.Instructions)
		}

		// Resolve required secrets → env vars
		for _, secretName := range rec.RequiredSecretsList() {
			if _, ok := secretEnv[secretName]; ok {
				continue // already resolved
			}
			secretRec, err := s.db.GetSecret(userID, secretName)
			if err != nil {
				log.Printf("[test-chat] secret %q for skill %q not found", secretName, skillName)
				continue
			}
			val, err := crypto.Decrypt(secretRec.EncryptedValue)
			if err != nil {
				log.Printf("[test-chat] decrypt secret %q failed: %v", secretName, err)
				continue
			}
			secretEnv[secretName] = val
		}

		// Symlink scripts into sandbox (done after sandbox creation below)
		_ = localStore // used below
		_ = rec
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

	// 6. Symlink skill scripts into sandbox
	for _, skillName := range req.Skills {
		skillDir := localStore.SkillDir(skillName)
		if _, err := os.Stat(filepath.Join(skillDir, "scripts")); err == nil {
			// Create skills/{name} symlink in sandbox
			sandboxSkillsDir := filepath.Join(sandboxDir, "skills", skillName)
			os.MkdirAll(filepath.Dir(sandboxSkillsDir), 0755)
			os.Symlink(skillDir, sandboxSkillsDir)
		}
	}

	// 7. Build system prompt
	system := `You are a knowledgeable AI assistant with access to tools.
Always respond in the same language as the user.
Provide thorough, well-structured answers with sufficient detail.`

	// Append skill instructions
	for _, instructions := range skillInstructions {
		system += "\n\n---\n\n" + instructions
	}

	// 8. Build AgentConfig
	agentCfg := mcp.AgentConfig{
		System:     system,
		Prompt:     req.Message,
		MCPServers: capMCPServers,
		ExtraTools: extraTools,
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
	}
	agentCfg.AI.Model = model
	agentCfg.AI.APIKey = apiKey
	agentCfg.AI.Provider = provider
	agentCfg.AI.Endpoint = "https://api.openai.com/v1/chat/completions"

	// 9. Run agent loop
	ctx := models.NewExecutionContext("test", userID, s.config.HomeDir)
	ctx.DB = s.db
	defer ctx.Close()

	log.Printf("🧪 [test-chat] user=%s model=%s skills=%v caps=%s", userID, model, req.Skills, capsJSON)

	result, err := mcp.RunAgentLoop(agentCfg, ctx)
	if err != nil {
		sendSSEError(w, flusher, "Agent error: "+err.Error())
		return
	}

	// 10. Send final result
	done, _ := json.Marshal(map[string]string{"result": result})
	fmt.Fprintf(w, "event: done\ndata: %s\n\n", done)
	flusher.Flush()
}

func sendSSEError(w http.ResponseWriter, flusher http.Flusher, msg string) {
	data, _ := json.Marshal(map[string]string{"error": msg})
	fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
	flusher.Flush()
}
