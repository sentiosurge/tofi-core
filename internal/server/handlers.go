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

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// --- Request/Response Structs ---

type RunRequest struct {
	Workflow string                 `json:"workflow"` // YAML content or Workflow ID
	Inputs   map[string]interface{} `json:"inputs"`
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
	ID       string `json:"id,omitempty"`       // Optional custom ID, if empty will be generated from Name
	OldID    string `json:"old_id,omitempty"`   // If renaming, provide old ID to delete old files
	Name     string `json:"name"`
	Content  string `json:"content"`
	Metadata struct {
		Description string                       `json:"description"`
		Icon        string                       `json:"icon"`
		Positions   map[string]map[string]float64 `json:"positions,omitempty"` // Node positions: { nodeId: { x, y } }
	} `json:"metadata"`
}

type WorkflowListItem struct {
	ID          string    `json:"id"`          // Unique identifier (filename without extension)
	Name        string    `json:"name"`        // Display name
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

// --- Auth Handlers ---

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
		Name      string                          `json:"name"`
		Positions map[string]map[string]float64   `json:"positions,omitempty"`
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
		user = "cli-admin"
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
	if err := json.Unmarshal(body, &runReq); err == nil && (runReq.Workflow != "" || len(runReq.Inputs) > 0) {
		if strings.HasPrefix(runReq.Workflow, "name:") || strings.HasPrefix(runReq.Workflow, "{") {
			// Detect format
			format := "yaml"
			if strings.HasPrefix(strings.TrimSpace(runReq.Workflow), "{") {
				format = "json"
			}
			wf, err = parser.ParseWorkflowFromBytes([]byte(runReq.Workflow), format)
		} else if runReq.Workflow != "" {
			userWorkflowDir := filepath.Join(s.config.HomeDir, user, "workflows")
			wf, err = parser.ResolveWorkflow(runReq.Workflow, userWorkflowDir)
			if err != nil {
				wfSys, errSys := parser.ResolveWorkflow(runReq.Workflow, "workflows")
				if errSys == nil {
					wf = wfSys
					err = nil
				}
			}
			if err != nil {
				http.Error(w, fmt.Sprintf("Failed to resolve workflow '%s': %v", runReq.Workflow, err), http.StatusBadRequest)
				return
			}
		}
		initialInputs = runReq.Inputs
	} else {
		wf, err = parser.ParseWorkflowFromBytes(body, "yaml")
	}

	if err != nil || wf == nil {
		http.Error(w, fmt.Sprintf("Failed to parse workflow: %v", err), http.StatusBadRequest)
		return
	}

	if err := engine.ValidateAll(wf); err != nil {
		http.Error(w, fmt.Sprintf("Workflow validation failed: %v", err), http.StatusBadRequest)
		return
	}

	uuidStr := uuid.New().String()[:4]
	execID := time.Now().Format("102150405") + "-" + uuidStr
	
	ctx := models.NewExecutionContext(execID, user, s.config.HomeDir)
	ctx.SetWorkflowName(wf.Name)
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

	records, err := s.db.ListExecutions(user, limit, offset)
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

// --- Artifact Handlers ---

func (s *Server) handleListArtifacts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	user := r.Context().Value(UserContextKey).(string)
	
	// We need to know the workflow name to find the artifact dir
	record, err := s.db.GetExecution(id)
	if err != nil {
		http.Error(w, "Execution not found", http.StatusNotFound)
		return
	}

	artDir := filepath.Join(s.config.HomeDir, user, "artifacts", models.NormalizeID(record.WorkflowName))
	if _, err := os.Stat(artDir); os.IsNotExist(err) {
		json.NewEncoder(w).Encode([]string{})
		return
	}

	files, err := os.ReadDir(artDir)
	if err != nil {
		http.Error(w, "Failed to read artifacts", http.StatusInternalServerError)
		return
	}

	names := []string{}
	for _, f := range files {
		if !f.IsDir() {
			names = append(names, f.Name())
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(names)
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

	filePath := filepath.Join(s.config.HomeDir, user, "artifacts", models.NormalizeID(record.WorkflowName), filepath.Base(filename))
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		http.Error(w, "Artifact not found", http.StatusNotFound)
		return
	}
	http.ServeFile(w, r, filePath)
}

func (s *Server) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	user := r.Context().Value(UserContextKey).(string)

	r.ParseMultipartForm(32 << 20)
	file, handler, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Failed to get file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	uploadDir := filepath.Join(s.config.HomeDir, user, "uploads", id)
	os.MkdirAll(uploadDir, 0755)

	dest, err := os.Create(filepath.Join(uploadDir, filepath.Base(handler.Filename)))
	if err != nil {
		http.Error(w, "Failed to create file", http.StatusInternalServerError)
		return
	}
	defer dest.Close()
	io.Copy(dest, file)
	w.WriteHeader(http.StatusOK)
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
	workflowsDir := filepath.Join(s.config.HomeDir, "workflows")
	wf, err := parser.ResolveWorkflow(id, workflowsDir)
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