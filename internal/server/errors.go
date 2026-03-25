package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
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
	ErrInvalidAIKey      = "INVALID_AI_KEY"
	ErrProviderError     = "PROVIDER_ERROR"
	ErrAgentError        = "AGENT_ERROR"
	ErrSpendCapExceeded  = "SPEND_CAP_EXCEEDED"
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
// classifyAgentError inspects an agent/LLM error and returns a user-safe
// (message, code, hint) triple. Raw upstream errors are logged, never exposed.
func classifyAgentError(err error) (message, code, hint string) {
	raw := err.Error()
	log.Printf("Agent error (raw): %s", raw)

	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "invalid_api_key") || strings.Contains(lower, "incorrect api key"):
		return "Your AI API key is invalid or expired",
			ErrInvalidAIKey,
			"Check your key and update it: PUT /api/v1/user/settings/ai-key"
	case strings.Contains(lower, "insufficient_quota") || strings.Contains(lower, "quota"):
		return "Your AI provider account has insufficient quota",
			ErrProviderError,
			"Check your billing at your AI provider's dashboard"
	case strings.Contains(lower, "rate_limit") || strings.Contains(lower, "429"):
		return "AI provider rate limit reached, please try again shortly",
			ErrProviderError,
			""
	case strings.Contains(lower, "model_not_found") || strings.Contains(lower, "does not exist"):
		return "The requested model is not available",
			ErrProviderError,
			"Check available models: GET /api/v1/models"
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded"):
		return "AI provider request timed out",
			ErrProviderError,
			"Try again or use a faster model"
	default:
		return "An error occurred while processing your request",
			ErrAgentError,
			""
	}
}

// getUserTimezone extracts user timezone from X-Timezone header or query param.
// Falls back to UTC if not provided.
func getUserTimezone(r *http.Request) string {
	if tz := r.Header.Get("X-Timezone"); tz != "" {
		return tz
	}
	if tz := r.URL.Query().Get("timezone"); tz != "" {
		return tz
	}
	return "UTC"
}

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
