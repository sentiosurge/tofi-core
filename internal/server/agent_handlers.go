package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"tofi-core/internal/storage"

	"github.com/google/uuid"
)

// ── Agent CRUD Handlers ──

// handleListAgents GET /api/v1/agents
func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	agents, err := s.db.ListAgents(userID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list agents: %v", err), http.StatusInternalServerError)
		return
	}
	if agents == nil {
		agents = []*storage.AgentRecord{}
	}

	// Attach next_run info for each agent
	type AgentWithMeta struct {
		*storage.AgentRecord
		PendingRuns int    `json:"pending_runs"`
		NextRunAt   string `json:"next_run_at,omitempty"`
	}

	result := make([]AgentWithMeta, len(agents))
	for i, a := range agents {
		result[i] = AgentWithMeta{AgentRecord: a}
		runs, err := s.db.ListAgentRuns(a.ID, "pending", 1)
		if err == nil && len(runs) > 0 {
			result[i].NextRunAt = runs[0].ScheduledAt
		}
		count, err := s.db.CountPendingRuns(a.ID)
		if err == nil {
			result[i].PendingRuns = count
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleGetAgent GET /api/v1/agents/{id}
func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	agent, err := s.db.GetAgent(id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	if agent.UserID != userID {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(agent)
}

// handleCreateAgent POST /api/v1/agents
func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
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
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	// Serialize skills to JSON
	skillsJSON, _ := json.Marshal(req.Skills)
	if req.Skills == nil {
		skillsJSON = []byte("[]")
	}

	// Schedule rules
	scheduleRules := "[]"
	if req.ScheduleRules != nil {
		scheduleRules = string(*req.ScheduleRules)
	}

	// Capabilities
	capabilities := "{}"
	if req.Capabilities != nil {
		capabilities = string(*req.Capabilities)
	}

	bufferSize := 20
	if req.BufferSize != nil {
		bufferSize = *req.BufferSize
	}
	renewalThreshold := 5
	if req.RenewalThreshold != nil {
		renewalThreshold = *req.RenewalThreshold
	}

	agent := &storage.AgentRecord{
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
	}

	if err := s.db.CreateAgent(agent); err != nil {
		http.Error(w, fmt.Sprintf("failed to create agent: %v", err), http.StatusInternalServerError)
		return
	}

	created, err := s.db.GetAgent(agent.ID)
	if err != nil {
		created = agent
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(created)
}

// handleUpdateAgent PUT /api/v1/agents/{id}
func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	existing, err := s.db.GetAgent(id)
	if err != nil || existing.UserID != userID {
		http.Error(w, "agent not found", http.StatusNotFound)
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
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Merge fields
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

	if err := s.db.UpdateAgent(existing); err != nil {
		http.Error(w, fmt.Sprintf("failed to update agent: %v", err), http.StatusInternalServerError)
		return
	}

	updated, _ := s.db.GetAgent(id)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}

// handleDeleteAgent DELETE /api/v1/agents/{id}
func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	// Remove from scheduler if active
	if s.agentScheduler != nil {
		s.agentScheduler.RemoveAgent(id)
	}

	if err := s.db.DeleteAgent(id, userID); err != nil {
		http.Error(w, fmt.Sprintf("failed to delete agent: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ── Schedule Handlers ──

// handleActivateAgent POST /api/v1/agents/{id}/activate
func (s *Server) handleActivateAgent(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	agent, err := s.db.GetAgent(id)
	if err != nil || agent.UserID != userID {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	if agent.ScheduleRules == "" || agent.ScheduleRules == "[]" {
		http.Error(w, "agent has no schedule rules configured", http.StatusBadRequest)
		return
	}

	// Set active in DB
	if err := s.db.SetAgentActive(id, userID, true); err != nil {
		http.Error(w, fmt.Sprintf("failed to activate: %v", err), http.StatusInternalServerError)
		return
	}

	// Generate initial runs and add to scheduler
	if s.agentScheduler != nil {
		agent.IsActive = true
		if err := s.agentScheduler.ActivateAgent(agent); err != nil {
			log.Printf("⚠️ Failed to activate agent %s in scheduler: %v", id, err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "activated",
		"message": "Agent schedule activated",
	})
}

// handleDeactivateAgent POST /api/v1/agents/{id}/deactivate
func (s *Server) handleDeactivateAgent(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	agent, err := s.db.GetAgent(id)
	if err != nil || agent.UserID != userID {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	// Set inactive in DB
	if err := s.db.SetAgentActive(id, userID, false); err != nil {
		http.Error(w, fmt.Sprintf("failed to deactivate: %v", err), http.StatusInternalServerError)
		return
	}

	// Cancel pending runs
	cancelled, _ := s.db.CancelPendingRuns(id)

	// Remove from scheduler heap
	if s.agentScheduler != nil {
		s.agentScheduler.RemoveAgent(id)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "deactivated",
		"cancelled": cancelled,
	})
}

// handleRunAgentNow POST /api/v1/agents/{id}/run
func (s *Server) handleRunAgentNow(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	agent, err := s.db.GetAgent(id)
	if err != nil || agent.UserID != userID {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	if agent.Prompt == "" {
		http.Error(w, "agent has no prompt configured", http.StatusBadRequest)
		return
	}

	// Create a KanbanCard and execute immediately
	card, err := s.createAndExecuteAgentCard(agent, userID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to run agent: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(card)
}

// handleListAgentRuns GET /api/v1/agents/{id}/runs
func (s *Server) handleListAgentRuns(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	agent, err := s.db.GetAgent(id)
	if err != nil || agent.UserID != userID {
		http.Error(w, "agent not found", http.StatusNotFound)
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

	runs, err := s.db.ListAgentRuns(id, status, limit)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list runs: %v", err), http.StatusInternalServerError)
		return
	}
	if runs == nil {
		runs = []*storage.AgentRunRecord{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(runs)
}

// handleParseSchedule POST /api/v1/agents/parse-schedule
func (s *Server) handleParseSchedule(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	var req struct {
		Text     string `json:"text"`
		Timezone string `json:"timezone"`
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

	// Use LLM to parse natural language into ScheduleRule
	systemPrompt := `You are a schedule parser. Convert natural language schedule descriptions into structured JSON.

Output ONLY valid JSON in this exact format:
{
  "rules": [
    {
      "days": ["mon", "tue", "wed", "thu", "fri"],
      "windows": [
        { "start": "09:00", "end": "09:30", "interval_min": 5 }
      ]
    }
  ],
  "timezone": "America/New_York"
}

Rules:
- "days": array of "mon","tue","wed","thu","fri","sat","sun". Empty array = every day.
- "windows": time windows within the day. start/end in HH:MM format (24h). interval_min = minutes between runs.
- If interval_min = 0 or omitted, run once at "start" time.
- Multiple rules = different schedules combined.
- Use the provided timezone.

Examples:
- "每天早上9点" → days:[], windows:[{start:"09:00",end:"09:00",interval_min:0}]
- "工作日每小时" → days:["mon"-"fri"], windows:[{start:"09:00",end:"17:00",interval_min:60}]
- "每周一和周五下午3点" → days:["mon","fri"], windows:[{start:"15:00",end:"15:00",interval_min:0}]`

	prompt := fmt.Sprintf("Timezone: %s\n\nSchedule description: %s", req.Timezone, req.Text)

	// Resolve model/key
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

	// Try to parse the LLM response as JSON
	// Strip markdown code fences if present
	cleaned := cleanJSONResponse(result)

	var parsed json.RawMessage
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		// Return raw text if not valid JSON
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
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
	// Remove ```json ... ``` or ``` ... ```
	if len(s) > 6 && s[:3] == "```" {
		// Find end fence
		end := len(s) - 1
		for end > 0 && s[end] != '`' {
			end--
		}
		if end > 3 {
			// Skip opening fence line
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

// createAndExecuteAgentCard creates a KanbanCard from an Agent and executes it
func (s *Server) createAndExecuteAgentCard(agent *storage.AgentRecord, userID string) (*storage.KanbanCardRecord, error) {
	card := &storage.KanbanCardRecord{
		ID:          uuid.New().String(),
		Title:       agent.Prompt,
		Description: fmt.Sprintf("[Agent: %s] %s", agent.Name, agent.Description),
		Status:      "todo",
		AgentID:     agent.ID,
		UserID:      userID,
	}

	if err := s.db.CreateKanbanCard(card); err != nil {
		return nil, err
	}

	created, _ := s.db.GetKanbanCard(card.ID)
	if created == nil {
		created = card
	}

	// Execute asynchronously using agent config
	go s.executeAgentCard(created, agent, userID)

	return created, nil
}

// executeAgentCard executes a KanbanCard using Agent configuration
// This reuses the wish execution pipeline but with agent-specific config
func (s *Server) executeAgentCard(card *storage.KanbanCardRecord, agent *storage.AgentRecord, userID string) {
	// Determine model: agent.Model > auto-detect
	requestedModel := agent.Model

	// Reuse the wish execution pipeline
	s.executeWish(card, userID, requestedModel)
}
