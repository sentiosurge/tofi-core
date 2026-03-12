package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"tofi-core/internal/apps"
	"tofi-core/internal/crypto"
	"tofi-core/internal/executor"
	"tofi-core/internal/mcp"
	"tofi-core/internal/models"
	"tofi-core/internal/skills"
	"tofi-core/internal/storage"

	"github.com/google/uuid"
)

// ── App CRUD Handlers ──

// handleListApps GET /api/v1/apps
func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	apps, err := s.db.ListApps(userID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list apps: %v", err), http.StatusInternalServerError)
		return
	}
	if apps == nil {
		apps = []*storage.AppRecord{}
	}

	type AppWithMeta struct {
		*storage.AppRecord
		PendingRuns int    `json:"pending_runs"`
		NextRunAt   string `json:"next_run_at,omitempty"`
	}

	result := make([]AppWithMeta, len(apps))
	for i, a := range apps {
		result[i] = AppWithMeta{AppRecord: a}
		runs, err := s.db.ListAppRuns(a.ID, "pending", 1)
		if err == nil && len(runs) > 0 {
			result[i].NextRunAt = runs[0].ScheduledAt
		}
		count, err := s.db.CountPendingAppRuns(a.ID)
		if err == nil {
			result[i].PendingRuns = count
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleGetApp GET /api/v1/apps/{id}
func (s *Server) handleGetApp(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	app, err := s.db.GetApp(id)
	if err != nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}
	if app.UserID != userID {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(app)
}

// handleCreateApp POST /api/v1/apps
func (s *Server) handleCreateApp(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	var req struct {
		Name             string           `json:"name"`
		Description      string           `json:"description"`
		Prompt           string           `json:"prompt"`
		SystemPrompt     string           `json:"system_prompt"`
		Model            string           `json:"model"`
		Skills           []string         `json:"skills"`
		ScheduleRules    *json.RawMessage `json:"schedule_rules"`
		Capabilities     *json.RawMessage `json:"capabilities"`
		BufferSize       *int             `json:"buffer_size"`
		RenewalThreshold *int             `json:"renewal_threshold"`
		Parameters       *json.RawMessage `json:"parameters"`
		ParameterDefs    *json.RawMessage `json:"parameter_defs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	skillsJSON, _ := json.Marshal(req.Skills)
	if req.Skills == nil {
		skillsJSON = []byte("[]")
	}

	scheduleRules := "[]"
	if req.ScheduleRules != nil {
		scheduleRules = string(*req.ScheduleRules)
	}

	capabilities := "{}"
	if req.Capabilities != nil {
		capabilities = string(*req.Capabilities)
	}

	parameters := "{}"
	if req.Parameters != nil {
		parameters = string(*req.Parameters)
	}

	parameterDefs := "{}"
	if req.ParameterDefs != nil {
		parameterDefs = string(*req.ParameterDefs)
	}

	bufferSize := 20
	if req.BufferSize != nil {
		bufferSize = *req.BufferSize
	}
	renewalThreshold := 5
	if req.RenewalThreshold != nil {
		renewalThreshold = *req.RenewalThreshold
	}

	app := &storage.AppRecord{
		ID:               uuid.New().String(),
		Name:             req.Name,
		Description:      req.Description,
		Prompt:           req.Prompt,
		SystemPrompt:     req.SystemPrompt,
		Model:            req.Model,
		Skills:           string(skillsJSON),
		ScheduleRules:    scheduleRules,
		Capabilities:     capabilities,
		BufferSize:       bufferSize,
		RenewalThreshold: renewalThreshold,
		IsActive:         false,
		UserID:           userID,
		Parameters:       parameters,
		ParameterDefs:    parameterDefs,
	}

	if err := s.db.CreateApp(app); err != nil {
		http.Error(w, fmt.Sprintf("failed to create app: %v", err), http.StatusInternalServerError)
		return
	}

	created, err := s.db.GetApp(app.ID)
	if err != nil {
		created = app
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(created)
}

// handleUpdateApp PUT /api/v1/apps/{id}
func (s *Server) handleUpdateApp(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	existing, err := s.db.GetApp(id)
	if err != nil || existing.UserID != userID {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}

	var req struct {
		Name             *string          `json:"name"`
		Description      *string          `json:"description"`
		Prompt           *string          `json:"prompt"`
		SystemPrompt     *string          `json:"system_prompt"`
		Model            *string          `json:"model"`
		Skills           []string         `json:"skills"`
		ScheduleRules    *json.RawMessage `json:"schedule_rules"`
		Capabilities     *json.RawMessage `json:"capabilities"`
		BufferSize       *int             `json:"buffer_size"`
		RenewalThreshold *int             `json:"renewal_threshold"`
		Parameters       *json.RawMessage `json:"parameters"`
		ParameterDefs    *json.RawMessage `json:"parameter_defs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Name != nil {
		existing.Name = *req.Name
	}
	if req.Description != nil {
		existing.Description = *req.Description
	}
	if req.Prompt != nil {
		existing.Prompt = *req.Prompt
	}
	if req.SystemPrompt != nil {
		existing.SystemPrompt = *req.SystemPrompt
	}
	if req.Model != nil {
		existing.Model = *req.Model
	}
	if req.Skills != nil {
		skillsJSON, _ := json.Marshal(req.Skills)
		existing.Skills = string(skillsJSON)
	}
	if req.ScheduleRules != nil {
		existing.ScheduleRules = string(*req.ScheduleRules)
	}
	if req.Capabilities != nil {
		existing.Capabilities = string(*req.Capabilities)
	}
	if req.BufferSize != nil {
		existing.BufferSize = *req.BufferSize
	}
	if req.RenewalThreshold != nil {
		existing.RenewalThreshold = *req.RenewalThreshold
	}
	if req.Parameters != nil {
		existing.Parameters = string(*req.Parameters)
	}
	if req.ParameterDefs != nil {
		existing.ParameterDefs = string(*req.ParameterDefs)
	}

	if err := s.db.UpdateApp(existing); err != nil {
		http.Error(w, fmt.Sprintf("failed to update app: %v", err), http.StatusInternalServerError)
		return
	}

	// If app is active and schedule changed, reschedule
	if existing.IsActive && req.ScheduleRules != nil && s.appScheduler != nil {
		s.appScheduler.RemoveApp(id)
		s.db.CancelPendingAppRuns(id)
		if err := s.appScheduler.ActivateApp(existing); err != nil {
			log.Printf("Failed to reschedule app %s: %v", id, err)
		}
	}

	updated, _ := s.db.GetApp(id)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}

// handleDeleteApp DELETE /api/v1/apps/{id}
func (s *Server) handleDeleteApp(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	if s.appScheduler != nil {
		s.appScheduler.RemoveApp(id)
	}

	if err := s.db.DeleteApp(id, userID); err != nil {
		http.Error(w, fmt.Sprintf("failed to delete app: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ── Schedule Handlers ──

// handleActivateApp POST /api/v1/apps/{id}/activate
func (s *Server) handleActivateApp(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	app, err := s.db.GetApp(id)
	if err != nil || app.UserID != userID {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}

	if app.ScheduleRules == "" || app.ScheduleRules == "[]" {
		http.Error(w, "app has no schedule rules configured", http.StatusBadRequest)
		return
	}

	if err := s.db.SetAppActive(id, userID, true); err != nil {
		http.Error(w, fmt.Sprintf("failed to activate: %v", err), http.StatusInternalServerError)
		return
	}

	if s.appScheduler != nil {
		app.IsActive = true
		if err := s.appScheduler.ActivateApp(app); err != nil {
			log.Printf("Failed to activate app %s in scheduler: %v", id, err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":  "activated",
		"message": "App schedule activated",
	})
}

// handleDeactivateApp POST /api/v1/apps/{id}/deactivate
func (s *Server) handleDeactivateApp(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	app, err := s.db.GetApp(id)
	if err != nil || app.UserID != userID {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}

	if err := s.db.SetAppActive(id, userID, false); err != nil {
		http.Error(w, fmt.Sprintf("failed to deactivate: %v", err), http.StatusInternalServerError)
		return
	}

	cancelled, _ := s.db.CancelPendingAppRuns(id)

	if s.appScheduler != nil {
		s.appScheduler.RemoveApp(id)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":    "deactivated",
		"cancelled": cancelled,
	})
}

// handleRunAppNow POST /api/v1/apps/{id}/run
func (s *Server) handleRunAppNow(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	app, err := s.db.GetApp(id)
	if err != nil || app.UserID != userID {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}

	if app.Prompt == "" {
		http.Error(w, "app has no prompt configured", http.StatusBadRequest)
		return
	}

	card, err := s.createAndExecuteAppCard(app, userID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to run app: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(card)
}

// handleListAppRuns GET /api/v1/apps/{id}/runs
func (s *Server) handleListAppRuns(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	app, err := s.db.GetApp(id)
	if err != nil || app.UserID != userID {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}

	status := r.URL.Query().Get("status")
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	runs, err := s.db.ListAppRuns(id, status, limit)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list runs: %v", err), http.StatusInternalServerError)
		return
	}
	if runs == nil {
		runs = []*storage.AppRunRecord{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(runs)
}

// handleParseSchedule POST /api/v1/apps/parse-schedule
func (s *Server) handleParseSchedule(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	var req struct {
		Text     string           `json:"text"`
		Timezone string           `json:"timezone"`
		Existing json.RawMessage  `json:"existing,omitempty"` // existing entries for smart merge
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Text == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}
	if req.Timezone == "" {
		req.Timezone = "America/New_York"
	}

	systemPrompt := `You are a schedule editor. Convert natural language into structured schedule entries, and intelligently merge with existing entries when provided.

Output ONLY valid JSON in this exact format:
{
  "entries": [
    {
      "time": "09:00",
      "repeat": { "type": "weekly", "days": ["mon", "tue", "wed", "thu", "fri"] },
      "enabled": true
    }
  ],
  "timezone": "Asia/Shanghai"
}

Entry fields:
- "time": required, HH:MM (24h), the run time
- "end_time": optional, HH:MM, only if interval_min > 0 (time window end)
- "interval_min": optional, minutes between runs within a window. 0 or omitted = run once at "time"
- "repeat": required object with:
  - "type": one of "daily", "weekly", "monthly", "once"
  - "days": for weekly, array of "mon","tue","wed","thu","fri","sat","sun"
  - "dates": for monthly, array of day numbers [1, 15, 28]
  - "date": for once, "YYYY-MM-DD" format
- "enabled": always true for new entries
- "label": optional short description

MERGE RULES (when existing entries are provided):
- Default: ADD new entries, keep all existing entries untouched
- Only MODIFY an existing entry if the user clearly refers to changing it (e.g. "把早上8点改成9点", "change 8am to 9am")
- Only REMOVE entries if the user explicitly says to remove, delete, or replace all
- When ambiguous, prefer adding over modifying
- Always preserve existing entries' enabled state

Examples:
- "每天早上9点" → entries:[{time:"09:00", repeat:{type:"daily"}, enabled:true}]
- "工作日9点到17点每小时" → entries:[{time:"09:00", end_time:"17:00", interval_min:60, repeat:{type:"weekly", days:["mon","tue","wed","thu","fri"]}, enabled:true}]
- "每月1号和15号下午3点" → entries:[{time:"15:00", repeat:{type:"monthly", dates:[1,15]}, enabled:true}]
- "3月15日下午2点" → entries:[{time:"14:00", repeat:{type:"once", date:"2026-03-15"}, enabled:true}]`

	// Build user prompt with existing entries context
	var promptParts []string
	promptParts = append(promptParts, fmt.Sprintf("Timezone: %s", req.Timezone))

	if len(req.Existing) > 0 && string(req.Existing) != "null" && string(req.Existing) != "[]" {
		promptParts = append(promptParts, fmt.Sprintf("\nEXISTING ENTRIES:\n%s", string(req.Existing)))
	}

	promptParts = append(promptParts, fmt.Sprintf("\nUser request: %s", req.Text))
	prompt := strings.Join(promptParts, "\n")

	model, apiKey, provider, err := s.resolveModelAndKey(userID, "")
	if err != nil {
		http.Error(w, fmt.Sprintf("no API key available: %v", err), http.StatusInternalServerError)
		return
	}

	result, err := callLLM(systemPrompt, prompt, apiKey, model, provider)
	if err != nil {
		http.Error(w, fmt.Sprintf("LLM call failed: %v", err), http.StatusInternalServerError)
		return
	}

	cleaned := cleanJSONResponse(result)

	var parsed json.RawMessage
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"raw":   result,
			"error": "LLM response is not valid JSON, please try again",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(parsed)
}

// cleanJSONResponse strips markdown code fences from LLM output
func cleanJSONResponse(s string) string {
	if len(s) > 6 && s[:3] == "```" {
		end := len(s) - 1
		for end > 0 && s[end] != '`' {
			end--
		}
		if end > 3 {
			start := 3
			for start < len(s) && s[start] != '\n' {
				start++
			}
			if start < end {
				s = s[start+1 : end-2]
			}
		}
	}
	return s
}

// createAndExecuteAppCard creates a KanbanCard from an App and executes it
func (s *Server) createAndExecuteAppCard(app *storage.AppRecord, userID string) (*storage.KanbanCardRecord, error) {
	// Resolve prompt template with parameter values
	prompt := apps.ResolveFromJSON(app.Prompt, app.Parameters, app.ParameterDefs)

	card := &storage.KanbanCardRecord{
		ID:          uuid.New().String(),
		Title:       prompt,
		Description: fmt.Sprintf("[App: %s] %s", app.Name, app.Description),
		Status:      "todo",
		AppID:       app.ID,
		AgentID:     app.ID, // backward compat
		UserID:      userID,
	}

	if err := s.db.CreateKanbanCard(card); err != nil {
		return nil, err
	}

	created, _ := s.db.GetKanbanCard(card.ID)
	if created == nil {
		created = card
	}

	go s.executeAppCard(created, app, userID)

	return created, nil
}

// executeAppCard executes a KanbanCard using App configuration
func (s *Server) executeAppCard(card *storage.KanbanCardRecord, app *storage.AppRecord, userID string) {
	requestedModel := app.Model
	s.executeWish(card, userID, requestedModel)
}

// ── Schedules Handlers ──

// handleGetUpcomingRuns GET /api/v1/schedules/upcoming
func (s *Server) handleGetUpcomingRuns(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	runs, err := s.db.GetUpcomingRuns(userID, limit)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to get upcoming runs: %v", err), http.StatusInternalServerError)
		return
	}
	if runs == nil {
		runs = []*storage.UpcomingRunRecord{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(runs)
}

// handleSkipRun POST /api/v1/schedules/{runId}/skip
func (s *Server) handleSkipRun(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	runID := r.PathValue("runId")

	if err := s.db.SkipAppRun(runID, userID); err != nil {
		http.Error(w, fmt.Sprintf("failed to skip run: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "skipped"})
}

// ── Manager Chat (SSE + AgentLoop) ──

// handleManagerChat POST /api/v1/apps/manager/chat — SSE streaming manager with full agent capabilities
func (s *Server) handleManagerChat(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	var req struct {
		Message  string                   `json:"message"`
		Messages []map[string]interface{} `json:"messages"` // conversation history [{role, content}, ...]
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Message == "" && len(req.Messages) == 0 {
		http.Error(w, "message or messages required", http.StatusBadRequest)
		return
	}

	// 1. Resolve model and API key
	model, apiKey, provider, err := s.resolveModelAndKey(userID, "")
	if err != nil {
		http.Error(w, fmt.Sprintf("no API key: %v", err), http.StatusBadRequest)
		return
	}

	// 2. Load current apps for context
	userApps, err := s.db.ListApps(userID)
	if err != nil {
		http.Error(w, "failed to load apps", http.StatusInternalServerError)
		return
	}
	var appsCtx []map[string]interface{}
	for _, a := range userApps {
		appsCtx = append(appsCtx, map[string]interface{}{
			"id":          a.ID,
			"name":        a.Name,
			"description": a.Description,
			"prompt":      a.Prompt,
			"model":       a.Model,
			"skills":      a.Skills,
			"schedule":    a.ScheduleRules,
			"is_active":   a.IsActive,
		})
	}
	appsJSON, _ := json.Marshal(appsCtx)

	// 3. Load default skills (web-search)
	defaultSkills := []string{"web-search"}
	var skillInstructions []string
	var skillTools []mcp.SkillTool
	secretEnv := make(map[string]string)
	var missingSecrets []string
	localStore := skills.NewLocalStore(s.config.HomeDir)

	for _, skillName := range defaultSkills {
		rec, err := s.db.GetSkillByName(userID, skillName)
		if err != nil {
			log.Printf("[manager] skill %q not found: %v", skillName, err)
			continue
		}
		if rec.Instructions != "" {
			skillInstructions = append(skillInstructions, rec.Instructions)
		}
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

		for _, secretName := range rec.RequiredSecretsList() {
			if _, ok := secretEnv[secretName]; ok {
				continue
			}
			secretRec, err := s.db.GetSecret(userID, secretName)
			if err != nil {
				missingSecrets = append(missingSecrets, fmt.Sprintf("Skill '%s' requires secret '%s'", rec.Name, secretName))
				continue
			}
			val, err := crypto.Decrypt(secretRec.EncryptedValue)
			if err != nil {
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

	fmt.Fprintf(w, "event: connected\ndata: {}\n\n")
	flusher.Flush()

	// Pre-flight: check missing secrets
	if len(missingSecrets) > 0 {
		errMsg := "⚠️ Missing configuration:\n"
		for _, ms := range missingSecrets {
			errMsg += "• " + ms + "\n"
		}
		errMsg += "\nPlease configure the required secrets in Settings → Secrets."
		sendSSEError(w, flusher, errMsg)
		return
	}

	// 5. Create sandbox
	sandboxDir, err := s.executor.CreateSandbox(executor.SandboxConfig{
		HomeDir: s.config.HomeDir,
		UserID:  userID,
		CardID:  "manager-chat",
	})
	if err != nil {
		sendSSEError(w, flusher, "Failed to create sandbox: "+err.Error())
		return
	}
	defer s.executor.Cleanup(sandboxDir)

	// 6. Build system prompt
	system := fmt.Sprintf(`You are the App Manager for Tofi — a platform where users create AI Apps that run on schedules.

## First Principles
Before making any changes, make sure you UNDERSTAND what the user wants:
- If the goal is unclear, ASK clarifying questions first
- If you see a better approach, SUGGEST it before acting
- Confirm your understanding before proposing changes
- Think step by step — plan before you act

## Your Capabilities
You can research (web search, fetch pages), search for skills on the marketplace, and propose changes to the user's apps.
When you want to make changes to apps, use the propose_action tool. The user will see your proposals and confirm before they take effect.

## Current Apps
%s

## Available Actions (via propose_action tool)
- create_app: Create a new app with name, description, prompt, skills, schedule
- update_app: Update an existing app's configuration
- delete_app: Delete an app
- activate_app: Enable an app's schedule
- deactivate_app: Disable an app's schedule
- run_app: Run an app immediately

## Schedule Format
schedule_rules uses entry-based format:
{"entries": [{"type": "interval", "interval": "4h"}, {"type": "cron", "cron": "0 9 * * 1-5"}], "timezone": "America/Los_Angeles"}

## Sandbox Environment
You have a sandbox shell for research tasks (curl, python3, etc.).
- Python packages: ALWAYS use python3 -m pip install <pkg>
- Multi-line Python: use heredoc python3 <<'PYEOF' ... PYEOF

## Rules
- Always respond in the same language as the user
- Be concise and helpful
- Use tools to research before proposing changes
- For update_app, only include fields being changed in data

Current time: %s`, string(appsJSON), time.Now().Format("2006-01-02 15:04:05 MST (Monday)"))

	for _, instructions := range skillInstructions {
		system += "\n\n---\n\n" + instructions
	}

	// 7. Build extra tools: propose_action, search_skills
	extraTools := s.buildManagerTools(userID)
	extraTools = append(extraTools, s.buildMemoryTools(userID, "")...)

	// 8. Build AgentConfig
	agentCfg := mcp.AgentConfig{
		System:     system,
		Prompt:     req.Message,
		Messages:   req.Messages,
		SkillTools: skillTools,
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
	agentCfg.AI.Model = model
	agentCfg.AI.APIKey = apiKey
	agentCfg.AI.Provider = provider
	agentCfg.AI.Endpoint = "https://api.openai.com/v1/chat/completions"

	// 9. Run agent loop
	ctx := models.NewExecutionContext("manager", userID, s.config.HomeDir)
	ctx.DB = s.db
	defer ctx.Close()

	log.Printf("🤖 [manager] user=%s model=%s skills=%v", userID, model, defaultSkills)

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

// buildManagerTools creates extra tools for the Manager agent
func (s *Server) buildManagerTools(userID string) []mcp.ExtraBuiltinTool {
	return []mcp.ExtraBuiltinTool{
		// propose_action: propose an app management action for user confirmation
		{
			Schema: mcp.OpenAITool{
				Type: "function",
				Function: mcp.OpenAIFunctionDef{
					Name:        "propose_action",
					Description: "Propose an app management action. The user will see the proposal and must confirm before it takes effect. Use this whenever you want to create, update, delete, activate, deactivate, or run an app.",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"action_type": map[string]interface{}{
								"type":        "string",
								"enum":        []string{"create_app", "update_app", "delete_app", "activate_app", "deactivate_app", "run_app"},
								"description": "The type of action to propose",
							},
							"app_id": map[string]interface{}{
								"type":        "string",
								"description": "The app ID (required for update/delete/activate/deactivate/run)",
							},
							"app_name": map[string]interface{}{
								"type":        "string",
								"description": "Display name of the app being modified",
							},
							"data": map[string]interface{}{
								"type":        "object",
								"description": "Action data. For create/update: {name, description, prompt, model, skills[], schedule_rules{}}. For delete/activate/deactivate/run: not needed.",
							},
						},
						"required": []string{"action_type"},
					},
				},
			},
			Handler: func(args map[string]interface{}) (string, error) {
				actionType, _ := args["action_type"].(string)
				appID, _ := args["app_id"].(string)
				appName, _ := args["app_name"].(string)
				data, _ := args["data"].(map[string]interface{})

				if actionType == "" {
					return "Error: action_type is required", nil
				}

				// Validate: update/delete/activate/deactivate/run need app_id
				switch actionType {
				case "update_app", "delete_app", "activate_app", "deactivate_app", "run_app":
					if appID == "" {
						return "Error: app_id is required for " + actionType, nil
					}
				case "create_app":
					if data == nil {
						return "Error: data is required for create_app (at minimum: name and prompt)", nil
					}
				}

				// Build display for user
				summary := fmt.Sprintf("Action proposed: %s", actionType)
				if appName != "" {
					summary += " — " + appName
				}
				if appID != "" {
					summary += " (id: " + appID[:8] + "...)"
				}

				return summary + "\n\nAwaiting user confirmation.", nil
			},
		},
		// search_skills: find skills on the marketplace
		{
			Schema: mcp.OpenAITool{
				Type: "function",
				Function: mcp.OpenAIFunctionDef{
					Name:        "search_skills",
					Description: "Search for skills on the skills.sh marketplace. Use this to find capabilities that could be added to an app.",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"query": map[string]interface{}{
								"type":        "string",
								"description": "Search query (e.g., 'web search', 'email', 'summarize')",
							},
						},
						"required": []string{"query"},
					},
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
					return "No skills found for: " + query, nil
				}
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("Found %d skills:\n\n", len(result.Skills)))
				for _, sk := range result.Skills {
					sb.WriteString(fmt.Sprintf("- **%s** (%s): %s\n", sk.Name, sk.Source, sk.Description))
				}
				return sb.String(), nil
			},
		},
		// list_installed_skills: see what skills are already available
		{
			Schema: mcp.OpenAITool{
				Type: "function",
				Function: mcp.OpenAIFunctionDef{
					Name:        "list_installed_skills",
					Description: "List all skills currently installed for this user.",
					Parameters: map[string]interface{}{
						"type":       "object",
						"properties": map[string]interface{}{},
					},
				},
			},
			Handler: func(args map[string]interface{}) (string, error) {
				records, err := s.db.ListSkills(userID)
				if err != nil {
					return fmt.Sprintf("Failed to list skills: %v", err), nil
				}
				if len(records) == 0 {
					return "No skills installed.", nil
				}
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("%d installed skills:\n\n", len(records)))
				for _, sk := range records {
					sb.WriteString(fmt.Sprintf("- **%s**: %s\n", sk.Name, sk.Description))
				}
				return sb.String(), nil
			},
		},
	}
}
