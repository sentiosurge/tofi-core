package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"tofi-core/internal/models"
	"tofi-core/internal/provider"
	"tofi-core/internal/skills"
	"tofi-core/internal/storage"

	"github.com/google/uuid"
)

// --- System Skills API ---

// handleListSystemSkills returns all scope="system" skills (built-in skills).
// GET /api/v1/skills/system
func (s *Server) handleListSystemSkills(w http.ResponseWriter, r *http.Request) {
	records, err := s.db.ListSystemSkills()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "failed to list system skills", "")
		return
	}
	type systemSkillResp struct {
		ID              string `json:"id"`
		Name            string `json:"name"`
		Description     string `json:"description"`
		RequiredSecrets string `json:"required_secrets"`
		HasScripts      bool   `json:"has_scripts"`
	}
	var resp []systemSkillResp
	for _, r := range records {
		resp = append(resp, systemSkillResp{
			ID:              r.ID,
			Name:            r.Name,
			Description:     r.Description,
			RequiredSecrets: r.RequiredSecrets,
			HasScripts:      r.HasScripts,
		})
	}
	if resp == nil {
		resp = []systemSkillResp{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// --- Skill API Handlers ---

// handleListSkills GET /api/v1/skills — 列出用户可见的所有 Skills
// Filesystem is the single source of truth: user skills from disk, system skills from embed FS.
func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	keyword := r.URL.Query().Get("q")
	keywordLower := strings.ToLower(keyword)

	var records []*storage.SkillRecord

	// User skills from filesystem
	localStore := skills.NewLocalStore(s.config.HomeDir)
	if userSkills, err := localStore.ListUserSkills(userID); err == nil {
		for _, sk := range userSkills {
			if keyword != "" && !strings.Contains(strings.ToLower(sk.Name), keywordLower) && !strings.Contains(strings.ToLower(sk.Desc), keywordLower) {
				continue
			}
			records = append(records, &storage.SkillRecord{
				ID:          "user/" + sk.Name,
				Name:        sk.Name,
				Description: sk.Desc,
				Version:     sk.Version,
				Scope:       sk.Scope,
				Source:       "local",
				UserID:      userID,
			})
		}
	}

	// System skills from embed FS
	systemSkills := skills.LoadAllSystemSkills()
	for _, sf := range systemSkills {
		if keyword != "" && !strings.Contains(strings.ToLower(sf.Manifest.Name), keywordLower) && !strings.Contains(strings.ToLower(sf.Manifest.Description), keywordLower) {
			continue
		}
		records = append(records, &storage.SkillRecord{
			ID:          "system/" + sf.Manifest.Name,
			Name:        sf.Manifest.Name,
			Description: sf.Manifest.Description,
			Version:     sf.Manifest.Version,
			Scope:       "system",
			Source:       "builtin",
			UserID:      "system",
		})
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
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "skill id required", "")
		return
	}

	skill, err := s.db.GetSkill(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrSkillNotFound, "skill not found", "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(skill)
}

// handleCreateSkill POST /api/v1/skills/create — 表单式创建 Skill
func (s *Server) handleCreateSkill(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	var req struct {
		Name         string                       `json:"name"`
		Description  string                       `json:"description"`
		Model        string                       `json:"model"`
		AllowedTools string                       `json:"allowed_tools"`
		Instructions string                       `json:"instructions"` // Markdown body
		Inputs       map[string]*models.SkillInput `json:"inputs"`
		Output       *models.SkillOutput           `json:"output"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", "")
		return
	}

	if req.Name == "" {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "name is required", "")
		return
	}
	if req.Instructions == "" {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "instructions is required", "")
		return
	}

	// 构建 SkillManifest
	manifest := models.SkillManifest{
		Name:         req.Name,
		Description:  req.Description,
		Model:        req.Model,
		AllowedTools: req.AllowedTools,
		Inputs:       req.Inputs,
		Output:       req.Output,
	}

	manifestJSON, _ := json.Marshal(manifest)
	inputSchema, _ := json.Marshal(req.Inputs)
	outputSchema, _ := json.Marshal(req.Output)

	now := time.Now().Format("2006-01-02 15:04:05")
	record := &storage.SkillRecord{
		ID:           fmt.Sprintf("%s/%s", userID, req.Name),
		Name:         req.Name,
		Description:  req.Description,
		Version:      "1.0",
		Scope:        "private",
		Source:       "local",
		ManifestJSON: string(manifestJSON),
		Instructions: req.Instructions,
		InputSchema:  string(inputSchema),
		OutputSchema: string(outputSchema),
		AllowedTools: toJSON(manifest.AllowedToolsList()),
		UserID:       userID,
		InstalledAt:  now,
	}

	if err := s.db.SaveSkill(record); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, fmt.Sprintf("failed to save skill: %v", err), "")
		return
	}

	// 保存到用户目录
	localStore := skills.NewLocalStore(s.config.HomeDir)
	content := buildSkillMDFromRecord(record)
	if err := localStore.SaveUserLocal(userID, req.Name, content); err != nil {
		log.Printf("[skills] warning: failed to save to user store: %v", err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(record)
}

// handleUpdateSkill PUT /api/v1/skills/{id} — 编辑已有 Skill
func (s *Server) handleUpdateSkill(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	existing, err := s.db.GetSkill(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrSkillNotFound, "skill not found", "")
		return
	}

	// 只能编辑自己的私有 Skill
	if existing.Scope == "public" || (existing.UserID != userID && existing.UserID != "system") {
		writeJSONError(w, http.StatusForbidden, ErrForbidden, "cannot edit public or others' skills", "")
		return
	}

	var req struct {
		Name         string                       `json:"name"`
		Description  string                       `json:"description"`
		Model        string                       `json:"model"`
		AllowedTools string                       `json:"allowed_tools"`
		Instructions string                       `json:"instructions"`
		Inputs       map[string]*models.SkillInput `json:"inputs"`
		Output       *models.SkillOutput           `json:"output"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", "")
		return
	}

	// 更新字段
	if req.Name != "" {
		existing.Name = req.Name
	}
	if req.Description != "" {
		existing.Description = req.Description
	}
	if req.Instructions != "" {
		existing.Instructions = req.Instructions
	}

	manifest := models.SkillManifest{
		Name:         existing.Name,
		Description:  existing.Description,
		Model:        req.Model,
		AllowedTools: req.AllowedTools,
		Inputs:       req.Inputs,
		Output:       req.Output,
	}
	manifestJSON, _ := json.Marshal(manifest)
	existing.ManifestJSON = string(manifestJSON)

	if req.Inputs != nil {
		inputSchema, _ := json.Marshal(req.Inputs)
		existing.InputSchema = string(inputSchema)
	}
	if req.Output != nil {
		outputSchema, _ := json.Marshal(req.Output)
		existing.OutputSchema = string(outputSchema)
	}

	existing.AllowedTools = toJSON(manifest.AllowedToolsList())

	if err := s.db.SaveSkill(existing); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, fmt.Sprintf("failed to update skill: %v", err), "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(existing)
}

// handleTestSkill POST /api/v1/skills/{id}/test — 在线测试 Skill（不保存执行记录）
func (s *Server) handleTestSkill(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	var req struct {
		Prompt string                 `json:"prompt"`
		Inputs map[string]interface{} `json:"inputs"` // 结构化输入
		Model  string                 `json:"model"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", "")
		return
	}

	if req.Prompt == "" && len(req.Inputs) == 0 {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "prompt or inputs is required", "")
		return
	}

	skill, err := s.db.GetSkill(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrSkillNotFound, "skill not found", "")
		return
	}

	// 构建 prompt — 如果有结构化输入，组合成完整 prompt
	prompt := req.Prompt
	if len(req.Inputs) > 0 && prompt == "" {
		parts := []string{}
		for k, v := range req.Inputs {
			parts = append(parts, fmt.Sprintf("%s: %v", k, v))
		}
		prompt = strings.Join(parts, "\n")
	}

	model := req.Model
	if model == "" {
		model = "gpt-5-mini"
	}

	wf := buildSkillWorkflow(skill, prompt, model, true) // test 默认使用系统 key

	uuidStr := uuid.New().String()[:4]
	execID := "test-" + time.Now().Format("150405") + "-" + uuidStr

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
		writeJSONError(w, http.StatusServiceUnavailable, ErrInternal, fmt.Sprintf("failed to submit: %v", err), "")
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

// handleExportSkill POST /api/v1/skills/{id}/export — 导出为 SKILL.md 文件
func (s *Server) handleExportSkill(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	skill, err := s.db.GetSkill(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrSkillNotFound, "skill not found", "")
		return
	}

	content := buildSkillMDFromRecord(skill)

	w.Header().Set("Content-Type", "text/markdown")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.SKILL.md\"", skill.Name))
	w.Write([]byte(content))
}

// handleInstallSkill POST /api/v1/skills/install — 安装 Skill
//
// 统一安装入口，支持多种方式:
//
//  1. source: "local" + content          — 直接粘贴 SKILL.md 内容
//  2. source: "git" + url                — owner/repo@skill 或 Git URL（直接安装，向后兼容）
//  3. source 省略 + content              — 等同于 local
//  4. mode: "preview" + url              — 预览安装：clone + discover，返回 skill 列表
//  5. mode: "confirm" + session_id       — 确认安装：从缓存安装选中的 skills
func (s *Server) handleInstallSkill(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	var req struct {
		Source     string   `json:"source"`      // "local" | "git" | ""
		Content    string   `json:"content"`     // SKILL.md 内容 (source=local)
		URL        string   `json:"url"`         // owner/repo@skill 或 Git URL (source=git)
		Mode       string   `json:"mode"`        // "" | "preview" | "confirm"
		SessionID  string   `json:"session_id"`  // confirm 模式需要
		SkillNames []string `json:"skill_names"` // confirm 模式可选（选择性安装）
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", "")
		return
	}

	// 两阶段安装模式
	switch req.Mode {
	case "preview":
		if req.URL == "" {
			writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "url is required for preview", "")
			return
		}
		s.installPreview(w, userID, req.URL)
		return
	case "confirm":
		if req.SessionID == "" {
			writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "session_id is required for confirm", "")
			return
		}
		s.installConfirm(w, userID, req.SessionID, req.SkillNames)
		return
	}

	// 原有逻辑（向后兼容）
	switch req.Source {
	case "local", "":
		if req.Content == "" {
			writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "content is required for local install", "")
			return
		}
		s.installFromContent(w, userID, req.Content, "local", "")

	case "git":
		if req.URL == "" {
			writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "url is required for git install", "")
			return
		}
		s.installFromSource(w, userID, req.URL)

	default:
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, fmt.Sprintf("unsupported source: %s", req.Source), "")
	}
}

// installFromContent 从 SKILL.md 内容安装（本地粘贴 → 私有 Skill）
func (s *Server) installFromContent(w http.ResponseWriter, userID, content, source, sourceURL string) {
	skillFile, err := skills.Parse([]byte(content))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, fmt.Sprintf("invalid SKILL.md: %v", err), "")
		return
	}

	// 保存到用户目录（私有 skill）
	localStore := skills.NewLocalStore(s.config.HomeDir)
	if err := localStore.SaveUserLocal(userID, skillFile.Manifest.Name, content); err != nil {
		log.Printf("[skills] warning: failed to save to user store: %v", err)
	}

	// 保存到数据库（私有）
	record := s.buildSkillRecord(userID, skillFile, source, sourceURL, "private")
	if err := s.db.SaveSkill(record); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, fmt.Sprintf("failed to save skill: %v", err), "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(record)
}

// installFromSource 从 source 字符串安装（Git → 公共 Skill 池）
// 支持 owner/repo@skill、Git URL 等格式
func (s *Server) installFromSource(w http.ResponseWriter, userID, source string) {
	// 去重检查：同一 source_url 是否已有公共 Skill
	existing, err := s.db.FindPublicSkillBySource(source)
	if err == nil && existing != nil {
		// 已存在，直接返回（不重复下载）
		log.Printf("[skills] skip duplicate install: %s already exists as %s", source, existing.ID)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(existing)
		return
	}

	localStore := skills.NewLocalStore(s.config.HomeDir)
	installer := skills.NewSkillInstaller(localStore)

	result, err := installer.Install(source)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, fmt.Sprintf("install failed: %v", err), "")
		return
	}

	// Git 安装的 Skills 进入公共池（scope=public, user_id=system）
	// 同时为请求用户创建 symlink
	var records []*storage.SkillRecord
	for _, sf := range result.Skills {
		record := s.buildSkillRecord("system", sf, string(result.Source.Type), result.Source.DisplayURL(), "public")
		if err := s.db.SaveSkill(record); err != nil {
			log.Printf("[skills] warning: failed to save skill %s: %v", sf.Manifest.Name, err)
			continue
		}
		records = append(records, record)

		// Create symlink for the requesting user
		if err := localStore.ActivateGlobalSkill(userID, sf.Manifest.Name, sf.Manifest.Name); err != nil {
			log.Printf("[skills] warning: failed to activate %s for user %s: %v", sf.Manifest.Name, userID, err)
		}
	}

	if len(records) == 0 {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "failed to save any skills to database", "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	if len(records) == 1 {
		json.NewEncoder(w).Encode(records[0])
	} else {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"installed": len(records),
			"skills":    records,
		})
	}
}

// buildSkillRecord 构建 SkillRecord 数据库记录
func (s *Server) buildSkillRecord(userID string, sf *models.SkillFile, source, sourceURL, scope string) *storage.SkillRecord {
	manifest := sf.Manifest
	manifestJSON, _ := json.Marshal(manifest)

	// ID 格式：public/skill-name 或 user/skill-name
	idPrefix := userID
	if scope == "public" {
		idPrefix = "public"
	}

	inputSchema, _ := json.Marshal(manifest.Inputs)
	outputSchema, _ := json.Marshal(manifest.Output)

	return &storage.SkillRecord{
		ID:           fmt.Sprintf("%s/%s", idPrefix, manifest.Name),
		Name:         manifest.Name,
		Description:  manifest.Description,
		Version:      "1.0",
		Scope:        scope,
		Source:       source,
		SourceURL:    sourceURL,
		ManifestJSON: string(manifestJSON),
		Instructions: sf.Body,
		InputSchema:  string(inputSchema),
		OutputSchema: string(outputSchema),
		HasScripts:   len(sf.ScriptDirs) > 0,
		RequiredSecrets: toJSON(manifest.RequiredEnvVars()),
		AllowedTools:    toJSON(manifest.AllowedToolsList()),
		UserID:       userID,
		InstalledAt:  time.Now().Format("2006-01-02 15:04:05"),
	}
}

// handleDeleteSkill DELETE /api/v1/skills/{id} — 卸载 Skill
func (s *Server) handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "skill id required", "")
		return
	}

	// 清理用户目录（symlink 或真实目录）+ GC 全局
	skill, err := s.db.GetSkill(id)
	if err == nil && skill != nil {
		localStore := skills.NewLocalStore(s.config.HomeDir)
		if deactErr := localStore.DeactivateSkill(userID, skill.Name); deactErr != nil {
			log.Printf("[skills] warning: deactivate %s for user %s: %v", skill.Name, userID, deactErr)
		}
	}

	if err := s.db.DeleteSkill(id, userID); err != nil {
		writeJSONError(w, http.StatusNotFound, ErrSkillNotFound, err.Error(), "")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleGetSkillResources GET /api/v1/skills/{id}/resources — 获取 Skill 的所有资源目录和文件
func (s *Server) handleGetSkillResources(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "skill id required", "")
		return
	}

	skill, err := s.db.GetSkill(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrSkillNotFound, "skill not found", "")
		return
	}

	type ResourceFile struct {
		Name      string `json:"name"`
		Content   string `json:"content"`
		Size      int64  `json:"size"`
		Truncated bool   `json:"truncated,omitempty"`
		Binary    bool   `json:"binary,omitempty"`
	}

	type ResourceDirectory struct {
		Name  string         `json:"name"`
		Files []ResourceFile `json:"files"`
	}

	localStore := skills.NewLocalStore(s.config.HomeDir)
	skillDir := localStore.SkillDir(skill.Name)

	topEntries, err := os.ReadDir(skillDir)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]interface{}{})
		return
	}

	// 二进制文件扩展名
	binaryExts := map[string]bool{
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".ico": true,
		".webp": true, ".svg": true, ".bmp": true, ".tiff": true,
		".zip": true, ".tar": true, ".gz": true, ".whl": true,
		".pdf": true, ".exe": true, ".bin": true, ".so": true, ".dylib": true,
		".wasm": true, ".pyc": true,
	}

	const maxSize = 100 * 1024 // 100KB

	var dirs []ResourceDirectory
	for _, de := range topEntries {
		if !de.IsDir() {
			continue
		}
		dirName := de.Name()
		// 跳过隐藏目录和 __pycache__
		if strings.HasPrefix(dirName, ".") || dirName == "__pycache__" || dirName == "node_modules" {
			continue
		}

		subDir := filepath.Join(skillDir, dirName)
		fileEntries, err := os.ReadDir(subDir)
		if err != nil {
			continue
		}

		var files []ResourceFile
		for _, fe := range fileEntries {
			if fe.IsDir() {
				continue
			}
			info, err := fe.Info()
			if err != nil {
				continue
			}

			rf := ResourceFile{
				Name: fe.Name(),
				Size: info.Size(),
			}

			ext := strings.ToLower(filepath.Ext(fe.Name()))
			if binaryExts[ext] {
				rf.Binary = true
			} else {
				data, err := os.ReadFile(filepath.Join(subDir, fe.Name()))
				if err == nil {
					if len(data) > maxSize {
						rf.Content = string(data[:maxSize])
						rf.Truncated = true
					} else {
						rf.Content = string(data)
					}
				}
			}
			files = append(files, rf)
		}

		if files == nil {
			files = []ResourceFile{}
		}
		dirs = append(dirs, ResourceDirectory{Name: dirName, Files: files})
	}

	if dirs == nil {
		dirs = []ResourceDirectory{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(dirs)
}

// handlePutSkillResource PUT /api/v1/skills/{id}/resources — 创建/更新资源文件
func (s *Server) handlePutSkillResource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "skill id required", "")
		return
	}

	skill, err := s.db.GetSkill(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrSkillNotFound, "skill not found", "")
		return
	}

	// 仅允许 local skill 修改资源
	if skill.Source != "local" {
		writeJSONError(w, http.StatusForbidden, ErrForbidden, "only local skills can be modified", "")
		return
	}

	var req struct {
		Dir      string `json:"dir"`
		Filename string `json:"filename"`
		Content  string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", "")
		return
	}
	if req.Dir == "" || req.Filename == "" {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "dir and filename are required", "")
		return
	}
	// 安全检查：防止路径遍历
	if strings.Contains(req.Dir, "..") || strings.Contains(req.Filename, "..") ||
		strings.Contains(req.Dir, "/") || strings.Contains(req.Filename, "/") {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "invalid dir or filename", "")
		return
	}

	localStore := skills.NewLocalStore(s.config.HomeDir)
	dirPath := filepath.Join(localStore.SkillDir(skill.Name), req.Dir)
	if err := os.MkdirAll(dirPath, 0755); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "failed to create directory", "")
		return
	}

	filePath := filepath.Join(dirPath, req.Filename)
	if err := os.WriteFile(filePath, []byte(req.Content), 0644); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "failed to write file", "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleDeleteSkillResource DELETE /api/v1/skills/{id}/resources — 删除资源文件
func (s *Server) handleDeleteSkillResource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "skill id required", "")
		return
	}

	skill, err := s.db.GetSkill(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrSkillNotFound, "skill not found", "")
		return
	}

	if skill.Source != "local" {
		writeJSONError(w, http.StatusForbidden, ErrForbidden, "only local skills can be modified", "")
		return
	}

	var req struct {
		Dir      string `json:"dir"`
		Filename string `json:"filename"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", "")
		return
	}
	if req.Dir == "" || req.Filename == "" {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "dir and filename are required", "")
		return
	}
	if strings.Contains(req.Dir, "..") || strings.Contains(req.Filename, "..") ||
		strings.Contains(req.Dir, "/") || strings.Contains(req.Filename, "/") {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "invalid dir or filename", "")
		return
	}

	localStore := skills.NewLocalStore(s.config.HomeDir)
	filePath := filepath.Join(localStore.SkillDir(skill.Name), req.Dir, req.Filename)
	if err := os.Remove(filePath); err != nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "failed to delete file", "")
		return
	}

	// 清理空目录
	dirPath := filepath.Join(localStore.SkillDir(skill.Name), req.Dir)
	entries, _ := os.ReadDir(dirPath)
	if len(entries) == 0 {
		os.Remove(dirPath)
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleRunSkill POST /api/v1/skills/{id}/run — 直接运行 Skill
func (s *Server) handleRunSkill(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	var req struct {
		Prompt       string                 `json:"prompt"`         // 用户输入
		Inputs       map[string]interface{} `json:"inputs"`         // 结构化输入
		Model        string                 `json:"model"`          // 可选覆盖模型
		UseSystemKey bool                   `json:"use_system_key"` // 使用系统 API Key
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", "")
		return
	}

	if req.Prompt == "" && len(req.Inputs) == 0 {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "prompt or inputs is required", "")
		return
	}

	skill, err := s.db.GetSkill(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrSkillNotFound, "skill not found", "")
		return
	}

	// 构建 prompt
	prompt := req.Prompt
	if len(req.Inputs) > 0 && prompt == "" {
		parts := []string{}
		for k, v := range req.Inputs {
			parts = append(parts, fmt.Sprintf("%s: %v", k, v))
		}
		prompt = strings.Join(parts, "\n")
	}

	wf := buildSkillWorkflow(skill, prompt, req.Model, req.UseSystemKey)

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
		writeJSONError(w, http.StatusServiceUnavailable, ErrInternal, fmt.Sprintf("failed to submit: %v", err), "")
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
		model = "gpt-5-mini"
	}

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
					"skill_id":       skill.ID,
					"prompt":         prompt,
					"model":          model,
					"use_system_key": useSystemKey,
				},
			},
		},
	}
}

// --- Registry Handlers (搜索 skills.sh) ---

// handleRegistrySearch GET /api/v1/registry/search?q=xxx — 搜索 skills.sh
func (s *Server) handleRegistrySearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "search query 'q' is required", "")
		return
	}

	client := skills.NewRegistryClient("")
	result, err := client.Search(query, 10)
	if err != nil {
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

// --- Settings / AI Key Handlers ---

// handleListAIKeys GET /api/v1/settings/ai-keys — 列出 AI Key 配置
func (s *Server) handleListAIKeys(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	// 获取系统级 + 用户级
	systemKeys, _ := s.db.ListAIKeys("system")
	userKeys, _ := s.db.ListAIKeys(userID)

	if systemKeys == nil {
		systemKeys = []map[string]string{}
	}
	if userKeys == nil {
		userKeys = []map[string]string{}
	}

	// 检测环境变量中的 key（补充到 env 分组）
	envKeys := []map[string]string{}
	envProviders := map[string]string{
		"openai":    "TOFI_OPENAI_API_KEY",
		"anthropic": "TOFI_ANTHROPIC_API_KEY",
		"gemini":    "TOFI_GEMINI_API_KEY",
		"deepseek":  "TOFI_DEEPSEEK_API_KEY",
	}
	for provider, envName := range envProviders {
		if v := os.Getenv(envName); v != "" {
			envKeys = append(envKeys, map[string]string{
				"provider":   provider,
				"masked_key": func(k string) string {
				if len(k) <= 8 {
					return "****"
				}
				return k[:4] + "****" + k[len(k)-4:]
			}(v),
				"source":     envName,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"system": systemKeys,
		"user":   userKeys,
		"env":    envKeys,
	})
}

// handleSetAIKey POST /api/v1/settings/ai-keys — 设置 AI Key（内部，支持 scope 参数）
func (s *Server) handleSetAIKey(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	role, _ := r.Context().Value(RoleContextKey).(string)

	var req struct {
		Provider string `json:"provider"` // anthropic, openai, gemini
		APIKey   string `json:"api_key"`
		Scope    string `json:"scope"` // "system" | "user"
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", "")
		return
	}

	if req.Provider == "" || req.APIKey == "" {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "provider and api_key are required", "")
		return
	}

	scope := userID
	if req.Scope == "system" {
		// 只有 admin 能设置系统级 key
		if role != "admin" {
			writeJSONError(w, http.StatusForbidden, ErrForbidden, "only admin can set system-level API keys", "")
			return
		}
		scope = "system"
	} else {
		// 非 admin 用户检查 allow_user_keys 开关
		if role != "admin" && !s.db.AllowUserKeys() {
			writeJSONError(w, http.StatusForbidden, ErrUserKeysDisabled, "user API keys are disabled by admin", "")
			return
		}
	}

	if err := s.db.SetAIKey(req.Provider, scope, req.APIKey); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, fmt.Sprintf("failed to save: %v", err), "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":   "ok",
		"provider": req.Provider,
		"scope":    scope,
	})
}

// handleDeleteAIKey DELETE /api/v1/settings/ai-keys/{provider} — 删除 AI Key
func (s *Server) handleDeleteAIKey(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	role, _ := r.Context().Value(RoleContextKey).(string)
	provider := r.PathValue("provider")

	scope := r.URL.Query().Get("scope")
	if scope == "" || scope == "user" {
		scope = userID
	}

	// 只有 admin 能删除系统级 key
	if scope == "system" && role != "admin" {
		writeJSONError(w, http.StatusForbidden, ErrForbidden, "only admin can delete system-level API keys", "")
		return
	}

	if err := s.db.DeleteAIKey(provider, scope); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, err.Error(), "")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- 用户端 AI Key API ---

// handleUserSetAIKey PUT /api/v1/user/settings/ai-key — 用户设置自己的 AI Key
func (s *Server) handleUserSetAIKey(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	// 检查 admin 开关
	if !s.db.AllowUserKeys() {
		writeJSONError(w, http.StatusForbidden, ErrUserKeysDisabled, "user API keys are disabled by admin", "")
		return
	}

	var req struct {
		Provider string `json:"provider"`
		APIKey   string `json:"api_key"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", "")
		return
	}

	if req.Provider == "" || req.APIKey == "" {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "provider and api_key are required", "")
		return
	}

	if err := s.db.SetAIKey(req.Provider, userID, req.APIKey); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, fmt.Sprintf("failed to save: %v", err), "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":   "ok",
		"provider": req.Provider,
	})
}

// handleUserListAIKeys GET /api/v1/user/settings/ai-keys — 用户查看自己的 AI Key（脱敏）
func (s *Server) handleUserListAIKeys(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	keys, _ := s.db.ListAIKeys(userID)
	if keys == nil {
		keys = []map[string]string{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"keys":            keys,
		"allow_user_keys": s.db.AllowUserKeys(),
	})
}

// handleUserDeleteAIKey DELETE /api/v1/user/settings/ai-key/{provider} — 用户删除自己的 AI Key
func (s *Server) handleUserDeleteAIKey(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	provider := r.PathValue("provider")

	if err := s.db.DeleteAIKey(provider, userID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, err.Error(), "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "provider": provider})
}

// handleSetAllowUserKeys PUT /api/v1/admin/settings/allow-user-keys — Admin 设置开关
func (s *Server) handleSetAllowUserKeys(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Allow bool `json:"allow"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", "")
		return
	}

	if err := s.db.SetAllowUserKeys(req.Allow); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, fmt.Sprintf("failed to save: %v", err), "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":          "ok",
		"allow_user_keys": req.Allow,
	})
}

// handleListModels GET /api/v1/models — 列出所有已知模型
// 支持 ?enabled=true 仅返回用户已启用的模型
func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	type modelEntry struct {
		Name            string  `json:"name"`
		Provider        string  `json:"provider"`
		ContextWindow   int     `json:"context_window"`
		InputCostPer1M  float64 `json:"input_cost_per_1m"`
		OutputCostPer1M float64 `json:"output_cost_per_1m"`
	}

	// 构建 enabled 过滤：只返回用户有 key 的 provider 的模型
	var enabledProviders map[string]bool
	if r.URL.Query().Get("enabled") == "true" {
		userID := r.Context().Value(UserContextKey).(string)
		enabledProviders = make(map[string]bool)
		for _, pName := range []string{"openai", "anthropic", "gemini", "deepseek", "groq", "openrouter"} {
			if key := s.findAPIKey(pName, userID); key != "" {
				enabledProviders[pName] = true
			}
		}
	}

	all := provider.ListAllModels()
	models := make([]modelEntry, 0, len(all))
	for name, info := range all {
		if enabledProviders != nil && !enabledProviders[info.Provider] {
			continue
		}
		models = append(models, modelEntry{
			Name:            name,
			Provider:        info.Provider,
			ContextWindow:   info.ContextWindow,
			InputCostPer1M:  info.InputCostPer1M,
			OutputCostPer1M: info.OutputCostPer1M,
		})
	}

	// Sort by provider order, then by cost descending (flagship models first)
	providerOrder := map[string]int{
		"openai": 0, "anthropic": 1, "gemini": 2, "deepseek": 3, "groq": 4, "openrouter": 5,
	}
	sort.SliceStable(models, func(i, j int) bool {
		oi, oki := providerOrder[models[i].Provider]
		oj, okj := providerOrder[models[j].Provider]
		if !oki {
			oi = 99
		}
		if !okj {
			oj = 99
		}
		if oi != oj {
			return oi < oj
		}
		// Within same provider, sort by output cost descending (flagship first)
		return models[i].OutputCostPer1M > models[j].OutputCostPer1M
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(models)
}

// handleGetPreferredModel GET /api/v1/settings/preferred-model
func (s *Server) handleGetPreferredModel(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	model, _ := s.db.GetSetting("preferred_model", userID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"model": model})
}

// handleSetPreferredModel POST /api/v1/settings/preferred-model
func (s *Server) handleSetPreferredModel(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	var req struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "invalid request", "")
		return
	}

	if req.Model == "" {
		// Clear preference
		s.db.DeleteSetting("preferred_model", userID)
	} else {
		s.db.SetSetting("preferred_model", userID, req.Model)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "model": req.Model})
}

// handleGetEnabledModels GET /api/v1/settings/enabled-models
func (s *Server) handleGetEnabledModels(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	val, _ := s.db.GetSetting("enabled_models", userID)
	var models []string
	if val != "" {
		json.Unmarshal([]byte(val), &models)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"models": models})
}

// handleSetEnabledModels POST /api/v1/settings/enabled-models
func (s *Server) handleSetEnabledModels(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	var req struct {
		Models []string `json:"models"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "invalid request", "")
		return
	}

	if len(req.Models) == 0 {
		s.db.DeleteSetting("enabled_models", userID)
	} else {
		data, _ := json.Marshal(req.Models)
		s.db.SetSetting("enabled_models", userID, string(data))
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "models": req.Models})
}

// --- Helper functions ---

func toJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// --- Two-Phase Install: Preview + Confirm ---

// installPreview 预览安装：clone + discover，缓存 session，返回 skill 列表
func (s *Server) installPreview(w http.ResponseWriter, userID, source string) {
	// 去重检查
	existing, err := s.db.FindPublicSkillBySource(source)
	if err == nil && existing != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"already_installed": true,
			"skill":            existing,
		})
		return
	}

	localStore := skills.NewLocalStore(s.config.HomeDir)
	installer := skills.NewSkillInstaller(localStore)

	result, cleanup, err := installer.PreviewInstall(source)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, fmt.Sprintf("preview failed: %v", err), "")
		return
	}

	// 如果只有 1 个 skill，无需预览，直接安装
	if len(result.Skills) == 1 {
		cleanup() // 关闭预览临时目录
		s.installFromSource(w, userID, source)
		return
	}

	// 多个 skill → 缓存 session，返回预览
	sessionID := uuid.New().String()[:8]
	s.createPreviewSession(sessionID, &PreviewSession{
		Skills:    result.Skills,
		Source:    result.Source,
		Cleanup:   cleanup,
		CreatedAt: time.Now(),
	})

	type skillPreview struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	previews := make([]skillPreview, len(result.Skills))
	for i, sf := range result.Skills {
		previews[i] = skillPreview{
			Name:        sf.Manifest.Name,
			Description: sf.Manifest.Description,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"preview":    true,
		"session_id": sessionID,
		"source_url": result.Source.DisplayURL(),
		"skills":     previews,
		"total":      len(previews),
	})
}

// installConfirm 确认安装：从缓存的 session 中安装选中的 skills
func (s *Server) installConfirm(w http.ResponseWriter, userID, sessionID string, skillNames []string) {
	session := s.getPreviewSession(sessionID)
	if session == nil {
		writeJSONError(w, http.StatusNotFound, ErrSessionNotFound, "session expired or not found", "")
		return
	}
	defer s.removePreviewSession(sessionID)

	// 确定要安装的 skills
	toInstall := session.Skills
	if len(skillNames) > 0 {
		nameSet := make(map[string]bool)
		for _, n := range skillNames {
			nameSet[n] = true
		}
		var filtered []*models.SkillFile
		for _, sf := range session.Skills {
			if nameSet[sf.Manifest.Name] {
				filtered = append(filtered, sf)
			}
		}
		toInstall = filtered
	}

	if len(toInstall) == 0 {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "no skills selected", "")
		return
	}

	// 安装到 local store + DB
	localStore := skills.NewLocalStore(s.config.HomeDir)
	installer := skills.NewSkillInstaller(localStore)

	var records []*storage.SkillRecord
	for _, sf := range toInstall {
		// Use session scope/userID if set (zip upload = private), else default to public/system (git)
		scope := "public"
		recordUserID := "system"
		sourceType := ""
		sourceURL := ""
		if session.Scope != "" {
			scope = session.Scope
			recordUserID = session.UserID
			sourceType = "local"
			sourceURL = "zip-upload"
		} else if session.Source != nil {
			sourceType = string(session.Source.Type)
			sourceURL = session.Source.DisplayURL()
		}

		if scope == "private" {
			// User upload → save directly to user dir
			content, _ := os.ReadFile(filepath.Join(sf.Dir, "SKILL.md"))
			if err := localStore.SaveUserLocal(userID, sf.Manifest.Name, string(content)); err != nil {
				log.Printf("[skills] warning: failed to save %s to user dir: %v", sf.Manifest.Name, err)
			}
		} else {
			// Git install → global dir + symlink for user
			if err := installer.InstallOne(sf); err != nil {
				log.Printf("[skills] warning: failed to install %s to global: %v", sf.Manifest.Name, err)
			}
			if err := localStore.ActivateGlobalSkill(userID, sf.Manifest.Name, sf.Manifest.Name); err != nil {
				log.Printf("[skills] warning: failed to activate %s for user %s: %v", sf.Manifest.Name, userID, err)
			}
		}

		record := s.buildSkillRecord(recordUserID, sf, sourceType, sourceURL, scope)
		if err := s.db.SaveSkill(record); err != nil {
			log.Printf("[skills] warning: failed to save skill %s: %v", sf.Manifest.Name, err)
			continue
		}
		records = append(records, record)
	}

	if len(records) == 0 {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "failed to save any skills to database", "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"installed": len(records),
		"skills":    records,
	})
}

// handleInstallSkillZip POST /api/v1/skills/install-zip — 上传 zip 包安装 Skill
//
// 接收 multipart/form-data，字段 "file" 为 .zip 文件。
// Zip 解压后通过 DiscoverSkills 发现 SKILL.md，然后走 InstallOne 流程。
// 单 skill → 直接安装返回 SkillRecord
// 多 skill → 返回 preview (复用 confirm 流程)
func (s *Server) handleInstallSkillZip(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	// 50MB limit
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "file too large or invalid multipart form", "")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "missing 'file' field in form data", "")
		return
	}
	defer file.Close()

	if !strings.HasSuffix(strings.ToLower(header.Filename), ".zip") {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "only .zip files are accepted", "")
		return
	}

	if header.Size > 50*1024*1024 {
		writeJSONError(w, http.StatusRequestEntityTooLarge, ErrBadRequest, "file too large (max 50MB)", "")
		return
	}

	// Write to temp file (zip.OpenReader needs a file path)
	tmpFile, err := os.CreateTemp("", "tofi-skill-*.zip")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "failed to create temp file", "")
		return
	}
	defer os.Remove(tmpFile.Name())

	if _, err := io.Copy(tmpFile, file); err != nil {
		tmpFile.Close()
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "failed to save uploaded file", "")
		return
	}
	tmpFile.Close()

	// Extract to temp dir
	tempDir, err := os.MkdirTemp("", "tofi-skill-zip-")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "failed to create temp directory", "")
		return
	}
	cleanup := func() { os.RemoveAll(tempDir) }

	if err := skills.ExtractZip(tmpFile.Name(), tempDir); err != nil {
		cleanup()
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, fmt.Sprintf("failed to extract zip: %v", err), "")
		return
	}

	// Discover skills in extracted directory
	discovered, err := skills.DiscoverSkills(tempDir)
	if err != nil || len(discovered) == 0 {
		cleanup()
		errMsg := "no SKILL.md found in zip"
		if err != nil {
			errMsg = fmt.Sprintf("skill discovery failed: %v", err)
		}
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, errMsg, "")
		return
	}

	localStore := skills.NewLocalStore(s.config.HomeDir)
	installer := skills.NewSkillInstaller(localStore)

	if len(discovered) == 1 {
		// Single skill → install directly
		defer cleanup()
		sf := discovered[0]

		// Check if this skill already exists (duplicate detection)
		skillID := fmt.Sprintf("%s/%s", userID, sf.Manifest.Name)
		existing, _ := s.db.GetSkill(skillID)
		isUpdate := existing != nil

		if err := installer.InstallOne(sf); err != nil {
			writeJSONError(w, http.StatusInternalServerError, ErrInternal, fmt.Sprintf("install failed: %v", err), "")
			return
		}

		record := s.buildSkillRecord(userID, sf, "local", "zip-upload", "private")
		if err := s.db.SaveSkill(record); err != nil {
			writeJSONError(w, http.StatusInternalServerError, ErrInternal, fmt.Sprintf("failed to save skill: %v", err), "")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"skill":   record,
			"updated": isUpdate,
		})
		return
	}

	// Multiple skills → preview mode (reuse confirm flow)
	sessionID := uuid.New().String()[:8]
	s.createPreviewSession(sessionID, &PreviewSession{
		Skills:    discovered,
		Cleanup:   cleanup,
		CreatedAt: time.Now(),
		Scope:     "private",
		UserID:    userID,
	})

	type skillPreview struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Exists      bool   `json:"exists"`
	}
	previews := make([]skillPreview, len(discovered))
	for i, sf := range discovered {
		skillID := fmt.Sprintf("%s/%s", userID, sf.Manifest.Name)
		existing, _ := s.db.GetSkill(skillID)
		previews[i] = skillPreview{
			Name:        sf.Manifest.Name,
			Description: sf.Manifest.Description,
			Exists:      existing != nil,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"preview":    true,
		"session_id": sessionID,
		"source_url": "zip-upload",
		"skills":     previews,
		"total":      len(previews),
	})
}

// --- Collection Handlers ---

// handleGetCollection GET /api/v1/skills/collection?source=xxx — 获取 Collection 中的所有 skills
func (s *Server) handleGetCollection(w http.ResponseWriter, r *http.Request) {
	sourceURL := r.URL.Query().Get("source")
	if sourceURL == "" {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "source query parameter is required", "")
		return
	}

	records, err := s.db.ListSkillsBySourceURL(sourceURL)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, err.Error(), "")
		return
	}
	if records == nil {
		records = []*storage.SkillRecord{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"source_url": sourceURL,
		"skills":     records,
		"total":      len(records),
	})
}

// handleDeleteCollection DELETE /api/v1/skills/collection?source=xxx — 删除整个 Collection
func (s *Server) handleDeleteCollection(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	sourceURL := r.URL.Query().Get("source")
	if sourceURL == "" {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "source query parameter is required", "")
		return
	}

	// 清理用户 symlink/目录 + GC 全局
	records, _ := s.db.ListSkillsBySourceURL(sourceURL)
	localStore := skills.NewLocalStore(s.config.HomeDir)
	for _, skill := range records {
		if err := localStore.DeactivateSkill(userID, skill.Name); err != nil {
			log.Printf("[skills] warning: deactivate %s: %v", skill.Name, err)
		}
	}

	count, err := s.db.DeleteSkillsBySourceURL(sourceURL, userID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, err.Error(), "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"deleted": count,
	})
}

// buildSkillMDFromRecord 从数据库记录重建 SKILL.md 内容
func buildSkillMDFromRecord(skill *storage.SkillRecord) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("name: %s\n", skill.Name))
	if skill.Description != "" {
		sb.WriteString(fmt.Sprintf("description: %s\n", skill.Description))
	}

	// 从 ManifestJSON 还原其他字段
	var manifest models.SkillManifest
	if err := json.Unmarshal([]byte(skill.ManifestJSON), &manifest); err == nil {
		if manifest.Model != "" {
			sb.WriteString(fmt.Sprintf("model: %s\n", manifest.Model))
		}
		if manifest.AllowedTools != "" {
			sb.WriteString(fmt.Sprintf("allowed-tools: %s\n", manifest.AllowedTools))
		}
	}

	sb.WriteString("---\n\n")
	sb.WriteString(skill.Instructions)
	return sb.String()
}
