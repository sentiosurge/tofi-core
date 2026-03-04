package skills

import (
	"testing"
)

func TestParse_Minimal(t *testing.T) {
	input := []byte(`---
name: explain-code
description: Explains code with visual diagrams and analogies
---

When explaining code, always include:
1. Start with an analogy
2. Draw a diagram
`)

	skill, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if skill.Manifest.Name != "explain-code" {
		t.Errorf("expected name 'explain-code', got %q", skill.Manifest.Name)
	}

	if skill.Manifest.Description != "Explains code with visual diagrams and analogies" {
		t.Errorf("unexpected description: %q", skill.Manifest.Description)
	}

	if skill.Body == "" {
		t.Error("body should not be empty")
	}

	if skill.Body != "When explaining code, always include:\n1. Start with an analogy\n2. Draw a diagram" {
		t.Errorf("unexpected body: %q", skill.Body)
	}
}

func TestParse_FullFields(t *testing.T) {
	input := []byte(`---
name: deploy-app
description: Deploy the application to production environment
license: MIT
compatibility: Requires git, docker, and network access
metadata:
  author: platform-team
  version: "2.0"
  requires_env: "DEPLOY_TOKEN,AWS_KEY"
allowed-tools: Bash(git:*) Bash(npm:*)
argument-hint: "[environment]"
model: claude-sonnet-4-20250514
context: fork
agent: general-purpose
---

Deploy $ARGUMENTS to production:

1. Run the test suite
2. Build the application
3. Push to the deployment target
`)

	skill, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	m := skill.Manifest

	// Standard fields
	if m.Name != "deploy-app" {
		t.Errorf("name = %q, want 'deploy-app'", m.Name)
	}
	if m.License != "MIT" {
		t.Errorf("license = %q, want 'MIT'", m.License)
	}
	if m.Compatibility != "Requires git, docker, and network access" {
		t.Errorf("compatibility = %q", m.Compatibility)
	}

	// Metadata
	if m.Metadata["author"] != "platform-team" {
		t.Errorf("metadata.author = %q", m.Metadata["author"])
	}
	if m.Metadata["version"] != "2.0" {
		t.Errorf("metadata.version = %q", m.Metadata["version"])
	}

	// AllowedTools
	tools := m.AllowedToolsList()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d: %v", len(tools), tools)
	}
	if tools[0] != "Bash(git:*)" || tools[1] != "Bash(npm:*)" {
		t.Errorf("tools = %v", tools)
	}

	// Extension fields
	if m.ArgumentHint != "[environment]" {
		t.Errorf("argument_hint = %q", m.ArgumentHint)
	}
	if m.Model != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q", m.Model)
	}
	if m.Context != "fork" {
		t.Errorf("context = %q", m.Context)
	}
	if m.Agent != "general-purpose" {
		t.Errorf("agent = %q", m.Agent)
	}

	// RequiredEnvVars
	envVars := m.RequiredEnvVars()
	if len(envVars) != 2 {
		t.Fatalf("expected 2 env vars, got %d: %v", len(envVars), envVars)
	}
	if envVars[0] != "DEPLOY_TOKEN" || envVars[1] != "AWS_KEY" {
		t.Errorf("env vars = %v", envVars)
	}
}

func TestParse_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{
			name:    "missing frontmatter",
			input:   "# Just a markdown file",
			wantErr: "must start with '---'",
		},
		{
			name: "missing closing delimiter",
			input: `---
name: test
description: test
`,
			wantErr: "missing closing '---'",
		},
		{
			name: "missing name",
			input: `---
description: test skill
---
body`,
			wantErr: "'name' is required",
		},
		{
			name: "missing description",
			input: `---
name: test
---
body`,
			wantErr: "'description' is required",
		},
		{
			name: "invalid name - uppercase",
			input: `---
name: MySkill
description: test
---
body`,
			wantErr: "lowercase alphanumeric",
		},
		{
			name: "invalid name - leading hyphen",
			input: `---
name: -my-skill
description: test
---
body`,
			wantErr: "lowercase alphanumeric",
		},
		{
			name: "invalid name - consecutive hyphens",
			input: `---
name: my--skill
description: test
---
body`,
			wantErr: "consecutive hyphens",
		},
		{
			name: "name too long",
			input: `---
name: aaaaaaaaaabbbbbbbbbbccccccccccddddddddddeeeeeeeeeefffff1234567890
description: test
---
body`,
			wantErr: "at most 64 characters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.input))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want containing %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestParse_EmptyBody(t *testing.T) {
	input := []byte(`---
name: simple
description: A simple skill with no body
---
`)

	skill, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if skill.Body != "" {
		t.Errorf("expected empty body, got %q", skill.Body)
	}
}

func TestParse_MultilineFrontmatter(t *testing.T) {
	input := []byte(`---
name: complex-skill
description: >
  This is a multi-line description
  that spans several lines using
  YAML folded style
metadata:
  author: test
  tags: "ai,automation"
---

# Instructions

Do the thing.
`)

	skill, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if skill.Manifest.Name != "complex-skill" {
		t.Errorf("name = %q", skill.Manifest.Name)
	}

	// YAML folded style adds a newline at the end
	if skill.Manifest.Description == "" {
		t.Error("description should not be empty")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
