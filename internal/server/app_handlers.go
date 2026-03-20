package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"tofi-core/internal/apps"
	"tofi-core/internal/crypto"
	"tofi-core/internal/executor"
	"tofi-core/internal/mcp"
	"tofi-core/internal/models"
	"tofi-core/internal/provider"
	"tofi-core/internal/skills"
	"tofi-core/internal/storage"
	"tofi-core/internal/workspace"
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
		// Compute next run time dynamically from schedule rules
		if a.IsActive && a.ScheduleRules != "" {
			nextTimes := ExpandSchedule(a.ScheduleRules, time.Now(), 1)
			if len(nextTimes) > 0 {
				result[i].NextRunAt = nextTimes[0].UTC().Format("2006-01-02T15:04:05Z")
			}
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
// Flow: parse request → build AgentDef → write files → sync to DB index
func (s *Server) handleCreateApp(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	var req struct {
		ID               string           `json:"id"`
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
	if req.ID == "" {
		http.Error(w, "id is required (lowercase + hyphens, e.g. 'daily-weather')", http.StatusBadRequest)
		return
	}
	if !isValidAppID(req.ID) {
		http.Error(w, "id must be kebab-case (lowercase letters, digits, hyphens only)", http.StatusBadRequest)
		return
	}

	// Build AgentDef from request
	def := requestToAgentDef(req.ID, req.Name, req.Description, req.Prompt, req.SystemPrompt, req.Model,
		req.Skills, req.ScheduleRules, req.Capabilities, req.BufferSize, req.RenewalThreshold,
		req.ParameterDefs)

	// Step 1: Write agent files to disk (source of truth)
	if s.workspace != nil {
		if err := s.workspace.WriteAgent(userID, def); err != nil {
			http.Error(w, fmt.Sprintf("failed to write agent files: %v", err), http.StatusInternalServerError)
			return
		}
	}

	// Step 2: Sync to DB index
	var record *storage.AppRecord
	if s.workspaceSync != nil {
		synced, err := s.workspaceSync.SyncAgentToDB(userID, req.ID)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to sync agent to index: %v", err), http.StatusInternalServerError)
			return
		}
		record = synced
	} else {
		// Fallback: direct DB write
		record = workspace.AgentDefToRecord(userID, def)
		if err := s.db.CreateApp(record); err != nil {
			http.Error(w, fmt.Sprintf("failed to create app: %v", err), http.StatusInternalServerError)
			return
		}
	}

	created, err := s.db.GetApp(record.ID)
	if err != nil {
		created = record
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(created)
}

// handleUpdateApp PUT /api/v1/apps/{id}
// Flow: merge updates → convert to AgentDef → write files → sync to DB index
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

	// Merge updates into existing record
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

	// Step 1: Write updated agent files to disk (directory = ID, unchanged)
	if s.workspace != nil {
		def := workspace.RecordToAgentDef(existing)
		if err := s.workspace.WriteAgent(userID, def); err != nil {
			http.Error(w, fmt.Sprintf("failed to write agent files: %v", err), http.StatusInternalServerError)
			return
		}
	}

	// Step 2: Sync to DB index
	if s.workspaceSync != nil {
		synced, err := s.workspaceSync.SyncAgentToDB(userID, existing.ID)
		if err != nil {
			log.Printf("[app-update] sync failed, falling back to direct DB: %v", err)
			if err := s.db.UpdateApp(existing); err != nil {
				http.Error(w, fmt.Sprintf("failed to update app: %v", err), http.StatusInternalServerError)
				return
			}
		} else {
			// Preserve fields that only live in DB (not in files)
			synced.IsActive = existing.IsActive
			synced.Parameters = existing.Parameters
			synced.ID = existing.ID
			if err := s.db.UpdateApp(synced); err != nil {
				http.Error(w, fmt.Sprintf("failed to update app index: %v", err), http.StatusInternalServerError)
				return
			}
		}
	} else {
		if err := s.db.UpdateApp(existing); err != nil {
			http.Error(w, fmt.Sprintf("failed to update app: %v", err), http.StatusInternalServerError)
			return
		}
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
// Flow: get app name → delete files → remove from DB index
func (s *Server) handleDeleteApp(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	// Get app name before deleting (needed for file deletion)
	app, err := s.db.GetApp(id)
	if err != nil || app.UserID != userID {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}

	if s.appScheduler != nil {
		s.appScheduler.RemoveApp(id)
	}

	// Step 1: Delete agent files from disk (directory = ID)
	if s.workspace != nil && app.ID != "" {
		if err := s.workspace.DeleteAgent(userID, app.ID); err != nil {
			log.Printf("[app-delete] failed to delete agent files for %q: %v", app.ID, err)
			// Continue to delete from DB even if file deletion fails
		}
	}

	// Step 2: Remove from DB index
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
// All runs go through the Dispatcher — manual trigger creates an app_run and dispatches immediately.
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

	run, err := s.appScheduler.DispatchManualRun(app, userID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to run app: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(run)
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

	model, apiKey, _, err := s.resolveModelAndKey(userID, "")
	if err != nil {
		http.Error(w, fmt.Sprintf("no API key available: %v", err), http.StatusInternalServerError)
		return
	}

	p, err := provider.NewForModel(model, apiKey)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to create provider: %v", err), http.StatusInternalServerError)
		return
	}
	llmResp, err := p.Chat(r.Context(), &provider.ChatRequest{
		Model:  model,
		System: systemPrompt,
		Messages: []provider.Message{
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("LLM call failed: %v", err), http.StatusInternalServerError)
		return
	}
	result := llmResp.Content
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
	model, apiKey, _, err := s.resolveModelAndKey(userID, "")
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

	// 3. Load default skills (app-manager pack + web-search)
	defaultSkills := []string{"app-manager", "web-search"}
	// Include sub-skills from the app-manager pack
	if packSkills, err := s.db.ListSkillsBySourceURL("system://app-manager"); err == nil {
		for _, ps := range packSkills {
			defaultSkills = append(defaultSkills, ps.Name)
		}
	}
	var skillInstructions []string
	var skillTools []mcp.SkillTool
	secretEnv := make(map[string]string)
	var missingSecrets []string
	localStore := skills.NewLocalStore(s.config.HomeDir)

	// Inject API credentials for app-manager scripts
	secretEnv["TOFI_API_URL"] = fmt.Sprintf("http://localhost:%d/api/v1", s.config.Port)
	if authHeader := r.Header.Get("Authorization"); strings.HasPrefix(authHeader, "Bearer ") {
		secretEnv["TOFI_TOKEN"] = strings.TrimPrefix(authHeader, "Bearer ")
	}

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

	// 5b. Pre-symlink default skill scripts into sandbox so sandbox_exec can find them immediately
	for _, st := range skillTools {
		if st.SkillDir == "" {
			continue
		}
		symlinkDir := filepath.Join(sandboxDir, "skills")
		os.MkdirAll(symlinkDir, 0755)
		link := filepath.Join(symlinkDir, st.Name)
		if _, err := os.Lstat(link); os.IsNotExist(err) {
			if err := os.Symlink(st.SkillDir, link); err != nil {
				log.Printf("[manager] failed to symlink skill %q: %v", st.Name, err)
			}
		}
	}

	// 6. Build system prompt
	system := fmt.Sprintf(`You are the App Manager for Tofi — a platform where users create AI Apps that run on schedules.

## First Principles
Before making any changes, make sure you UNDERSTAND what the user wants:
- If the goal is unclear, ASK clarifying questions first
- If you see a better approach, SUGGEST it before acting
- Think step by step — plan before you act

## Your Capabilities
- Research: web search, fetch pages
- Skills: search the marketplace for skills to add to apps
- App Management: create, update, delete, activate, deactivate, run apps using tofi_* tools

## Current Apps
%s

## How to Make Changes
Use the built-in tofi_* tools to manage apps:
- tofi_list_apps — list all apps
- tofi_create_app — create a new app
- tofi_update_app — update an existing app
- tofi_delete_app — delete an app
- tofi_run_app — trigger a manual run
- tofi_list_app_runs — view run history
- tofi_toggle_schedule — enable/disable schedule
- tofi_list_notify_targets — list notification receivers
- tofi_set_notify_targets — configure notifications

## Writing App Prompts — You Are the Manager

The --prompt is the ONLY instruction the App Agent receives when it runs. The App Agent is a capable employee who knows nothing about your conversation with the user. You are the manager writing a clear work brief.

**Your responsibility**: The user tells you what they want. You turn that into a professional, complete brief that any competent agent can execute independently. Never pass the user's raw words as the prompt — that's forwarding an email without context.

**When the user is vague**: You make the decisions. Choose reasonable defaults for scope, sources, format, and language. Explain your choices when presenting the plan. A good manager doesn't go back to the CEO asking "which platforms?" — they pick sensible ones and present the plan.

**What makes a good brief**:
- A clear role and deliverable
- Defined scope (which sources, how many items, what region/language)
- Quality standards (what "done well" looks like, what's unacceptable)
- Output format and language
- Time relevance — if the task needs current data, say so explicitly (today? this week?)

**The prompt must be self-contained** — include everything needed. The agent has zero context beyond this prompt.

## Workflow
1. Understand the user's request — if you need the user to choose between specific options, use the **ask_question** tool
2. If user is vague, make reasonable choices yourself
3. Research if needed (web search, skill search)
4. Call the **present_plan** tool with a structured plan — only include fields relevant to this action
5. Wait for the user's response (Approve or Deny)
6. After approval, execute using the tofi_* tools
7. Verify with list or get

IMPORTANT:
- Use ask_question when you need the user to choose from specific options (e.g. notification channels, language, timezone). After asking, WAIT for their answer.
- ALWAYS call present_plan before create/update/delete — never execute without user approval
- After the user approves (e.g. "确认", "Approve"), execute IMMEDIATELY. Do NOT call present_plan again — the plan was already approved.
- Only include fields in present_plan that are relevant. For example: updating only the prompt? Don't include schedule or capabilities.
- For simple queries (list, get, activate/deactivate/run), you can execute directly
- Schedule JSON format: {"entries": [{"time":"09:00","repeat":{"type":"daily"},"enabled":true}], "timezone":"Asia/Shanghai"}
- Capabilities JSON: {"web_search":{"enabled":true}}
- If the app needs real-time data, ALWAYS enable web_search in capabilities

## Sandbox Environment
You have a sandbox shell (curl, python3, etc.).
- Python packages: python3 -m pip install <pkg>
- Multi-line Python: heredoc python3 <<'PYEOF' ... PYEOF

## Rules
- Always respond in the same language as the user
- Be concise and helpful

Current time: %s`, string(appsJSON), time.Now().Format("2006-01-02 15:04:05 MST (Monday)"))

	for _, instructions := range skillInstructions {
		system += "\n\n---\n\n" + instructions
	}

	// 7. Build extra tools: propose_action, search_skills
	extraTools := s.buildManagerTools(userID)
	extraTools = append(extraTools, s.buildMemoryTools(userID, "")...)
	extraTools = append(extraTools, s.buildBuiltinTools(userID)...)

	// 8. Convert messages to provider.Message format
	var providerMessages []provider.Message
	for _, m := range req.Messages {
		msg := provider.Message{
			Role:    fmt.Sprint(m["role"]),
			Content: fmt.Sprint(m["content"]),
		}
		providerMessages = append(providerMessages, msg)
	}

	// Build AgentConfig
	agentCfg := mcp.AgentConfig{
		System:     system,
		Prompt:     req.Message,
		Messages:   providerMessages,
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

	// Create Provider instance
	p, err := provider.NewForModel(model, apiKey)
	if err != nil {
		sendSSEError(w, flusher, "Failed to create provider: "+err.Error())
		return
	}
	agentCfg.Provider = p
	agentCfg.Model = model

	// 9. Run agent loop
	ctx := models.NewExecutionContext("manager", userID, s.config.HomeDir)
	ctx.DB = s.db
	defer ctx.Close()

	log.Printf("🤖 [manager] user=%s model=%s skills=%v", userID, model, defaultSkills)

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

// buildManagerTools creates extra tools for the Manager agent
func (s *Server) buildManagerTools(userID string) []mcp.ExtraBuiltinTool {
	return []mcp.ExtraBuiltinTool{
		// present_plan: structured plan for user approval
		{
			Schema: provider.Tool{
				Name:        "present_plan",
				Description: "Present an app plan to the user for approval. ALWAYS call this before executing any create/update/delete operation. Only include fields relevant to the action — e.g. if only updating the prompt, omit schedule and capabilities.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"action": map[string]interface{}{
							"type":        "string",
							"enum":        []string{"create", "update", "delete"},
							"description": "Plan action type",
						},
						"app_id": map[string]interface{}{
							"type":        "string",
							"description": "App ID (required for update/delete)",
						},
						"name": map[string]interface{}{
							"type":        "string",
							"description": "App name",
						},
						"description": map[string]interface{}{
							"type":        "string",
							"description": "Short app description",
						},
						"prompt": map[string]interface{}{
							"type":        "string",
							"description": "The complete, self-contained prompt the App Agent receives each run",
						},
						"capabilities": map[string]interface{}{
							"type":        "array",
							"items":       map[string]interface{}{"type": "string"},
							"description": "Capability list, e.g. [\"web_search\", \"web_fetch\"]",
						},
						"schedule": map[string]interface{}{
							"type":        "string",
							"description": "Human-readable schedule, e.g. '每天早上 8:00'",
						},
						"skills": map[string]interface{}{
							"type":        "array",
							"items":       map[string]interface{}{"type": "string"},
							"description": "Skill IDs to attach",
						},
					},
					"required": []string{"action"},
				},
			},
			Handler: func(args map[string]interface{}) (string, error) {
				return "Plan presented to user. Wait for their response (Approve or Deny) before proceeding.", nil
			},
		},
		// search_skills: find skills on the marketplace
		{
			Schema: provider.Tool{
				Name:        "tofi_search",
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
			Schema: provider.Tool{
				Name:        "list_installed_skills",
				Description: "List all skills currently installed for this user.",
				Parameters: map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
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
		// ask_question: structured question with options for user input
		{
			Schema: provider.Tool{
				Name:        "ask_question",
				Description: "Ask the user a structured question with selectable options. Use this when you need the user to choose between specific options (e.g. notification channels, language, timezone). The user will see clickable options and can select one or multiple, or type a custom answer.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"question": map[string]interface{}{
							"type":        "string",
							"description": "The question to ask the user",
						},
						"options": map[string]interface{}{
							"type":        "array",
							"items":       map[string]interface{}{"type": "string"},
							"description": "List of options for the user to choose from",
						},
						"multi_select": map[string]interface{}{
							"type":        "boolean",
							"description": "If true, user can select multiple options. Default: false",
						},
					},
					"required": []string{"question", "options"},
				},
			},
			Handler: func(args map[string]interface{}) (string, error) {
				return "Question presented to user. Wait for their answer before proceeding.", nil
			},
		},
	}
}

// isValidAppID checks if an ID is valid kebab-case (lowercase letters, digits, hyphens).
func isValidAppID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return false
		}
	}
	// Must not start or end with hyphen
	return id[0] != '-' && id[len(id)-1] != '-'
}

// requestToAgentDef converts API request fields into an AgentDef for file-based storage.
func requestToAgentDef(
	id, name, description, prompt, systemPrompt, model string,
	skillsList []string,
	scheduleRules, capabilities *json.RawMessage,
	bufferSize, renewalThreshold *int,
	parameterDefs *json.RawMessage,
) *apps.AgentDef {
	cfg := apps.AppConfig{
		ID:          id,
		Name:        name,
		Description: description,
		Model:       model,
		Skills:      skillsList,
	}

	if bufferSize != nil {
		cfg.BufferSize = *bufferSize
	} else {
		cfg.BufferSize = 20
	}
	if renewalThreshold != nil {
		cfg.RenewalThreshold = *renewalThreshold
	} else {
		cfg.RenewalThreshold = 5
	}

	// Parse capabilities JSON into map
	if capabilities != nil {
		var caps map[string]any
		if err := json.Unmarshal(*capabilities, &caps); err == nil {
			cfg.Capabilities = caps
		}
	}

	// Parse parameter defs JSON
	if parameterDefs != nil {
		var params map[string]*apps.AppParameter
		if err := json.Unmarshal(*parameterDefs, &params); err == nil {
			cfg.Parameters = params
		}
	}

	// Parse schedule rules JSON
	if scheduleRules != nil {
		var schedule apps.AppConfigSchedule
		if err := json.Unmarshal(*scheduleRules, &schedule); err == nil {
			cfg.Schedule = &schedule
		}
	}

	return &apps.AgentDef{
		Config:   cfg,
		AgentsMD: prompt,
		SoulMD:   systemPrompt,
	}
}
