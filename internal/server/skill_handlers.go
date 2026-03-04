package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"tofi-core/internal/models"
	"tofi-core/internal/skills"
	"tofi-core/internal/storage"

	"github.com/google/uuid"
)

// --- Skill API Handlers ---

// handleListSkills GET /api/v1/skills — 列出用户安装的所有 Skills
func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")

	keyword := r.URL.Query().Get("q")

	var records []*storage.SkillRecord
	var err error

	if keyword != "" {
		records, err = s.db.SearchSkills(userID, keyword)
	} else {
		records, err = s.db.ListSkills(userID)
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if records == nil {
		records = []*storage.SkillRecord{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(records)
}

// handleGetSkill GET /api/v1/skills/{id} — 获取 Skill 详情
func (s *Server) handleGetSkill(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "skill id required", http.StatusBadRequest)
		return
	}

	skill, err := s.db.GetSkill(id)
	if err != nil {
		http.Error(w, "skill not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(skill)
}

// handleInstallSkill POST /api/v1/skills/install — 安装 Skill
// 支持两种来源:
//   - source: "local" + content (SKILL.md 内容)
//   - source: "git" + url (git repo URL)
func (s *Server) handleInstallSkill(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")

	var req struct {
		Source  string `json:"source"`  // "local" | "git"
		Content string `json:"content"` // SKILL.md 内容 (source=local)
		URL     string `json:"url"`     // git repo URL (source=git)
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	switch req.Source {
	case "local", "":
		// 直接从 SKILL.md 内容安装
		if req.Content == "" {
			http.Error(w, "content is required for local install", http.StatusBadRequest)
			return
		}
		s.installFromContent(w, userID, req.Content, "local", "")

	case "git":
		// 从 Git repo 安装
		if req.URL == "" {
			http.Error(w, "url is required for git install", http.StatusBadRequest)
			return
		}
		s.installFromGit(w, userID, req.URL)

	default:
		http.Error(w, fmt.Sprintf("unsupported source: %s", req.Source), http.StatusBadRequest)
	}
}

// installFromContent 从 SKILL.md 内容安装
func (s *Server) installFromContent(w http.ResponseWriter, userID, content, source, sourceURL string) {
	// 解析 SKILL.md
	skillFile, err := skills.Parse([]byte(content))
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid SKILL.md: %v", err), http.StatusBadRequest)
		return
	}

	// 构建数据库记录
	manifest := skillFile.Manifest
	manifestJSON, _ := json.Marshal(manifest)

	record := &storage.SkillRecord{
		ID:          fmt.Sprintf("%s/%s", userID, manifest.Name),
		Name:        manifest.Name,
		Description: manifest.Description,
		Version:     "1.0",
		Source:      source,
		SourceURL:   sourceURL,
		ManifestJSON: string(manifestJSON),
		Instructions: skillFile.Body,
		HasScripts:  len(skillFile.ScriptDirs) > 0,
		RequiredSecrets: toJSON(manifest.RequiredEnvVars()),
		AllowedTools:    toJSON(manifest.AllowedToolsList()),
		UserID:      userID,
		InstalledAt: time.Now().Format("2006-01-02 15:04:05"),
	}

	// 同时保存到本地文件系统
	localStore := skills.NewLocalStore(s.config.HomeDir)
	if err := localStore.SaveLocal(manifest.Name, content); err != nil {
		log.Printf("[skills] warning: failed to save to local store: %v", err)
	}

	// 保存到数据库
	if err := s.db.SaveSkill(record); err != nil {
		http.Error(w, fmt.Sprintf("failed to save skill: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(record)
}

// installFromGit 从 Git repo 安装 Skill
func (s *Server) installFromGit(w http.ResponseWriter, userID, gitURL string) {
	// 使用 LocalStore 管理本地目录
	localStore := skills.NewLocalStore(s.config.HomeDir)
	if err := localStore.EnsureDir(); err != nil {
		http.Error(w, fmt.Sprintf("failed to create skills directory: %v", err), http.StatusInternalServerError)
		return
	}

	// 从 URL 推断 skill 名称
	name := inferSkillName(gitURL)
	if name == "" {
		http.Error(w, "cannot infer skill name from URL", http.StatusBadRequest)
		return
	}

	// Git clone 到本地
	skillDir := localStore.SkillDir(name)
	cloner := skills.NewGitInstaller()
	if err := cloner.Clone(gitURL, skillDir); err != nil {
		http.Error(w, fmt.Sprintf("git clone failed: %v", err), http.StatusInternalServerError)
		return
	}

	// 解析 SKILL.md
	skillFile, err := skills.ParseDir(skillDir)
	if err != nil {
		// 清理失败的克隆
		localStore.Remove(name)
		http.Error(w, fmt.Sprintf("invalid skill: %v", err), http.StatusBadRequest)
		return
	}

	// 构建并保存数据库记录
	manifest := skillFile.Manifest
	manifestJSON, _ := json.Marshal(manifest)

	record := &storage.SkillRecord{
		ID:          fmt.Sprintf("%s/%s", userID, manifest.Name),
		Name:        manifest.Name,
		Description: manifest.Description,
		Version:     "1.0",
		Source:      "git",
		SourceURL:   gitURL,
		ManifestJSON: string(manifestJSON),
		Instructions: skillFile.Body,
		HasScripts:  len(skillFile.ScriptDirs) > 0,
		RequiredSecrets: toJSON(manifest.RequiredEnvVars()),
		AllowedTools:    toJSON(manifest.AllowedToolsList()),
		UserID:      userID,
		InstalledAt: time.Now().Format("2006-01-02 15:04:05"),
	}

	if err := s.db.SaveSkill(record); err != nil {
		http.Error(w, fmt.Sprintf("failed to save skill: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(record)
}

// handleDeleteSkill DELETE /api/v1/skills/{id} — 卸载 Skill
func (s *Server) handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "skill id required", http.StatusBadRequest)
		return
	}

	// 先获取 skill 信息（用于清理本地文件）
	skill, err := s.db.GetSkill(id)
	if err == nil && skill != nil {
		localStore := skills.NewLocalStore(s.config.HomeDir)
		localStore.Remove(skill.Name) // 清理本地文件，忽略错误
	}

	if err := s.db.DeleteSkill(id, userID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleRunSkill POST /api/v1/skills/{id}/run — 直接运行 Skill
// 构建临时单节点工作流并提交到 WorkerPool 执行
func (s *Server) handleRunSkill(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	id := r.PathValue("id")

	var req struct {
		Prompt       string `json:"prompt"`         // 用户输入
		Model        string `json:"model"`          // 可选覆盖模型
		UseSystemKey bool   `json:"use_system_key"` // 使用系统 API Key
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Prompt == "" {
		http.Error(w, "prompt is required", http.StatusBadRequest)
		return
	}

	// 获取 Skill
	skill, err := s.db.GetSkill(id)
	if err != nil {
		http.Error(w, "skill not found", http.StatusNotFound)
		return
	}

	// 构建临时工作流对象（单个 skill 节点）
	wf := buildSkillWorkflow(skill, req.Prompt, req.Model, req.UseSystemKey)

	// 生成执行 ID
	uuidStr := uuid.New().String()[:4]
	execID := time.Now().Format("102150405") + "-" + uuidStr

	ctx := models.NewExecutionContext(execID, userID, s.config.HomeDir)
	ctx.SetWorkflowName(wf.Name)
	ctx.WorkflowID = wf.ID
	ctx.DB = s.db

	job := &WorkflowJob{
		ExecutionID: execID,
		Workflow:    wf,
		Context:     ctx,
		DB:          s.db,
	}

	if err := s.workerPool.Submit(job); err != nil {
		http.Error(w, fmt.Sprintf("failed to submit: %v", err), http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{
		"execution_id": execID,
		"skill_id":     id,
		"status":       "queued",
	})
}

// buildSkillWorkflow 构建用于执行 Skill 的临时工作流对象
func buildSkillWorkflow(skill *storage.SkillRecord, prompt, model string, useSystemKey bool) *models.Workflow {
	if model == "" {
		model = "claude-sonnet-4-20250514"
	}

	skillID := skill.ID
	wfName := "skill-" + skill.Name

	return &models.Workflow{
		ID:          wfName + "_ephemeral",
		Name:        wfName,
		Description: "Auto-generated workflow for skill: " + skill.Name,
		Nodes: map[string]*models.Node{
			"run_skill": {
				ID:   "run_skill",
				Name: "Run " + skill.Name,
				Type: "skill",
				Config: map[string]interface{}{
					"skill_id":       skillID,
					"prompt":         prompt,
					"model":          model,
					"use_system_key": useSystemKey,
				},
			},
		},
	}
}

// --- Registry Handlers (Browse skills.sh) ---

// handleRegistrySearch GET /api/v1/registry/search?q=xxx — 搜索 skills.sh
func (s *Server) handleRegistrySearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		http.Error(w, "search query 'q' is required", http.StatusBadRequest)
		return
	}

	client := skills.NewRegistryClient("")
	result, err := client.Search(query, 1, 20)
	if err != nil {
		// Registry 不可达时返回空结果而非错误
		log.Printf("[registry] search failed: %v", err)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"skills": []interface{}{},
			"total":  0,
			"error":  err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleRegistryTrending GET /api/v1/registry/trending — 热门技能
func (s *Server) handleRegistryTrending(w http.ResponseWriter, r *http.Request) {
	client := skills.NewRegistryClient("")
	result, err := client.Trending(20)
	if err != nil {
		log.Printf("[registry] trending failed: %v", err)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"skills": []interface{}{},
			"total":  0,
			"error":  err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// --- Helper functions ---

func toJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func inferSkillName(gitURL string) string {
	// 从 git URL 推断名称
	// e.g., "https://github.com/user/my-skill.git" → "my-skill"
	url := strings.TrimSuffix(gitURL, ".git")
	parts := strings.Split(url, "/")
	if len(parts) > 0 {
		return strings.ToLower(parts[len(parts)-1])
	}
	return ""
}

