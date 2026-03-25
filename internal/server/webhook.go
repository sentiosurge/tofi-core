package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"tofi-core/internal/engine"
	"tofi-core/internal/models"
	"tofi-core/internal/parser"

	"github.com/google/uuid"
)

// --- Webhook Trigger (Public endpoint, no auth required) ---

func (s *Server) handleWebhookTrigger(w http.ResponseWriter, r *http.Request) {
	// Extract token from URL: /api/v1/hooks/{token}
	token := r.PathValue("token")
	if token == "" {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "Missing webhook token", "")
		return
	}

	// Look up webhook by token
	webhook, err := s.db.GetWebhookByToken(token)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, ErrNotFound, "Invalid or inactive webhook", "")
		return
	}

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "Failed to read request body", "")
		return
	}
	defer r.Body.Close()

	// Optional: Verify HMAC signature if secret is configured
	if webhook.Secret != "" {
		signature := r.Header.Get("X-Tofi-Signature")
		if signature == "" {
			signature = r.Header.Get("X-Hub-Signature-256") // GitHub compat
		}
		if !verifyHMAC(body, signature, webhook.Secret) {
			writeJSONError(w, http.StatusUnauthorized, ErrUnauthorized, "Invalid signature", "")
			return
		}
	}

	// Parse payload as JSON (or use raw body)
	var payload map[string]interface{}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			// Not JSON — wrap raw body as string
			payload = map[string]interface{}{
				"body": string(body),
			}
		}
	} else {
		payload = make(map[string]interface{})
	}

	// Add request metadata
	payload["_webhook"] = map[string]interface{}{
		"method":     r.Method,
		"headers":    flattenHeaders(r.Header),
		"query":      flattenQuery(r.URL.Query()),
		"remote_addr": r.RemoteAddr,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
	}

	// Load the workflow
	userWorkflowDir := filepath.Join(s.config.HomeDir, webhook.UserID, "workflows")
	wf, err := parser.ResolveWorkflow(webhook.WorkflowID, userWorkflowDir)
	if err != nil {
		// Try system directory fallback
		wf, err = parser.ResolveWorkflow(webhook.WorkflowID, "workflows")
		if err != nil {
			log.Printf("❌ Webhook %s: failed to resolve workflow '%s': %v", webhook.Token, webhook.WorkflowID, err)
			writeJSONError(w, http.StatusInternalServerError, ErrInternal, "Failed to load workflow", "")
			return
		}
	}

	if wf.ID == "" {
		wf.ID = webhook.WorkflowID
	}

	// Validate
	if err := engine.ValidateAll(wf); err != nil {
		log.Printf("❌ Webhook %s: workflow validation failed: %v", webhook.Token, err)
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, fmt.Sprintf("Workflow validation failed: %v", err), "")
		return
	}

	// Create execution
	uuidStr := uuid.New().String()[:4]
	execID := time.Now().Format("102150405") + "-wh-" + uuidStr

	ctx := models.NewExecutionContext(execID, webhook.UserID, s.config.HomeDir)
	ctx.SetWorkflowName(wf.Name)
	ctx.WorkflowID = wf.ID
	ctx.DB = s.db

	// Inject webhook payload as initial inputs under "data" key
	initialInputs := map[string]interface{}{
		"data": payload,
	}

	job := &WorkflowJob{
		ExecutionID:   execID,
		Workflow:      wf,
		Context:       ctx,
		InitialInputs: initialInputs,
		DB:            s.db,
	}

	if err := s.workerPool.Submit(job); err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, ErrInternal, "Server busy, try again later", "")
		return
	}

	log.Printf("🔗 Webhook triggered: workflow=%s exec=%s", webhook.WorkflowID, execID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{
		"execution_id": execID,
		"status":       "queued",
		"message":      "Workflow triggered via webhook",
	})
}

// --- Webhook Management (Protected endpoints) ---

func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)

	var req struct {
		WorkflowID  string `json:"workflow_id"`
		Secret      string `json:"secret,omitempty"`
		Description string `json:"description,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "Invalid request body", "")
		return
	}
	if req.WorkflowID == "" {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "workflow_id is required", "")
		return
	}

	id := uuid.New().String()
	token := generateWebhookToken()

	if err := s.db.CreateWebhook(id, user, req.WorkflowID, token, req.Secret, req.Description); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, fmt.Sprintf("Failed to create webhook: %v", err), "")
		return
	}

	// Build the full webhook URL
	webhookURL := fmt.Sprintf("/api/v1/hooks/%s", token)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"id":          id,
		"token":       token,
		"webhook_url": webhookURL,
		"workflow_id": req.WorkflowID,
	})
}

func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)
	workflowID := r.URL.Query().Get("workflow_id")

	webhooks, err := s.db.ListWebhooks(user, workflowID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "Failed to list webhooks", "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(webhooks)
}

func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	if err := s.db.DeleteWebhook(id, user); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "Failed to delete webhook", "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleToggleWebhook(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(UserContextKey).(string)
	id := r.PathValue("id")

	var req struct {
		Active bool `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "Invalid request body", "")
		return
	}

	if err := s.db.ToggleWebhook(id, user, req.Active); err != nil {
		writeJSONError(w, http.StatusInternalServerError, ErrInternal, "Failed to toggle webhook", "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":     id,
		"active": req.Active,
	})
}

// --- Helper Functions ---

func generateWebhookToken() string {
	// Generate a URL-safe token: 16 bytes = 32 hex chars
	id := uuid.New()
	return strings.ReplaceAll(id.String(), "-", "")
}

func verifyHMAC(body []byte, signature, secret string) bool {
	if signature == "" {
		return false
	}
	// Remove "sha256=" prefix if present (GitHub format)
	signature = strings.TrimPrefix(signature, "sha256=")

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

func flattenHeaders(h http.Header) map[string]string {
	result := make(map[string]string)
	for k, v := range h {
		if len(v) > 0 {
			result[k] = v[0]
		}
	}
	return result
}

func flattenQuery(q map[string][]string) map[string]string {
	result := make(map[string]string)
	for k, v := range q {
		if len(v) > 0 {
			result[k] = v[0]
		}
	}
	return result
}
