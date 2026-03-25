package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"tofi-core/internal/storage"
)

// handleCreateAPIKey creates a new API key for the authenticated user.
// POST /api/v1/user/api-keys
func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	var req struct {
		Name          string `json:"name"`
		ExpiresInDays *int   `json:"expires_in_days,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "Invalid request body", "")
		return
	}
	if req.Name == "" {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "Name is required", "")
		return
	}

	// Generate key: tofi-sk-{32 hex chars}
	tokenBody, keyHash := storage.GenerateSecureToken(16) // 16 bytes = 32 hex chars
	fullKey := "tofi-sk-" + tokenBody
	prefix := fullKey[:16] // "tofi-sk-" + first 8 hex chars

	id := uuid.New().String()

	var expiresAt *time.Time
	if req.ExpiresInDays != nil && *req.ExpiresInDays > 0 {
		t := time.Now().Add(time.Duration(*req.ExpiresInDays) * 24 * time.Hour)
		expiresAt = &t
	}

	if err := s.db.CreateAPIKey(id, userID, prefix, keyHash, req.Name, expiresAt); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, err.Error(), "")
		return
	}

	// Return full key only once
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":         id,
		"key":        fullKey,
		"key_prefix": prefix,
		"name":       req.Name,
		"expires_at": expiresAt,
		"hint":       "Save this key now. You won't be able to see it again.",
	})
}

// handleListAPIKeys lists all API keys for the authenticated user.
// GET /api/v1/user/api-keys
func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	keys, err := s.db.ListAPIKeys(userID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, err.Error(), "")
		return
	}
	if keys == nil {
		keys = []storage.APIKeyRecord{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"api_keys": keys,
	})
}

// handleDeleteAPIKey revokes an API key.
// DELETE /api/v1/user/api-keys/{id}
func (s *Server) handleDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	keyID := r.PathValue("id")

	if err := s.db.DeleteAPIKey(keyID, userID); err != nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "API key not found", "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "deleted",
	})
}
