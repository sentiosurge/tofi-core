package server

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// --- Admin Request/Response Structs ---

type CreateUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"` // admin or user
}

type UserResponse struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
}

type WorkflowInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	User        string `json:"user"`
	Description string `json:"description,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// --- Admin Handlers ---

// handleAdminListUsers 返回所有用户列表
func (s *Server) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.db.ListAllUsers()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, err.Error(), "")
		return
	}

	resp := []UserResponse{}
	for _, u := range users {
		resp = append(resp, UserResponse{
			ID:        u.ID,
			Username:  u.Username,
			Role:      u.Role,
			CreatedAt: u.CreatedAt.String,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleAdminCreateUser 创建新用户
func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	var req CreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "Invalid request body", "")
		return
	}

	if req.Username == "" || req.Password == "" {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "Username and password are required", "")
		return
	}

	// 默认角色为 user
	if req.Role != "admin" && req.Role != "user" {
		req.Role = "user"
	}

	// 检查用户名是否已存在
	existing, _ := s.db.GetUser(req.Username)
	if existing != nil {
		writeJSONError(w, http.StatusConflict, ErrConflict, "Username already exists", "")
		return
	}

	// 密码哈希
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "Failed to hash password", "")
		return
	}

	id := uuid.New().String()
	if err := s.db.SaveUser(id, req.Username, string(hash), req.Role); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, err.Error(), "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"id":      id,
		"message": "User created successfully",
	})
}

// handleAdminDeleteUser 删除用户
func (s *Server) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "User ID is required", "")
		return
	}

	// Cannot delete self
	currentUser := r.Context().Value(UserContextKey).(string)
	targetUser, err := s.db.GetUserByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "User not found", "")
		return
	}
	if targetUser.Username == currentUser {
		writeJSONError(w, http.StatusForbidden, ErrForbidden, "Cannot delete your own account", "")
		return
	}

	// Cannot delete last admin
	if targetUser.Role == "admin" {
		users, _ := s.db.ListAllUsers()
		adminCount := 0
		for _, u := range users {
			if u.Role == "admin" {
				adminCount++
			}
		}
		if adminCount <= 1 {
			writeJSONError(w, http.StatusForbidden, ErrForbidden, "Cannot delete the last admin account", "")
			return
		}
	}

	if err := s.db.DeleteUser(id); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "Failed to delete user", "")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleAdminListExecutions 返回所有用户的执行记录
func (s *Server) handleAdminListExecutions(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	userFilter := r.URL.Query().Get("user")

	var records interface{}
	var err error

	if userFilter != "" {
		records, err = s.db.ListExecutions(userFilter, limit, offset)
	} else {
		records, err = s.db.ListAllExecutions(limit, offset)
	}

	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, err.Error(), "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(records)
}

// handleAdminGetStats 返回系统统计
func (s *Server) handleAdminGetStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.db.GetSystemStats()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, err.Error(), "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// handleAdminListWorkflows 返回所有用户的工作流
func (s *Server) handleAdminListWorkflows(w http.ResponseWriter, r *http.Request) {
	userFilter := r.URL.Query().Get("user")

	var workflows []WorkflowInfo

	// 获取所有用户
	users, err := s.db.ListAllUsers()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, err.Error(), "")
		return
	}

	for _, user := range users {
		// 如果指定了用户筛选，只处理该用户
		if userFilter != "" && user.Username != userFilter {
			continue
		}

		// 遍历用户的工作流目录
		userWorkflowDir := filepath.Join(s.config.HomeDir, user.Username, "workflows")
		if _, err := os.Stat(userWorkflowDir); os.IsNotExist(err) {
			continue
		}

		filepath.WalkDir(userWorkflowDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
				return nil
			}

			// 获取文件信息
			info, _ := d.Info()
			name := strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))

			workflows = append(workflows, WorkflowInfo{
				ID:        name,
				Name:      name,
				User:      user.Username,
				UpdatedAt: info.ModTime().Format("2006-01-02T15:04:05Z"),
			})
			return nil
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(workflows)
}

// handleAdminGetUsage returns usage statistics grouped by model
func (s *Server) handleAdminGetUsage(w http.ResponseWriter, r *http.Request) {
	month := r.URL.Query().Get("month")   // e.g., "2026-03"
	userID := r.URL.Query().Get("user_id") // optional

	var startDate, endDate string
	if month != "" {
		// Parse "YYYY-MM" into date range
		startDate = month + "-01"
		// Calculate next month
		parts := strings.SplitN(month, "-", 2)
		if len(parts) == 2 {
			year, _ := strconv.Atoi(parts[0])
			mon, _ := strconv.Atoi(parts[1])
			mon++
			if mon > 12 {
				mon = 1
				year++
			}
			endDate = fmt.Sprintf("%04d-%02d-01", year, mon)
		}
	}

	usage, err := s.db.GetUsageByModel(userID, startDate, endDate)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, err.Error(), "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(usage)
}

// --- Admin Secrets Handlers ---

type SecretInfo struct {
	ID        string `json:"id"`
	User      string `json:"user"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// handleAdminListSecrets 返回所有用户的 secrets（仅元数据，不含加密值）
func (s *Server) handleAdminListSecrets(w http.ResponseWriter, r *http.Request) {
	secrets, err := s.db.ListAllSecrets()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, err.Error(), "")
		return
	}

	resp := []SecretInfo{}
	for _, sec := range secrets {
		resp = append(resp, SecretInfo{
			ID:        sec.ID,
			User:      sec.User,
			Name:      sec.Name,
			CreatedAt: sec.CreatedAt.String,
			UpdatedAt: sec.UpdatedAt.String,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleAdminDeleteSecret 删除指定 ID 的 secret
func (s *Server) handleAdminDeleteSecret(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "Secret ID is required", "")
		return
	}

	if err := s.db.DeleteSecretByID(id); err != nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "Secret not found", "")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
