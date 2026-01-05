package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"tofi-core/internal/crypto"
	"tofi-core/internal/engine"
	"tofi-core/internal/models"
	"tofi-core/internal/parser"

	"github.com/google/uuid"
)

type RunRequest struct {
	Workflow string                 `json:"workflow"` // 可以是 YAML 内容，也可以是 ID
	Inputs   map[string]interface{} `json:"inputs"`   // 初始参数
}

type RunResponse struct {
	ExecutionID string `json:"execution_id"`
	Status      string `json:"status"`
	Message     string `json:"message"`
}

func (s *Server) handleGetExecution(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Execution ID is required", http.StatusBadRequest)
		return
	}

	// 1. 优先尝试从内存中获取 (Active)
	if ctx, ok := s.registry.Get(id); ok {
		results, stats := ctx.Snapshot()
		resp := models.ExecutionResult{
			ExecutionID:  ctx.ExecutionID,
			WorkflowName: ctx.WorkflowName,
			Status:       "RUNNING",
			StartTime:    time.Now(),
			Duration:     "Running...",
			Stats:        stats,
			Outputs:      results,
		}
		if len(stats) > 0 {
			resp.StartTime = stats[0].StartTime
			resp.Duration = time.Since(stats[0].StartTime).String()
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	// 2. 尝试从数据库中获取 (Completed)
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

func (s *Server) handleListArtifacts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Execution ID is required", http.StatusBadRequest)
		return
	}

	user := "anonymous"
	if u, ok := r.Context().Value(UserContextKey).(string); ok {
		user = u
	}

	artDir := filepath.Join(s.config.HomeDir, user, "artifacts", id)
	if _, err := os.Stat(artDir); os.IsNotExist(err) {
		json.NewEncoder(w).Encode([]string{})
		return
	}

	files, err := os.ReadDir(artDir)
	if err != nil {
		http.Error(w, "Failed to read artifacts", http.StatusInternalServerError)
		return
	}

	var artifactNames []string
	for _, f := range files {
		if !f.IsDir() {
			artifactNames = append(artifactNames, f.Name())
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(artifactNames)
}

func (s *Server) handleDownloadArtifact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	filename := r.PathValue("filename")
	if id == "" || filename == "" {
		http.Error(w, "Execution ID and filename are required", http.StatusBadRequest)
		return
	}

	user := "anonymous"
	if u, ok := r.Context().Value(UserContextKey).(string); ok {
		user = u
	}

	safeFilename := filepath.Base(filename)
	filePath := filepath.Join(s.config.HomeDir, user, "artifacts", id, safeFilename)

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		http.Error(w, "Artifact not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", safeFilename))
	http.ServeFile(w, r, filePath)
}

func (s *Server) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Execution ID is required", http.StatusBadRequest)
		return
	}

	var user string
	if u, ok := r.Context().Value(UserContextKey).(string); ok {
		user = u
	} else {
		user = "anonymous"
	}

	r.ParseMultipartForm(32 << 20)
	file, handler, err := r.FormFile("file")
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get file: %v", err), http.StatusBadRequest)
		return
	}
	defer file.Close()

	uploadDir := filepath.Join(s.config.HomeDir, user, "uploads", id)
	os.MkdirAll(uploadDir, 0755)

	destPath := filepath.Join(uploadDir, filepath.Base(handler.Filename))
	dest, err := os.Create(destPath)
	if err != nil {
		http.Error(w, "Failed to create destination file", http.StatusInternalServerError)
		return
	}
	defer dest.Close()

	if _, err := io.Copy(dest, file); err != nil {
		http.Error(w, "Failed to save file", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Successfully uploaded %s", handler.Filename)
}

func (s *Server) handleRunWorkflow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 0. 获取当前用户
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
			wf, err = parser.ParseWorkflowFromBytes([]byte(runReq.Workflow), "yaml")
		} else if runReq.Workflow != "" {
			// 1. 尝试从用户私有目录加载
			userWorkflowDir := filepath.Join(s.config.HomeDir, user, "workflows")
			wf, err = parser.ResolveWorkflow(runReq.Workflow, userWorkflowDir)
			
			// 2. 如果失败，尝试从系统公共目录加载
			if err != nil {
				// Fallback to system workflows
				wfSys, errSys := parser.ResolveWorkflow(runReq.Workflow, "workflows")
				if errSys == nil {
					wf = wfSys
					err = nil
				}
				// 如果系统也没找到，保留 err 为用户的错误（或者更具体的错误）
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

	// [REMOVED] Per-execution log file is no longer needed as logs go to DB and system rotating log.

	// 提交到工作池
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

	resp := RunResponse{
		ExecutionID: execID,
		Status:      "queued",
		Message:     "Workflow execution queued successfully",
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(resp)
}

// Secret 管理相关的请求/响应结构

type CreateSecretRequest struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type SecretResponse struct {
	Name      string `json:"name"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	Value     string `json:"value,omitempty"` // 仅在 Get 时返回
}

type SecretListResponse struct {
	Secrets []SecretResponse `json:"secrets"`
}

// handleCreateSecret 创建或更新一个 Secret
func (s *Server) handleCreateSecret(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 获取用户
	user := "anonymous"
	if u, ok := r.Context().Value(UserContextKey).(string); ok {
		user = u
	}

	// 解析请求
	var req CreateSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Failed to parse request: %v", err), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// 验证参数
	if req.Name == "" || req.Value == "" {
		http.Error(w, "Name and value are required", http.StatusBadRequest)
		return
	}

	// 加密 Secret
	encryptedValue, err := crypto.Encrypt(req.Value)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to encrypt secret: %v", err), http.StatusInternalServerError)
		return
	}

	// 生成 ID
	id := uuid.New().String()

	// 保存到数据库
	if err := s.db.SaveSecret(id, user, req.Name, encryptedValue); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save secret: %v", err), http.StatusInternalServerError)
		return
	}

	// 返回成功响应
	resp := SecretResponse{
		Name: req.Name,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

// handleGetSecret 获取指定的 Secret（解密后）
func (s *Server) handleGetSecret(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "Secret name is required", http.StatusBadRequest)
		return
	}

	// 获取用户
	user := "anonymous"
	if u, ok := r.Context().Value(UserContextKey).(string); ok {
		user = u
	}

	// 从数据库获取
	record, err := s.db.GetSecret(user, name)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "Secret not found", http.StatusNotFound)
		} else {
			http.Error(w, fmt.Sprintf("Failed to get secret: %v", err), http.StatusInternalServerError)
		}
		return
	}

	// 解密
	decryptedValue, err := crypto.Decrypt(record.EncryptedValue)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to decrypt secret: %v", err), http.StatusInternalServerError)
		return
	}

	// 返回响应
	resp := SecretResponse{
		Name:      record.Name,
		Value:     decryptedValue,
		CreatedAt: record.CreatedAt.String,
		UpdatedAt: record.UpdatedAt.String,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleListSecrets 列出用户的所有 Secrets（不包含值）
func (s *Server) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	// 获取用户
	user := "anonymous"
	if u, ok := r.Context().Value(UserContextKey).(string); ok {
		user = u
	}

	// 从数据库获取列表
	records, err := s.db.ListSecrets(user)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to list secrets: %v", err), http.StatusInternalServerError)
		return
	}

	// 构建响应
	secrets := make([]SecretResponse, 0, len(records))
	for _, record := range records {
		secrets = append(secrets, SecretResponse{
			Name:      record.Name,
			CreatedAt: record.CreatedAt.String,
			UpdatedAt: record.UpdatedAt.String,
		})
	}

	resp := SecretListResponse{
		Secrets: secrets,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleDeleteSecret 删除指定的 Secret
func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "Secret name is required", http.StatusBadRequest)
		return
	}

	// 获取用户
	user := "anonymous"
	if u, ok := r.Context().Value(UserContextKey).(string); ok {
		user = u
	}

	// 从数据库删除
	if err := s.db.DeleteSecret(user, name); err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "Secret not found", http.StatusNotFound)
		} else {
			http.Error(w, fmt.Sprintf("Failed to delete secret: %v", err), http.StatusInternalServerError)
		}
		return
	}

	// 返回成功响应
	w.WriteHeader(http.StatusNoContent)
}
