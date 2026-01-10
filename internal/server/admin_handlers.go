package server

import (
	"encoding/json"
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Username == "" || req.Password == "" {
		http.Error(w, "Username and password are required", http.StatusBadRequest)
		return
	}

	// 默认角色为 user
	if req.Role != "admin" && req.Role != "user" {
		req.Role = "user"
	}

	// 检查用户名是否已存在
	existing, _ := s.db.GetUser(req.Username)
	if existing != nil {
		http.Error(w, "Username already exists", http.StatusConflict)
		return
	}

	// 密码哈希
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "Failed to hash password", http.StatusInternalServerError)
		return
	}

	id := uuid.New().String()
	if err := s.db.SaveUser(id, req.Username, string(hash), req.Role); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, "User ID is required", http.StatusBadRequest)
		return
	}

	if err := s.db.DeleteUser(id); err != nil {
		http.Error(w, "User not found", http.StatusNotFound)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(records)
}

// handleAdminGetStats 返回系统统计
func (s *Server) handleAdminGetStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.db.GetSystemStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, "Secret ID is required", http.StatusBadRequest)
		return
	}

	if err := s.db.DeleteSecretByID(id); err != nil {
		http.Error(w, "Secret not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
