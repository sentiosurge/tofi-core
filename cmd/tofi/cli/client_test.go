package cli

import (
	"testing"
)

func TestParseAPIError_JSONError(t *testing.T) {
	body := []byte(`{"error":{"code":"NO_AI_KEY","message":"No API key configured","hint":"PUT /api/v1/user/settings/ai-key"}}`)
	result := parseAPIError(400, body)

	expected := "No API key configured\n  → PUT /api/v1/user/settings/ai-key"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestParseAPIError_JSONErrorNoHint(t *testing.T) {
	body := []byte(`{"error":{"code":"UNAUTHORIZED","message":"Missing Authorization header"}}`)
	result := parseAPIError(401, body)

	if result != "Missing Authorization header" {
		t.Errorf("expected 'Missing Authorization header', got %q", result)
	}
}

func TestParseAPIError_PlainText(t *testing.T) {
	body := []byte("some old-style error message")
	result := parseAPIError(500, body)

	if result != "some old-style error message" {
		t.Errorf("expected plain text fallback, got %q", result)
	}
}

func TestParseAPIError_EmptyBody(t *testing.T) {
	result := parseAPIError(500, []byte{})

	if result != "API error (500)" {
		t.Errorf("expected 'API error (500)', got %q", result)
	}
}
