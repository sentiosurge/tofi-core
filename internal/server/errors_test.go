package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteJSONError_Normal(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSONError(w, http.StatusBadRequest, ErrNoAIKey, "No API key configured", "PUT /api/v1/user/settings/ai-key")

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", ct)
	}

	var resp map[string]apiError
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	errObj := resp["error"]
	if errObj.Code != ErrNoAIKey {
		t.Errorf("expected code %s, got %s", ErrNoAIKey, errObj.Code)
	}
	if errObj.Message != "No API key configured" {
		t.Errorf("expected message 'No API key configured', got %s", errObj.Message)
	}
	if errObj.Hint != "PUT /api/v1/user/settings/ai-key" {
		t.Errorf("expected hint, got %s", errObj.Hint)
	}
}

func TestWriteJSONError_InternalErrorProtection(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSONError(w, http.StatusInternalServerError, ErrInternal, "database connection failed: dial tcp 127.0.0.1:5432", "")

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", w.Code)
	}

	var resp map[string]apiError
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	errObj := resp["error"]
	if errObj.Code != ErrInternal {
		t.Errorf("expected code %s, got %s", ErrInternal, errObj.Code)
	}
	// Must NOT contain the real error message
	if errObj.Message != "An internal error occurred" {
		t.Errorf("INTERNAL_ERROR should use generic message, got: %s", errObj.Message)
	}
	if errObj.Hint != "" {
		t.Errorf("INTERNAL_ERROR should have no hint, got: %s", errObj.Hint)
	}
}

func TestWriteJSONError_OmitEmptyHint(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSONError(w, http.StatusUnauthorized, ErrUnauthorized, "Missing Authorization header", "")

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	var errMap map[string]json.RawMessage
	if err := json.Unmarshal(raw["error"], &errMap); err != nil {
		t.Fatalf("failed to parse error object: %v", err)
	}

	if _, exists := errMap["hint"]; exists {
		t.Error("hint field should be omitted when empty")
	}
}
