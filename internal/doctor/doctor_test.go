package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"tofi-core/internal/paths"
)

func TestCheckDirectories_EmptyHome(t *testing.T) {
	tmp := t.TempDir()
	nonExistent := filepath.Join(tmp, "nope")

	results := CheckDirectories(nonExistent)
	if len(results) != 1 {
		t.Fatalf("expected 1 result (fail), got %d", len(results))
	}
	if results[0].Severity != SeverityFail {
		t.Errorf("expected SeverityFail, got %v", results[0].Severity)
	}
}

func TestCheckDirectories_ExistingHome(t *testing.T) {
	tmp := t.TempDir()
	// Create home dir only, no subdirs
	results := CheckDirectories(tmp)

	// Should have: 1 OK (home) + 4 warnings (users, skills, packages, logs)
	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}
	if results[0].Severity != SeverityOK {
		t.Errorf("home dir should be OK")
	}
	for _, r := range results[1:] {
		if r.Severity != SeverityWarn {
			t.Errorf("expected Warn for %s, got %v", r.Label, r.Severity)
		}
		if !r.Fixable {
			t.Errorf("expected Fixable for %s", r.Label)
		}
	}
}

func TestCheckDirectories_FixCreatesDir(t *testing.T) {
	tmp := t.TempDir()
	results := CheckDirectories(tmp)

	// Fix all fixable items
	report := &Report{Results: results}
	fixResults := Fix(report)

	if len(fixResults) != 4 {
		t.Fatalf("expected 4 fix results, got %d", len(fixResults))
	}
	for _, fr := range fixResults {
		if !fr.Fixed {
			t.Errorf("expected %s to be fixed, got error: %s", fr.Label, fr.Error)
		}
	}

	// Verify dirs were created
	for _, dir := range []string{"users", "skills", "packages", "logs"} {
		p := filepath.Join(tmp, dir)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			t.Errorf("expected %s to exist after fix", dir)
		}
	}
}

func TestCheckConfig_MissingFile(t *testing.T) {
	tmp := t.TempDir()
	results := CheckConfig(tmp)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Severity != SeverityFail {
		t.Errorf("expected Fail for missing config, got %v", results[0].Severity)
	}
}

func TestCheckConfig_ValidConfig(t *testing.T) {
	tmp := t.TempDir()
	configContent := `
port: 8321
provider: openai
api_key: sk-test1234567890abcdefghijklmnop
auth_mode: token
jwt_secret: abcdefghijklmnopqrstuvwxyz123456
`
	os.WriteFile(filepath.Join(tmp, "config.yaml"), []byte(configContent), 0644)

	results := CheckConfig(tmp)

	// Should have multiple OK results
	failCount := 0
	for _, r := range results {
		if r.Severity == SeverityFail {
			failCount++
			t.Errorf("unexpected Fail: %s — %s", r.Label, r.Detail)
		}
	}
	if failCount > 0 {
		t.Errorf("expected no failures for valid config")
	}
}

func TestCheckConfig_InvalidYAML(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "config.yaml"), []byte("not: [valid: yaml"), 0644)

	results := CheckConfig(tmp)

	// Should report YAML syntax error
	hasSyntaxFail := false
	for _, r := range results {
		if r.Label == "YAML syntax" && r.Severity == SeverityFail {
			hasSyntaxFail = true
		}
	}
	if !hasSyntaxFail {
		t.Error("expected YAML syntax failure")
	}
}

func TestMaskString(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		{"short", "****"},
		{"12345678", "****"},
		{"123456789", "1234...6789"},
		{"sk-ant-api03-abcdefg", "sk-a...defg"},
	}
	for _, tt := range tests {
		got := maskString(tt.input)
		if got != tt.expected {
			t.Errorf("maskString(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestValidateKeyFormat(t *testing.T) {
	tests := []struct {
		provider, key string
		wantWarn      bool
	}{
		{"anthropic", "sk-ant-api03-abc", false},
		{"anthropic", "sk-wrong", true},
		{"openai", "sk-abc123", false},
		{"openai", "wrong-key", true},
		{"gemini", "whatever", false}, // no format check for gemini
	}
	for _, tt := range tests {
		got := validateKeyFormat(tt.provider, tt.key)
		if (got != "") != tt.wantWarn {
			t.Errorf("validateKeyFormat(%q, %q) = %q, wantWarn=%v", tt.provider, tt.key, got, tt.wantWarn)
		}
	}
}

func TestRun_CriticalOnly(t *testing.T) {
	tmp := t.TempDir()
	paths.SetTofiHome(tmp)

	// Create minimal valid setup
	os.MkdirAll(filepath.Join(tmp, "users"), 0755)
	os.MkdirAll(filepath.Join(tmp, "skills"), 0755)
	os.MkdirAll(filepath.Join(tmp, "packages"), 0755)
	os.MkdirAll(filepath.Join(tmp, "logs"), 0755)
	os.WriteFile(filepath.Join(tmp, "config.yaml"), []byte(`
provider: openai
api_key: sk-test1234567890abcdefghijklmnop
auth_mode: token
jwt_secret: abcdefghijklmnopqrstuvwxyz123456
`), 0644)

	report := Run(Options{
		HomeDir:      tmp,
		CriticalOnly: true,
	})

	// CriticalOnly should not include Python Deps, System Skills, or Database
	for _, r := range report.Results {
		if r.Category == catPythonDeps || r.Category == catSystemSkills || r.Category == catDatabase {
			t.Errorf("CriticalOnly should not include %s checks", r.Category)
		}
	}
}

func TestRun_Full(t *testing.T) {
	tmp := t.TempDir()
	paths.SetTofiHome(tmp)

	// Create home dir
	os.MkdirAll(tmp, 0755)
	os.WriteFile(filepath.Join(tmp, "config.yaml"), []byte(`
provider: openai
api_key: sk-test1234567890abcdefghijklmnop
auth_mode: token
jwt_secret: abcdefghijklmnopqrstuvwxyz123456
`), 0644)

	report := Run(Options{HomeDir: tmp})

	// Should include all categories
	categories := make(map[string]bool)
	for _, r := range report.Results {
		categories[r.Category] = true
	}

	for _, expected := range []string{catDirectories, catConfig, catEnvironment} {
		if !categories[expected] {
			t.Errorf("expected category %q in full run", expected)
		}
	}
}

func TestFix_NilFixFunc(t *testing.T) {
	report := &Report{
		Results: []CheckResult{
			{Label: "test", Fixable: true, fixFunc: nil},
			{Label: "test2", Fixable: false},
		},
	}
	results := Fix(report)
	if len(results) != 0 {
		t.Errorf("expected 0 fix results for nil fixFunc, got %d", len(results))
	}
}

func TestFix_FixFuncError(t *testing.T) {
	report := &Report{
		Results: []CheckResult{
			{
				Label:   "test",
				Fixable: true,
				fixFunc: func() error { return os.ErrPermission },
			},
		},
	}
	results := Fix(report)
	if len(results) != 1 {
		t.Fatalf("expected 1 fix result, got %d", len(results))
	}
	if results[0].Fixed {
		t.Error("expected Fixed=false for error")
	}
	if results[0].Error == "" {
		t.Error("expected error message")
	}
}

func TestCheckDatabase_NoFile(t *testing.T) {
	tmp := t.TempDir()
	results := CheckDatabase(tmp)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Severity != SeverityInfo {
		t.Errorf("expected Info for missing DB (first run), got %v", results[0].Severity)
	}
}
