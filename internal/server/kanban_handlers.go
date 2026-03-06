package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"tofi-core/internal/skills"
	"tofi-core/internal/storage"

	"github.com/google/uuid"
)

// handleRetryCard POST /api/v1/kanban/{id}/retry — 重试失败的卡片
func (s *Server) handleRetryCard(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	cardID := r.PathValue("id")

	// 获取原卡片
	original, err := s.db.GetKanbanCard(cardID)
	if err != nil || original.UserID != userID {
		http.Error(w, "card not found", http.StatusNotFound)
		return
	}

	if original.Status != "failed" {
		http.Error(w, "only failed cards can be retried", http.StatusConflict)
		return
	}

	// 创建新卡片（复制 title + description）
	newCard := &storage.KanbanCardRecord{
		ID:          uuid.New().String(),
		Title:       original.Title,
		Description: original.Description,
		Status:      "todo",
		UserID:      userID,
	}

	if err := s.db.CreateKanbanCard(newCard); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	created, _ := s.db.GetKanbanCard(newCard.ID)
	if created == nil {
		created = newCard
	}

	// 异步执行
	go s.executeWish(created, userID, "")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(created)
}

// --- Kanban API Handlers ---

// handleCreateKanbanCard POST /api/v1/kanban — 创建看板卡片
func (s *Server) handleCreateKanbanCard(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	var req struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}

	card := &storage.KanbanCardRecord{
		ID:          uuid.New().String(),
		Title:       req.Title,
		Description: req.Description,
		Status:      "todo",
		UserID:      userID,
	}

	if err := s.db.CreateKanbanCard(card); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 重新获取完整的卡片（含 created_at, updated_at）
	created, err := s.db.GetKanbanCard(card.ID)
	if err != nil {
		// 创建成功但获取失败，仍然返回 201
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(card)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(created)
}

// handleListKanbanCards GET /api/v1/kanban — 列出用户的所有卡片
func (s *Server) handleListKanbanCards(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	cards, err := s.db.ListKanbanCards(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if cards == nil {
		cards = []*storage.KanbanCardRecord{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cards)
}

// handleGetKanbanCard GET /api/v1/kanban/{id} — 获取单张卡片
func (s *Server) handleGetKanbanCard(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "card id required", http.StatusBadRequest)
		return
	}

	card, err := s.db.GetKanbanCard(id)
	if err != nil {
		http.Error(w, "card not found", http.StatusNotFound)
		return
	}

	// 权限检查：只能查看自己的卡片
	if card.UserID != userID {
		http.Error(w, "card not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(card)
}

// handleUpdateKanbanCard PUT /api/v1/kanban/{id} — 更新卡片
func (s *Server) handleUpdateKanbanCard(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "card id required", http.StatusBadRequest)
		return
	}

	// 先获取现有卡片
	existing, err := s.db.GetKanbanCard(id)
	if err != nil {
		http.Error(w, "card not found", http.StatusNotFound)
		return
	}

	// 权限检查
	if existing.UserID != userID {
		http.Error(w, "card not found", http.StatusNotFound)
		return
	}

	// 解析请求体
	var req struct {
		Title       *string `json:"title"`
		Description *string `json:"description"`
		Status      *string `json:"status"`
		AgentID     *string `json:"agent_id"`
		ExecutionID *string `json:"execution_id"`
		Progress    *int    `json:"progress"`
		Result      *string `json:"result"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// 合并：仅更新请求中提供的字段
	if req.Title != nil {
		existing.Title = *req.Title
	}
	if req.Description != nil {
		existing.Description = *req.Description
	}
	if req.Status != nil {
		// 验证状态值
		valid := map[string]bool{"todo": true, "working": true, "hold": true, "done": true, "failed": true}
		if !valid[*req.Status] {
			http.Error(w, "invalid status (must be todo/working/hold/done/failed)", http.StatusBadRequest)
			return
		}
		existing.Status = *req.Status
	}
	if req.AgentID != nil {
		existing.AgentID = *req.AgentID
	}
	if req.ExecutionID != nil {
		existing.ExecutionID = *req.ExecutionID
	}
	if req.Progress != nil {
		if *req.Progress < 0 || *req.Progress > 100 {
			http.Error(w, "progress must be 0-100", http.StatusBadRequest)
			return
		}
		existing.Progress = *req.Progress
	}
	if req.Result != nil {
		existing.Result = *req.Result
	}

	if err := s.db.UpdateKanbanCard(existing); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 重新获取更新后的卡片
	updated, err := s.db.GetKanbanCard(id)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(existing)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}

// handleDeleteKanbanCard DELETE /api/v1/kanban/{id} — 删除卡片
func (s *Server) handleDeleteKanbanCard(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "card id required", http.StatusBadRequest)
		return
	}

	// Release any hold channel before deleting
	s.signalHold(id, "abort")

	if err := s.db.DeleteKanbanCard(id, userID); err != nil {
		http.Error(w, "card not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleApproveAction POST /api/v1/kanban/{id}/actions/{index}/approve — 审批安装 Skill
func (s *Server) handleApproveAction(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	cardID := r.PathValue("id")
	indexStr := r.PathValue("index")

	if cardID == "" || indexStr == "" {
		http.Error(w, "card id and action index required", http.StatusBadRequest)
		return
	}

	index, err := strconv.Atoi(indexStr)
	if err != nil {
		http.Error(w, "invalid action index", http.StatusBadRequest)
		return
	}

	// 1. 获取卡片并检查权限
	card, err := s.db.GetKanbanCard(cardID)
	if err != nil {
		http.Error(w, "card not found", http.StatusNotFound)
		return
	}
	if card.UserID != userID {
		http.Error(w, "card not found", http.StatusNotFound)
		return
	}

	// 2. 获取 actions
	actions, err := s.db.GetKanbanActions(cardID)
	if err != nil {
		http.Error(w, "failed to read actions", http.StatusInternalServerError)
		return
	}
	if index < 0 || index >= len(actions) {
		http.Error(w, "action index out of range", http.StatusBadRequest)
		return
	}

	action := actions[index]

	// 3. 检查状态
	if action.Status == "installed" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "already_installed"})
		return
	}
	if action.Status != "pending" && action.Status != "failed" {
		http.Error(w, fmt.Sprintf("action is %s, cannot approve", action.Status), http.StatusConflict)
		return
	}

	// 4. 更新状态为 installing
	s.db.UpdateKanbanAction(cardID, index, "installing", "")
	log.Printf("📦 [kanban:%s] Installing skill: %s", cardID[:8], action.SkillID)

	// 5. 异步安装
	go func() {
		localStore := skills.NewLocalStore(s.config.HomeDir)
		installer := skills.NewSkillInstaller(localStore)

		result, err := installer.Install(action.SkillID)
		if err != nil {
			log.Printf("❌ [kanban:%s] Skill install failed: %v", cardID[:8], err)
			s.db.UpdateKanbanAction(cardID, index, "failed", err.Error())
			return
		}

		// 保存到数据库（复用 buildSkillRecord）
		for _, sf := range result.Skills {
			record := s.buildSkillRecord("system", sf, string(result.Source.Type), result.Source.DisplayURL(), "public")
			if err := s.db.SaveSkill(record); err != nil {
				log.Printf("[kanban:%s] warning: save skill %s failed: %v", cardID[:8], sf.Manifest.Name, err)
			}
		}

		log.Printf("✅ [kanban:%s] Skill installed: %s (%d skills)", cardID[:8], action.SkillID, len(result.Skills))
		s.db.UpdateKanbanAction(cardID, index, "installed", "")
	}()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "installing"})
}

// handleContinueCard POST /api/v1/kanban/{id}/continue — 用户安装完 skill 后继续 agent 执行
func (s *Server) handleContinueCard(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	cardID := r.PathValue("id")

	card, err := s.db.GetKanbanCard(cardID)
	if err != nil || card.UserID != userID {
		http.Error(w, "card not found", http.StatusNotFound)
		return
	}

	if card.Status != "hold" {
		http.Error(w, "card is not in hold state", http.StatusConflict)
		return
	}

	if !s.signalHold(cardID, "continue") {
		http.Error(w, "no active hold found for this card", http.StatusConflict)
		return
	}

	log.Printf("▶ [kanban:%s] User clicked Continue", cardID[:8])
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "continued"})
}

// handleAbortCard POST /api/v1/kanban/{id}/abort — 用户跳过 skill 安装，恢复 agent
func (s *Server) handleAbortCard(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	cardID := r.PathValue("id")

	card, err := s.db.GetKanbanCard(cardID)
	if err != nil || card.UserID != userID {
		http.Error(w, "card not found", http.StatusNotFound)
		return
	}

	if card.Status != "hold" {
		http.Error(w, "card is not in hold state", http.StatusConflict)
		return
	}

	// Mark the latest pending action as aborted
	actions, _ := s.db.GetKanbanActions(cardID)
	for i := len(actions) - 1; i >= 0; i-- {
		if actions[i].Status == "pending" {
			s.db.UpdateKanbanAction(cardID, i, "aborted", "User skipped")
			break
		}
	}

	if !s.signalHold(cardID, "abort") {
		http.Error(w, "no active hold found for this card", http.StatusConflict)
		return
	}

	log.Printf("⏭ [kanban:%s] User clicked Skip", cardID[:8])
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "aborted"})
}
