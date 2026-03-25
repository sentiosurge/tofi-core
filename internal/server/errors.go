package server

import (
	"encoding/json"
	"log"
	"net/http"
)

// Standard error codes — English, stable, localization-ready.
// These codes are part of the public API contract. Do NOT rename without versioning.
const (
	ErrBadRequest        = "BAD_REQUEST"
	ErrUnauthorized      = "UNAUTHORIZED"
	ErrForbidden         = "FORBIDDEN"
	ErrNotFound          = "NOT_FOUND"
	ErrConflict          = "CONFLICT"
	ErrInternal          = "INTERNAL_ERROR"
	ErrInvalidCredentials = "INVALID_CREDENTIALS"
	ErrNoAIKey           = "NO_AI_KEY"
	ErrNoAIKeyForModel   = "NO_AI_KEY_FOR_MODEL"
	ErrUserKeysDisabled  = "USER_KEYS_DISABLED"
	ErrSessionNotFound   = "SESSION_NOT_FOUND"
	ErrAppNotFound       = "APP_NOT_FOUND"
	ErrSkillNotFound     = "SKILL_NOT_FOUND"
)

// apiKeyError is a structured error returned by resolveModelAndKey.
// It carries error code, user-friendly message, and actionable hint,
// allowing callers to produce structured error responses.
type apiKeyError struct {
	Code     string
	Message  string
	Hint     string
	Provider string // optional: which provider was missing
}

func (e *apiKeyError) Error() string {
	return e.Message
}

// apiError is the JSON structure for all error responses.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// writeJSONError writes a structured JSON error response.
//
// If code is INTERNAL_ERROR, the message is replaced with a generic string
// to prevent leaking internal details. The original message is logged instead.
// Pass hint="" to omit the hint field from the JSON output.
//
//	Response format:
//	{
//	  "error": {
//	    "code": "NO_AI_KEY",
//	    "message": "No API key configured",
//	    "hint": "PUT /api/v1/user/settings/ai-key ..."   // omitted when empty
//	  }
//	}
func writeJSONError(w http.ResponseWriter, status int, code, message, hint string) {
	if code == ErrInternal {
		log.Printf("Internal error: %s", message)
		message = "An internal error occurred"
		hint = ""
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]apiError{
		"error": {Code: code, Message: message, Hint: hint},
	})
}
