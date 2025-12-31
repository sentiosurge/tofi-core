package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
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

	// 3. 尝试从状态文件中获取
	statePath := filepath.Join(s.config.HomeDir, "states", fmt.Sprintf("state-%s.json", id))
	if data, err := os.ReadFile(statePath); err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
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

	logPath := filepath.Join(s.config.HomeDir, "logs", id+".log")
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		http.Error(w, "Log file not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	http.ServeFile(w, r, logPath)
}

func (s *Server) handleListArtifacts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Execution ID is required", http.StatusBadRequest)
		return
	}

	artDir := filepath.Join(s.config.HomeDir, "artifacts", id)
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

	safeFilename := filepath.Base(filename)
	filePath := filepath.Join(s.config.HomeDir, "artifacts", id, safeFilename)

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		http.Error(w, "Artifact not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", safeFilename))
	http.ServeFile(w, r, filePath)
}

func (s *Server) handleRunWorkflow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var wf *models.Workflow
	var initialInputs map[string]interface{}

	// 尝试作为 JSON 解析 (RunRequest 结构)
	var runReq RunRequest
	if err := json.Unmarshal(body, &runReq); err == nil && (runReq.Workflow != "" || len(runReq.Inputs) > 0) {
		if strings.HasPrefix(runReq.Workflow, "name:") || strings.HasPrefix(runReq.Workflow, "{") {
			wf, err = parser.ParseWorkflowFromBytes([]byte(runReq.Workflow), "yaml")
		} else if runReq.Workflow != "" {
			wf, err = parser.ResolveWorkflow(runReq.Workflow, "workflows")
		}
		initialInputs = runReq.Inputs
	} else {
		// 回退到原始 YAML Body 模式
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
	ctx := models.NewExecutionContext(execID, s.config.HomeDir)
	ctx.WorkflowName = wf.Name

	dirs := []string{ctx.Paths.Logs, ctx.Paths.Artifacts, ctx.Paths.Uploads, ctx.Paths.States}
	for _, d := range dirs {
		os.MkdirAll(d, 0755)
	}
	
	logFilePath := filepath.Join(ctx.Paths.Logs, execID+".log")
	if f, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
		ctx.SetLogger(f)
	}

	s.registry.Register(execID, ctx)

	go func() {
		defer s.registry.Unregister(execID)
		defer ctx.Close()

		defer func() {
			if r := recover(); r != nil {
				ctx.Log("PANIC RECOVERED: %v", r)
			}
		}()

		ctx.Log("🚀 Execution Started via API")
		engine.Start(wf, ctx, initialInputs)
		ctx.Wg.Wait()

		if err := engine.SaveReport(wf, ctx, s.db); err != nil {
			ctx.Log("Failed to save report to DB: %v", err)
		} else {
			ctx.Log("Execution record saved to database")
		}
		
		ctx.Log("🏁 Execution Finished")
	}()

	resp := RunResponse{
		ExecutionID: execID,
		Status:      "started",
		Message:     "Workflow execution initiated successfully",
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(resp)
}