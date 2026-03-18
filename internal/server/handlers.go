package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"tofi-core/internal/crypto"
	"tofi-core/internal/engine"
	"tofi-core/internal/models"
	"tofi-core/internal/parser"
	"tofi-core/internal/storage"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// --- Request/Response Structs ---

type RunRequest struct {
	Workflow   string                 `json:"workflow"`              // Deprecated: use workflow_id or content
	WorkflowID string                 `json:"workflow_id,omitempty"` // ID of saved workflow to run
	Content    string                 `json:"content,omitempty"`     // YAML/JSON content for ephemeral run
	Inputs     map[string]interface{} `json:"inputs"`
}

type RunResponse struct {
	ExecutionID string `json:"execution_id"`
	Status      string `json:"status"`
	Message     string `json:"message"`
}

type SetupRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type SaveWorkflowRequest struct {
	ID       string `json:"id,omitempty"`     // Optional custom ID, if empty will be generated from Name
	OldID    string `json:"old_id,omitempty"` // If renaming, provide old ID to delete old files
	Name     string `json:"name"`
	Content  string `json:"content"`
	Metadata struct {
		Description string                        `json:"description"`
		Icon        string                        `json:"icon"`
		Positions   map[string]map[string]float64 `json:"positions,omitempty"` // Node positions: { nodeId: { x, y } }
	} `json:"metadata"`
}

type WorkflowListItem struct {
	ID          string    `json:"id"`   // Unique identifier (filename without extension)
	Name        string    `json:"name"` // Display name
	Description string    `json:"description"`
	Icon        string    `json:"icon"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type CreateSecretRequest struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type SecretResponse struct {
	Name      string `json:"name"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	Value     string `json:"value,omitempty"`
}

type SecretListResponse struct {
	Secrets []SecretResponse `json:"secrets"`
}

type ApproveRequest struct {
	Action string `json:"action"` // "approve" or "reject"
}

// --- Workflow Helper Functions ---

// generateWorkflowID converts a display name to a valid workflow ID
// Example: "My Awesome Workflow" -> "my_awesome_workflow"
func generateWorkflowID(displayName string) string {
	// Convert to lowercase
	id := strings.ToLower(displayName)
	// Replace spaces with underscores
	id = strings.ReplaceAll(id, " ", "_")
	// Remove special characters (keep only alphanumeric, underscores, and hyphens)
	var result strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// idToDisplayName converts a workflow ID to a display name
// Example: "demo_agent_research" -> "Demo Agent Research"
func idToDisplayName(id string) string {
	// Replace underscores with spaces
	name := strings.ReplaceAll(id, "_", " ")
	// Title case each word
	words := strings.Fields(name)
	for i, word := range words {
		if len(word) > 0 {
			words[i] = strings.ToUpper(string(word[0])) + word[1:]
		}
	}
	return strings.Join(words, " ")
}

func (s *Server) handleListAllArtifacts(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)
	limit := 100 // Default limit for dashboard
	offset := 0

	artifacts, err := s.db.ListAllArtifacts(user, limit, offset)
	if err != nil {
		http.Error(w, "Failed to list artifacts", http.StatusInternalServerError)
		return
	}
	if artifacts == nil {
		artifacts = make([]*models.ArtifactRecord, 0)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(artifacts)
}

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	count, err := s.db.CountUsers()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"initialized": count > 0})
}

func (s *Server) handleSetupAdmin(w http.ResponseWriter, r *http.Request) {
	count, _ := s.db.CountUsers()
	if count > 0 {
		http.Error(w, "System already initialized", http.StatusForbidden)
		return
	}

	var req SetupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "Failed to hash password", http.StatusInternalServerError)
		return
	}

	id := uuid.New().String()
	if err := s.db.SaveUser(id, req.Username, string(hash), "admin"); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	fmt.Fprint(w, "Admin created successfully")
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	user, err := s.db.GetUser(req.Username)
	if err != nil {
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}

	token, err := GenerateToken(user.Username, user.Role)
	if err != nil {
		http.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"token": token})
}

func (s *Server) handleGetMe(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)
	u, err := s.db.GetUser(user)
	if err != nil {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"username": u.Username,
		"role":     u.Role,
	})
}

// --- Workflow Handlers ---

func (s *Server) handleListWorkflows(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)
	dir := filepath.Join(s.config.HomeDir, user, "workflows")

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]WorkflowListItem{})
		return
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		http.Error(w, "Failed to read workflows", http.StatusInternalServerError)
		return
	}

	items := []WorkflowListItem{}
	for _, f := range files {
		if !f.IsDir() && (strings.HasSuffix(f.Name(), ".yaml") || strings.HasSuffix(f.Name(), ".yml")) {
			info, _ := f.Info()
			id := strings.TrimSuffix(f.Name(), filepath.Ext(f.Name()))

			// Try to read sidecar metadata from {id}.json
			metaPath := filepath.Join(dir, id+".json")
			var meta struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				Description string `json:"description"`
				Icon        string `json:"icon"`
			}

			// Auto-migration: create metadata JSON if not exists
			if mData, err := os.ReadFile(metaPath); err == nil {
				_ = json.Unmarshal(mData, &meta)
			} else {
				// Generate display name from ID
				meta.ID = id
				meta.Name = idToDisplayName(id)
				meta.Description = ""
				meta.Icon = "FileText"

				// Save metadata JSON
				metaData, _ := json.MarshalIndent(meta, "", "  ")
				_ = os.WriteFile(metaPath, metaData, 0644)
			}

			// If metadata doesn't have name, generate it
			if meta.Name == "" {
				meta.Name = idToDisplayName(id)
			}

			items = append(items, WorkflowListItem{
				ID:          id,
				Name:        meta.Name,
				Description: meta.Description,
				Icon:        meta.Icon,
				UpdatedAt:   info.ModTime(),
			})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

func (s *Server) handleGetWorkflow(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("name") // URL param is still called "name" for backward compatibility

	dir := filepath.Join(s.config.HomeDir, user, "workflows")
	yamlPath := filepath.Join(dir, id+".yaml")
	content, err := os.ReadFile(yamlPath)
	if err != nil {
		http.Error(w, "Workflow not found", http.StatusNotFound)
		return
	}

	// Read metadata
	metaPath := filepath.Join(dir, id+".json")
	var meta struct {
		Name      string                        `json:"name"`
		Positions map[string]map[string]float64 `json:"positions,omitempty"`
	}
	if mData, err := os.ReadFile(metaPath); err == nil {
		_ = json.Unmarshal(mData, &meta)
	}

	// If no display name in metadata, generate from ID
	displayName := meta.Name
	if displayName == "" {
		displayName = idToDisplayName(id)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":        id,
		"name":      displayName,
		"content":   string(content),
		"positions": meta.Positions,
	})
}

func (s *Server) handleSaveWorkflow(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)
	var req SaveWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// Use custom ID if provided, otherwise generate from Name
	var id string
	if req.ID != "" {
		// Validate and sanitize custom ID
		id = generateWorkflowID(req.ID) // Sanitize the custom ID
		if id == "" {
			http.Error(w, "Workflow ID cannot be empty", http.StatusBadRequest)
			return
		}
	} else {
		id = generateWorkflowID(req.Name)
		if id == "" {
			http.Error(w, "Workflow name cannot be empty", http.StatusBadRequest)
			return
		}
	}

	dir := filepath.Join(s.config.HomeDir, user, "workflows")
	os.MkdirAll(dir, 0755)

	// If renaming (old_id provided and different from new id), delete old files
	if req.OldID != "" && req.OldID != id {
		oldId := generateWorkflowID(req.OldID)
		if oldId != "" && oldId != id {
			oldYamlPath := filepath.Join(dir, oldId+".yaml")
			oldMetaPath := filepath.Join(dir, oldId+".json")
			os.Remove(oldYamlPath)
			os.Remove(oldMetaPath)
		}
	}

	yamlPath := filepath.Join(dir, id+".yaml")
	metaPath := filepath.Join(dir, id+".json")

	// Check if this is an update (workflow exists) or a new creation
	isUpdate := false
	if _, err := os.Stat(yamlPath); err == nil {
		// Workflow exists - check if it's the same workflow being edited
		// Read existing metadata to compare
		var existingMeta struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if mData, err := os.ReadFile(metaPath); err == nil {
			json.Unmarshal(mData, &existingMeta)
			// If the ID matches, this is an update
			if existingMeta.ID == id {
				isUpdate = true
			} else {
				// Different workflow with same generated ID - conflict
				http.Error(w, fmt.Sprintf("Workflow with ID '%s' already exists. Please choose a different name.", id), http.StatusConflict)
				return
			}
		} else {
			// File exists but no metadata - likely an update of migrated workflow
			isUpdate = true
		}
	}

	// Detect format
	format := "yaml"
	if strings.HasPrefix(strings.TrimSpace(req.Content), "{") {
		format = "json"
	}

	wf, err := parser.ParseWorkflowFromBytes([]byte(req.Content), format)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid workflow content (%s): %v", format, err), http.StatusBadRequest)
		return
	}

	if err := engine.ValidateAll(wf); err != nil {
		http.Error(w, fmt.Sprintf("Workflow validation failed: %v", err), http.StatusBadRequest)
		return
	}

	// Save YAML file
	if err := os.WriteFile(yamlPath, []byte(req.Content), 0644); err != nil {
		http.Error(w, "Failed to save workflow file", http.StatusInternalServerError)
		return
	}

	// Save Sidecar Metadata with ID and Name
	metadata := map[string]interface{}{
		"id":          id,
		"name":        req.Name,
		"description": req.Metadata.Description,
		"icon":        req.Metadata.Icon,
		"updated_at":  time.Now().Format(time.RFC3339),
	}

	// Save node positions if provided
	if len(req.Metadata.Positions) > 0 {
		metadata["positions"] = req.Metadata.Positions
	}

	// Preserve created_at if this is an update
	if isUpdate {
		var existingMeta map[string]interface{}
		if mData, err := os.ReadFile(metaPath); err == nil {
			json.Unmarshal(mData, &existingMeta)
			if createdAt, ok := existingMeta["created_at"]; ok {
				metadata["created_at"] = createdAt
			} else {
				metadata["created_at"] = time.Now().Format(time.RFC3339)
			}
		} else {
			metadata["created_at"] = time.Now().Format(time.RFC3339)
		}
	} else {
		// New workflow
		metadata["created_at"] = time.Now().Format(time.RFC3339)
	}

	metaData, _ := json.MarshalIndent(metadata, "", "  ")
	_ = os.WriteFile(metaPath, metaData, 0644)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Workflow saved successfully",
		"id":      id,
		"name":    req.Name,
	})
}

func (s *Server) handleValidateWorkflow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	wf, err := parser.ParseWorkflowFromBytes([]byte(req.Content), "yaml")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("Invalid YAML: %v", err)})
		return
	}

	if err := engine.ValidateAll(wf); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("Validation failed: %v", err)})
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "Valid")
}

func (s *Server) handleDeleteWorkflow(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)
	name := r.PathValue("name")
	path := filepath.Join(s.config.HomeDir, user, "workflows", name+".yaml")
	if err := os.Remove(path); err != nil {
		http.Error(w, "Failed to delete workflow", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Execution Handlers ---

func (s *Server) handleRunWorkflow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var user string
	if u, ok := r.Context().Value(UserContextKey).(string); ok {
		user = u
	} else {
		user = "admin"
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var wf *models.Workflow
	var initialInputs map[string]interface{}

	var runReq RunRequest
	if err := json.Unmarshal(body, &runReq); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	initialInputs = runReq.Inputs

	fmt.Printf("[DEBUG] RunRequest: ID='%s', ContentLen=%d\n", runReq.WorkflowID, len(runReq.Content))

	// 兼容性逻辑：将旧的 Workflow 字段映射到新的 WorkflowID 或 Content
	if runReq.Workflow != "" && runReq.WorkflowID == "" && runReq.Content == "" {
		// 如果看起来像 YAML/JSON 内容，则是 Content
		if strings.HasPrefix(runReq.Workflow, "name:") || strings.HasPrefix(runReq.Workflow, "id:") || strings.HasPrefix(runReq.Workflow, "{") {
			runReq.Content = runReq.Workflow
		} else {
			// 否则假定为 ID
			runReq.WorkflowID = runReq.Workflow
		}
	}

	if runReq.WorkflowID != "" {
		// Case 1: 按 ID 运行已保存的工作流
		userWorkflowDir := filepath.Join(s.config.HomeDir, user, "workflows")
		wf, err = parser.ResolveWorkflow(runReq.WorkflowID, userWorkflowDir)
		if err != nil {
			// 尝试从系统目录加载 (Fallback)
			wfSys, errSys := parser.ResolveWorkflow(runReq.WorkflowID, "workflows")
			if errSys == nil {
				wf = wfSys
				err = nil
			}
		}
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to resolve workflow ID '%s': %v", runReq.WorkflowID, err), http.StatusBadRequest)
			return
		}
		// 确保 ID 一致
		if wf.ID == "" {
			wf.ID = runReq.WorkflowID
		}

	} else if runReq.Content != "" {
		// Case 2: 运行临时内容 (Test Run)
		format := "yaml"
		if strings.HasPrefix(strings.TrimSpace(runReq.Content), "{") {
			format = "json"
		}
		wf, err = parser.ParseWorkflowFromBytes([]byte(runReq.Content), format)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to parse workflow content: %v", err), http.StatusBadRequest)
			return
		}
		// 临时任务如果没有 ID，生成一个临时的
		if wf.ID == "" {
			wf.ID = models.NormalizeID(wf.Name) + "_ephemeral"
		}

	} else if len(runReq.Inputs) == 0 { // Allow inputs-only run if we supported that context, but here we need a workflow
		http.Error(w, "Request must provide workflow_id or content", http.StatusBadRequest)
		return
	}

	if wf == nil {
		http.Error(w, "Failed to load workflow definition", http.StatusInternalServerError)
		return
	}

	fmt.Printf("[DEBUG] Loaded Workflow '%s' (ID: %s) with %d nodes\n", wf.Name, wf.ID, len(wf.Nodes))

	if err := engine.ValidateAll(wf); err != nil {
		http.Error(w, fmt.Sprintf("Workflow validation failed: %v", err), http.StatusBadRequest)
		return
	}

	// CLEANUP: Cancel any existing running instances of this workflow
	// This prevents multiple "zombies" and ensures the UI always connects to the latest run.
	if err := s.db.CancelRunningExecutions(user, wf.ID); err != nil {
		fmt.Printf("⚠️ Failed to cancel old executions: %v\n", err)
	}

	uuidStr := uuid.New().String()[:4]
	execID := time.Now().Format("102150405") + "-" + uuidStr

	ctx := models.NewExecutionContext(execID, user, s.config.HomeDir)
	ctx.SetWorkflowName(wf.Name)
	ctx.WorkflowID = wf.ID
	ctx.DB = s.db

	job := &WorkflowJob{
		ExecutionID:   execID,
		Workflow:      wf,
		Context:       ctx,
		InitialInputs: initialInputs,
		DB:            s.db,
	}

	if err := s.workerPool.Submit(job); err != nil {
		http.Error(w, fmt.Sprintf("Failed to submit job to worker pool: %v", err), http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(RunResponse{
		ExecutionID: execID,
		Status:      "queued",
		Message:     "Workflow execution queued successfully",
	})
}

func (s *Server) handleListExecutions(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	workflowID := r.URL.Query().Get("workflow_id")

	var records []*storage.ExecutionRecord
	var err error

	if workflowID != "" {
		records, err = s.db.ListExecutionsByWorkflow(user, workflowID, limit)
	} else {
		records, err = s.db.ListExecutions(user, limit, offset)
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(records)
}

func (s *Server) handleGetExecution(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Execution ID is required", http.StatusBadRequest)
		return
	}

	if ctx, ok := s.registry.Get(id); ok {
		// 使用脱敏后的快照，确保 secrets 不会泄露到前端
		results, stats := ctx.MaskedSnapshot()
		resp := models.ExecutionResult{
			ExecutionID:  ctx.ExecutionID,
			WorkflowName: ctx.WorkflowName,
			Status:       "RUNNING",
			Outputs:      results,
			Stats:        stats,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	record, err := s.db.GetExecution(id)
	if err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(record.ResultJSON))
		return
	}
	http.Error(w, "Execution not found", http.StatusNotFound)
}

func (s *Server) handleGetExecutionLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Execution ID is required", http.StatusBadRequest)
		return
	}
	logs, err := s.db.GetLogs(id)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to fetch logs: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(logs)
}

func (s *Server) handleApproveExecution(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	nodeID := r.PathValue("node_id")

	var req ApproveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req.Action = "approve"
	}
	if req.Action == "" {
		req.Action = "approve"
	}

	if ctx, ok := s.registry.Get(id); ok {
		ctx.ApproveNode(nodeID, req.Action)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "success",
			"node_id": nodeID,
			"action":  req.Action,
		})
		return
	}

	http.Error(w, "Execution not found or not running", http.StatusNotFound)
}

func (s *Server) handleCancelExecution(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if ctx, ok := s.registry.Get(id); ok {
		ctx.Cancel() // 触发 Context 取消信号
		s.db.UpdateStatus(id, "CANCELLED")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "Cancelled")
		return
	}

	http.Error(w, "Execution not found or not running", http.StatusNotFound)
}

// --- Artifact Handlers ---

func (s *Server) handleListArtifacts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	artifacts, err := s.db.ListExecutionArtifacts(id)
	if err != nil {
		http.Error(w, "Failed to list artifacts", http.StatusInternalServerError)
		return
	}
	if artifacts == nil {
		artifacts = make([]*models.ArtifactRecord, 0)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(artifacts)
}

func (s *Server) handleDownloadArtifact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	filename := r.PathValue("filename")
	user := r.Context().Value(UserContextKey).(string)

	record, err := s.db.GetExecution(id)
	if err != nil {
		http.Error(w, "Execution not found", http.StatusNotFound)
		return
	}

	// Try new path with execution_id subdirectory first, fallback to legacy path
	basePath := filepath.Join(s.config.HomeDir, user, "artifacts", models.NormalizeID(record.WorkflowName))
	filePath := filepath.Join(basePath, id, filepath.Base(filename))
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		// Fallback: legacy path without execution_id
		filePath = filepath.Join(basePath, filepath.Base(filename))
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			http.Error(w, "Artifact not found", http.StatusNotFound)
			return
		}
	}

	// Detect Content-Type
	file, err := os.Open(filePath)
	if err == nil {
		buffer := make([]byte, 512)
		_, _ = file.Read(buffer)
		file.Close()
		contentType := http.DetectContentType(buffer)
		w.Header().Set("Content-Type", contentType)
	}

	// Support ?mode=preview for inline display
	mode := r.URL.Query().Get("mode")
	if mode == "preview" {
		w.Header().Set("Content-Disposition", "inline; filename=\""+filepath.Base(filename)+"\"")
	} else {
		w.Header().Set("Content-Disposition", "attachment; filename=\""+filepath.Base(filename)+"\"")
	}
	http.ServeFile(w, r, filePath)
}

// handlePreviewFileGlobal serves a file from the global file library inline (for thumbnails)
func (s *Server) handlePreviewFileGlobal(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	files, err := s.db.ListUserFiles(user)
	if err != nil {
		http.Error(w, "Failed to list files", http.StatusInternalServerError)
		return
	}

	var target *models.UserFileRecord
	for _, f := range files {
		if f.FileID == id || f.UUID == id {
			target = f
			break
		}
	}

	if target == nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	filePath := filepath.Join(s.config.HomeDir, user, "storage", "files", target.UUID)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		http.Error(w, "File not found on disk", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", target.MimeType)
	w.Header().Set("Content-Disposition", "inline; filename=\""+target.OriginalFilename+"\"")
	http.ServeFile(w, r, filePath)
}

// --- Global File Library Handlers ---

func (s *Server) handleUploadFileGlobal(w http.ResponseWriter, r *http.Request) {
	// Parse max 100MB
	r.ParseMultipartForm(100 << 20)

	user := r.Context().Value(UserContextKey).(string)
	fileID := r.FormValue("file_id")

	if fileID == "" {
		http.Error(w, "file_id is required", http.StatusBadRequest)
		return
	}

	// Check 1GB Quota
	currentSize, err := s.db.GetUserTotalFileSize(user)
	if err != nil {
		http.Error(w, "Failed to check quota", http.StatusInternalServerError)
		return
	}
	if currentSize >= 1*1024*1024*1024 { // 1GB
		http.Error(w, "Storage quota exceeded (1GB limit)", http.StatusForbidden)
		return
	}

	// Check file_id uniqueness
	exists, err := s.db.CheckFileIDExists(user, fileID)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	if exists {
		http.Error(w, fmt.Sprintf("File ID '%s' already exists", fileID), http.StatusConflict)
		return
	}

	file, handler, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Failed to get file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	if handler.Size > 100*1024*1024 {
		http.Error(w, "File too large (max 100MB)", http.StatusRequestEntityTooLarge)
		return
	}

	// Detect MIME type
	buff := make([]byte, 512)
	if _, err := file.Read(buff); err != nil {
		http.Error(w, "Failed to read file", http.StatusInternalServerError)
		return
	}
	mimeType := http.DetectContentType(buff)
	file.Seek(0, 0) // Reset pointer

	// Allowlist check can be added here
	// For now we allow most, maybe block executables if needed

	uuidStr := uuid.New().String()
	storageDir := filepath.Join(s.config.HomeDir, user, "storage", "files")
	os.MkdirAll(storageDir, 0755)

	destPath := filepath.Join(storageDir, uuidStr)
	dest, err := os.Create(destPath)
	if err != nil {
		http.Error(w, "Failed to store file", http.StatusInternalServerError)
		return
	}
	defer dest.Close()

	if _, err := io.Copy(dest, file); err != nil {
		http.Error(w, "Failed to write file", http.StatusInternalServerError)
		return
	}

	// Save metadata to DB
	err = s.db.SaveUserFile(
		uuidStr,
		fileID,
		user,
		handler.Filename,
		mimeType,
		handler.Size,
		"", // hash optional for now
	)
	if err != nil {
		// Clean up file if DB fails
		os.Remove(destPath)
		http.Error(w, "Failed to save metadata", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"uuid":              uuidStr,
		"file_id":           fileID,
		"original_filename": handler.Filename,
	})
}

func (s *Server) handleListFilesGlobal(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)
	files, err := s.db.ListUserFiles(user)
	if err != nil {
		http.Error(w, "Failed to list files", http.StatusInternalServerError)
		return
	}
	if files == nil {
		files = make([]*models.UserFileRecord, 0)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(files)
}

func (s *Server) handleDeleteFileGlobal(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	// Get file to find UUID (for physical deletion)
	// We iterate list to support deleting by either FileID or UUID
	files, _ := s.db.ListUserFiles(user)
	var target *models.UserFileRecord
	for _, f := range files {
		if f.FileID == id || f.UUID == id {
			target = f
			break
		}
	}

	if target == nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	// Delete from DB
	if err := s.db.DeleteUserFile(user, id); err != nil {
		http.Error(w, "Failed to delete metadata", http.StatusInternalServerError)
		return
	}

	// Delete physical file
	path := filepath.Join(s.config.HomeDir, user, "storage", "files", target.UUID)
	os.Remove(path) // Ignore error if already gone

	w.WriteHeader(http.StatusNoContent)
}

// --- Secret Handlers ---

func (s *Server) handleCreateSecret(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)
	var req CreateSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	val, err := crypto.Encrypt(req.Value)
	if err != nil {
		http.Error(w, "Encryption failed", http.StatusInternalServerError)
		return
	}

	if err := s.db.SaveSecret(uuid.New().String(), user, req.Name, val); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handleGetSecret(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)
	name := r.PathValue("name")
	record, err := s.db.GetSecret(user, name)
	if err != nil {
		http.Error(w, "Secret not found", http.StatusNotFound)
		return
	}
	val, _ := crypto.Decrypt(record.EncryptedValue)
	json.NewEncoder(w).Encode(SecretResponse{Name: record.Name, Value: val})
}

func (s *Server) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)
	records, _ := s.db.ListSecrets(user)
	resp := []SecretResponse{}
	for _, r := range records {
		resp = append(resp, SecretResponse{Name: r.Name, CreatedAt: r.CreatedAt.String})
	}
	json.NewEncoder(w).Encode(SecretListResponse{Secrets: resp})
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)
	name := r.PathValue("name")
	s.db.DeleteSecret(user, name)
	w.WriteHeader(http.StatusNoContent)
}

// --- Workflow File Link Handler (Symlink-based) ---

type CreateWorkflowFileLinkRequest struct {
	NodeID     string `json:"node_id"`
	SourcePath string `json:"source_path"`
}

type CreateWorkflowFileLinkResponse struct {
	RelativePath string `json:"relative_path"`
	Filename     string `json:"filename"`
}

// handleCreateWorkflowFileLink creates a symlink in the workflow's files directory
// POST /api/v1/workflows/{id}/files
func (s *Server) handleCreateWorkflowFileLink(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)
	workflowID := r.PathValue("id")

	var req CreateWorkflowFileLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.NodeID == "" {
		http.Error(w, "node_id is required", http.StatusBadRequest)
		return
	}
	if req.SourcePath == "" {
		http.Error(w, "source_path is required", http.StatusBadRequest)
		return
	}

	// Validate source file exists
	info, err := os.Stat(req.SourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, fmt.Sprintf("Source file does not exist: %s", req.SourcePath), http.StatusBadRequest)
			return
		}
		http.Error(w, fmt.Sprintf("Failed to access source file: %v", err), http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		http.Error(w, "Source path is a directory, not a file", http.StatusBadRequest)
		return
	}

	// Create files directory
	filesDir := filepath.Join(s.config.HomeDir, user, "workflows", workflowID, "files")
	if err := os.MkdirAll(filesDir, 0755); err != nil {
		http.Error(w, fmt.Sprintf("Failed to create files directory: %v", err), http.StatusInternalServerError)
		return
	}

	// Symlink naming: {node_id}_{filename}
	originalFilename := filepath.Base(req.SourcePath)
	symlinkName := fmt.Sprintf("%s_%s", req.NodeID, originalFilename)
	symlinkPath := filepath.Join(filesDir, symlinkName)

	// Remove existing symlink if any
	if _, err := os.Lstat(symlinkPath); err == nil {
		os.Remove(symlinkPath)
	}

	// Create symlink
	if err := os.Symlink(req.SourcePath, symlinkPath); err != nil {
		http.Error(w, fmt.Sprintf("Failed to create symlink: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(CreateWorkflowFileLinkResponse{
		RelativePath: symlinkName,
		Filename:     originalFilename,
	})
}

// WorkflowFileUploadResponse is the response for workflow file upload
type WorkflowFileUploadResponse struct {
	FileID    string `json:"file_id"`
	Filename  string `json:"filename"`
	MimeType  string `json:"mime_type"`
	SizeBytes int64  `json:"size_bytes"`
	CreatedAt string `json:"created_at"`
	// Legacy fields for backward compatibility
	RelativePath string `json:"relative_path,omitempty"`
}

// handleUploadWorkflowFile uploads a file to the unified storage and registers it in the database
// POST /api/v1/workflows/{id}/files/upload
func (s *Server) handleUploadWorkflowFile(w http.ResponseWriter, r *http.Request) {
	// Parse max 100MB
	r.ParseMultipartForm(100 << 20)

	user := r.Context().Value(UserContextKey).(string)
	workflowID := r.PathValue("id")
	nodeID := r.FormValue("node_id")

	if nodeID == "" {
		http.Error(w, "node_id is required", http.StatusBadRequest)
		return
	}

	file, handler, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Failed to get file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	if handler.Size > 100*1024*1024 {
		http.Error(w, "File too large (max 100MB)", http.StatusRequestEntityTooLarge)
		return
	}

	// Generate file ID and stored filename
	// Format: {nodeId}_{timestamp}_{originalFilename}
	// Example: input_1738825200_data.csv
	timestamp := time.Now().Unix()
	storedFilename := fmt.Sprintf("%s_%d_%s", nodeID, timestamp, handler.Filename)
	fileID := storedFilename // File ID = stored filename for simplicity and readability

	// Create storage directory: $HOME/{user}/storage/files/
	storageDir := filepath.Join(s.config.HomeDir, user, "storage", "files")
	if err := os.MkdirAll(storageDir, 0755); err != nil {
		http.Error(w, fmt.Sprintf("Failed to create storage directory: %v", err), http.StatusInternalServerError)
		return
	}

	destPath := filepath.Join(storageDir, storedFilename)

	// Create destination file
	dest, err := os.Create(destPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create file: %v", err), http.StatusInternalServerError)
		return
	}
	defer dest.Close()

	// Copy content
	if _, err := io.Copy(dest, file); err != nil {
		os.Remove(destPath) // Cleanup on failure
		http.Error(w, fmt.Sprintf("Failed to write file: %v", err), http.StatusInternalServerError)
		return
	}

	// Detect MIME type from extension
	mimeType := handler.Header.Get("Content-Type")
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = detectMimeType(handler.Filename)
	}

	// Save to database
	// Note: storedFilename is used as UUID since it's unique (nodeId_timestamp_filename)
	if err := s.db.SaveWorkflowFile(storedFilename, fileID, user, handler.Filename, mimeType, handler.Size, "", workflowID, nodeID); err != nil {
		os.Remove(destPath) // Cleanup on failure
		http.Error(w, fmt.Sprintf("Failed to save file record: %v", err), http.StatusInternalServerError)
		return
	}

	// Also create a symlink in workflow directory for backward compatibility
	// This ensures old workflows using file_path still work
	legacyDir := filepath.Join(s.config.HomeDir, user, "workflows", workflowID, "files")
	os.MkdirAll(legacyDir, 0755)
	legacyFilename := fmt.Sprintf("%s_%s", nodeID, handler.Filename)
	legacyPath := filepath.Join(legacyDir, legacyFilename)
	os.Remove(legacyPath) // Remove old symlink if exists
	os.Symlink(destPath, legacyPath)

	createdAt := time.Now().Format(time.RFC3339)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(WorkflowFileUploadResponse{
		FileID:       fileID,
		Filename:     handler.Filename,
		MimeType:     mimeType,
		SizeBytes:    handler.Size,
		CreatedAt:    createdAt,
		RelativePath: legacyFilename, // For backward compatibility
	})
}

// detectMimeType returns MIME type based on file extension
func detectMimeType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	mimeTypes := map[string]string{
		".txt":  "text/plain",
		".md":   "text/markdown",
		".json": "application/json",
		".csv":  "text/csv",
		".xml":  "application/xml",
		".html": "text/html",
		".css":  "text/css",
		".js":   "application/javascript",
		".ts":   "application/typescript",
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".png":  "image/png",
		".gif":  "image/gif",
		".webp": "image/webp",
		".svg":  "image/svg+xml",
		".pdf":  "application/pdf",
		".zip":  "application/zip",
		".yaml": "application/yaml",
		".yml":  "application/yaml",
	}
	if mime, ok := mimeTypes[ext]; ok {
		return mime
	}
	return "application/octet-stream"
}

// handleDeleteWorkflowFileLink removes a symlink from the workflow's files directory
// DELETE /api/v1/workflows/{id}/files/{filename}
func (s *Server) handleDeleteWorkflowFileLink(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)
	workflowID := r.PathValue("id")
	filename := r.PathValue("filename")

	symlinkPath := filepath.Join(s.config.HomeDir, user, "workflows", workflowID, "files", filepath.Base(filename))

	if err := os.Remove(symlinkPath); err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "File link not found", http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("Failed to delete file link: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Workflow Schema Handler ---

type WorkflowSchemaResponse struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Icon        string                 `json:"icon"`
	Data        map[string]interface{} `json:"data"`
	Secrets     map[string]string      `json:"secrets"`
}

// handleGetWorkflowSchema 返回指定 workflow 的 schema (data 和 secrets 字段)
// 用于前端动态生成表单
func (s *Server) handleGetWorkflowSchema(w http.ResponseWriter, r *http.Request) {
	// 从 URL 路径获取 workflow ID
	// 格式: /api/v1/workflows/:id/schema
	// 例如: /api/v1/workflows/tofi/ai_response/schema
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Workflow ID is required", http.StatusBadRequest)
		return
	}

	// 使用 parser 解析 workflow
	// 这里需要注意：如果是用户的 workflow，我们需要知道是哪个用户。
	// 目前这个 API 主要是给 Run 用，假设是当前用户？或者 Public？
	// 暂时假设是当前用户
	user := r.Context().Value(UserContextKey).(string)
	userWorkflowDir := filepath.Join(s.config.HomeDir, user, "workflows")

	wf, err := parser.ResolveWorkflow(id, userWorkflowDir)
	if err != nil {
		// Fallback to system workflows
		wfSys, errSys := parser.ResolveWorkflow(id, "workflows")
		if errSys == nil {
			wf = wfSys
			err = nil
		}
	}

	if err != nil {
		http.Error(w, fmt.Sprintf("Workflow not found: %v", err), http.StatusNotFound)
		return
	}

	// 构造响应
	resp := WorkflowSchemaResponse{
		Name:        wf.Name,
		Description: wf.Description,
		Icon:        wf.Icon,
		Data:        wf.Data,
		Secrets:     wf.Secrets,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
