package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"sync"
	"time"
	"tofi-core/internal/engine"
	"tofi-core/internal/models"
	"tofi-core/internal/parser"
	"tofi-core/internal/storage"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

// Scheduler manages cron-based workflow triggers
type Scheduler struct {
	cron       *cron.Cron
	server     *Server
	mu         sync.RWMutex
	entryMap   map[string]cron.EntryID // cronTriggerID -> cron.EntryID
}

func NewScheduler(server *Server) *Scheduler {
	return &Scheduler{
		cron:     cron.New(cron.WithSeconds()), // 支持秒级精度
		server:   server,
		entryMap: make(map[string]cron.EntryID),
	}
}

// Start loads all active cron triggers from DB and starts the scheduler
func (sc *Scheduler) Start() error {
	triggers, err := sc.server.db.ListActiveCronTriggers()
	if err != nil {
		return fmt.Errorf("failed to load cron triggers: %v", err)
	}

	for _, t := range triggers {
		if err := sc.addTrigger(t); err != nil {
			log.Printf("⚠️  Cron trigger %s failed to register: %v", t.ID, err)
		}
	}

	sc.cron.Start()
	log.Printf("⏰ Scheduler started with %d active cron triggers", len(triggers))
	return nil
}

// Stop gracefully stops the scheduler
func (sc *Scheduler) Stop() {
	ctx := sc.cron.Stop()
	<-ctx.Done()
	log.Println("⏰ Scheduler stopped")
}

// addTrigger registers a cron trigger
func (sc *Scheduler) addTrigger(t *storage.CronTriggerRecord) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	// Remove existing entry if present (for updates)
	if existingID, ok := sc.entryMap[t.ID]; ok {
		sc.cron.Remove(existingID)
	}

	triggerID := t.ID
	workflowID := t.WorkflowID
	userID := t.UserID

	entryID, err := sc.cron.AddFunc(t.Expression, func() {
		sc.executeWorkflow(triggerID, workflowID, userID)
	})
	if err != nil {
		return fmt.Errorf("invalid cron expression '%s': %v", t.Expression, err)
	}

	sc.entryMap[t.ID] = entryID
	return nil
}

// removeTrigger removes a cron trigger
func (sc *Scheduler) removeTrigger(triggerID string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	if entryID, ok := sc.entryMap[triggerID]; ok {
		sc.cron.Remove(entryID)
		delete(sc.entryMap, triggerID)
	}
}

// executeWorkflow is called when a cron trigger fires
func (sc *Scheduler) executeWorkflow(triggerID, workflowID, userID string) {
	log.Printf("⏰ Cron firing: trigger=%s workflow=%s", triggerID, workflowID)

	// Update last_executed timestamp
	if err := sc.server.db.UpdateCronLastExecuted(triggerID); err != nil {
		log.Printf("⚠️  Failed to update last_executed for trigger %s: %v", triggerID, err)
	}

	// Load workflow
	userWorkflowDir := filepath.Join(sc.server.config.HomeDir, userID, "workflows")
	wf, err := parser.ResolveWorkflow(workflowID, userWorkflowDir)
	if err != nil {
		wf, err = parser.ResolveWorkflow(workflowID, "workflows")
		if err != nil {
			log.Printf("❌ Cron %s: failed to resolve workflow '%s': %v", triggerID, workflowID, err)
			return
		}
	}

	if wf.ID == "" {
		wf.ID = workflowID
	}

	if err := engine.ValidateAll(wf); err != nil {
		log.Printf("❌ Cron %s: workflow validation failed: %v", triggerID, err)
		return
	}

	// Create execution
	uuidStr := uuid.New().String()[:4]
	execID := time.Now().Format("102150405") + "-cr-" + uuidStr

	ctx := models.NewExecutionContext(execID, userID, sc.server.config.HomeDir)
	ctx.SetWorkflowName(wf.Name)
	ctx.WorkflowID = wf.ID
	ctx.DB = sc.server.db

	initialInputs := map[string]interface{}{
		"data": map[string]interface{}{
			"_trigger":    "cron",
			"_trigger_id": triggerID,
			"_timestamp":  time.Now().UTC().Format(time.RFC3339),
		},
	}

	job := &WorkflowJob{
		ExecutionID:   execID,
		Workflow:      wf,
		Context:       ctx,
		InitialInputs: initialInputs,
		DB:            sc.server.db,
	}

	if err := sc.server.workerPool.Submit(job); err != nil {
		log.Printf("❌ Cron %s: failed to submit job: %v", triggerID, err)
		return
	}

	log.Printf("⏰ Cron triggered: workflow=%s exec=%s", workflowID, execID)
}

// --- Cron Management HTTP Handlers ---

func (s *Server) handleCreateCronTrigger(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)

	var req struct {
		WorkflowID  string `json:"workflow_id"`
		Expression  string `json:"expression"`
		Timezone    string `json:"timezone,omitempty"`
		Description string `json:"description,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.WorkflowID == "" || req.Expression == "" {
		http.Error(w, "workflow_id and expression are required", http.StatusBadRequest)
		return
	}
	if req.Timezone == "" {
		req.Timezone = "UTC"
	}

	// Validate cron expression
	parser := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	if _, err := parser.Parse(req.Expression); err != nil {
		http.Error(w, fmt.Sprintf("Invalid cron expression: %v", err), http.StatusBadRequest)
		return
	}

	id := uuid.New().String()

	if err := s.db.CreateCronTrigger(id, user, req.WorkflowID, req.Expression, req.Timezone, req.Description); err != nil {
		http.Error(w, fmt.Sprintf("Failed to create cron trigger: %v", err), http.StatusInternalServerError)
		return
	}

	// Register in scheduler if it exists
	if s.scheduler != nil {
		trigger := &storage.CronTriggerRecord{
			ID:         id,
			UserID:     user,
			WorkflowID: req.WorkflowID,
			Expression: req.Expression,
			Timezone:   req.Timezone,
			Active:     true,
		}
		if err := s.scheduler.addTrigger(trigger); err != nil {
			log.Printf("⚠️  Cron trigger %s saved to DB but failed to register in scheduler: %v", id, err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"id":          id,
		"workflow_id": req.WorkflowID,
		"expression":  req.Expression,
		"timezone":    req.Timezone,
	})
}

func (s *Server) handleListCronTriggers(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)
	workflowID := r.URL.Query().Get("workflow_id")

	triggers, err := s.db.ListCronTriggers(user, workflowID)
	if err != nil {
		http.Error(w, "Failed to list cron triggers", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(triggers)
}

func (s *Server) handleUpdateCronTrigger(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	var req struct {
		Expression  string `json:"expression"`
		Timezone    string `json:"timezone,omitempty"`
		Description string `json:"description,omitempty"`
		Active      *bool  `json:"active,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Timezone == "" {
		req.Timezone = "UTC"
	}

	active := true
	if req.Active != nil {
		active = *req.Active
	}

	// Validate expression if provided
	if req.Expression != "" {
		p := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
		if _, err := p.Parse(req.Expression); err != nil {
			http.Error(w, fmt.Sprintf("Invalid cron expression: %v", err), http.StatusBadRequest)
			return
		}
	}

	if err := s.db.UpdateCronTrigger(id, user, req.Expression, req.Timezone, req.Description, active); err != nil {
		http.Error(w, "Failed to update cron trigger", http.StatusInternalServerError)
		return
	}

	// Update in scheduler
	if s.scheduler != nil {
		if active && req.Expression != "" {
			trigger := &storage.CronTriggerRecord{
				ID:         id,
				UserID:     user,
				Expression: req.Expression,
				Active:     true,
			}
			// Need workflow_id — query from DB
			triggers, _ := s.db.ListCronTriggers(user, "")
			for _, t := range triggers {
				if t.ID == id {
					trigger.WorkflowID = t.WorkflowID
					break
				}
			}
			s.scheduler.addTrigger(trigger)
		} else if !active {
			s.scheduler.removeTrigger(id)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":     id,
		"active": active,
	})
}

func (s *Server) handleDeleteCronTrigger(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	if err := s.db.DeleteCronTrigger(id, user); err != nil {
		http.Error(w, "Failed to delete cron trigger", http.StatusInternalServerError)
		return
	}

	// Remove from scheduler
	if s.scheduler != nil {
		s.scheduler.removeTrigger(id)
	}

	w.WriteHeader(http.StatusNoContent)
}
