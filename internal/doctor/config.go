package doctor

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const catConfig = "Configuration"

// tofiConfig represents the full config.yaml structure for validation.
type tofiConfig struct {
	Port              int    `yaml:"port"`
	Provider          string `yaml:"provider"`
	APIKey            string `yaml:"api_key"`
	AuthMode          string `yaml:"auth_mode"`
	AccessToken       string `yaml:"access_token"`
	AdminUsername     string `yaml:"admin_username"`
	AdminPasswordHash string `yaml:"admin_password_hash"`
	JWTSecret         string `yaml:"jwt_secret"`
}

// CheckConfig validates config.yaml.
func CheckConfig(homeDir string) []CheckResult {
	var results []CheckResult
	configPath := filepath.Join(homeDir, "config.yaml")

	// File exists?
	data, err := os.ReadFile(configPath)
	if err != nil {
		results = append(results, newFail(catConfig, "config.yaml", "cannot read — run tofi init"))
		return results
	}
	results = append(results, newOK(catConfig, "config.yaml", "readable"))

	// Valid YAML?
	var cfg tofiConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		results = append(results, newFail(catConfig, "YAML syntax", err.Error()))
		return results
	}

	// Auth mode
	switch cfg.AuthMode {
	case "token", "password":
		results = append(results, newOK(catConfig, "Auth mode", cfg.AuthMode))
	case "":
		results = append(results, newWarn(catConfig, "Auth mode", "not set"))
	default:
		results = append(results, newWarn(catConfig, "Auth mode", "unknown: "+cfg.AuthMode))
	}

	// JWT secret
	if cfg.JWTSecret == "" {
		results = append(results, newWarn(catConfig, "JWT secret", "not set — internal auth may fail"))
	} else if len(cfg.JWTSecret) < 32 {
		results = append(results, newWarn(catConfig, "JWT secret", "too short (< 32 chars)"))
	} else {
		results = append(results, newOK(catConfig, "JWT secret", "set ("+maskString(cfg.JWTSecret)+")"))
	}

	// Provider
	if cfg.Provider != "" {
		results = append(results, newOK(catConfig, "Provider", cfg.Provider))
	} else {
		results = append(results, newWarn(catConfig, "Provider", "not set"))
	}

	// API Key — check config then env vars
	results = append(results, checkAPIKey(cfg)...)

	return results
}

func checkAPIKey(cfg tofiConfig) []CheckResult {
	if cfg.APIKey != "" {
		detail := maskString(cfg.APIKey)
		if warning := validateKeyFormat(cfg.Provider, cfg.APIKey); warning != "" {
			return []CheckResult{newWarn(catConfig, "API Key", detail+" — "+warning)}
		}
		return []CheckResult{newOK(catConfig, "API Key", detail)}
	}

	// Check environment variables
	envVars := []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY"}
	for _, env := range envVars {
		if v := os.Getenv(env); v != "" {
			return []CheckResult{newOK(catConfig, "API Key", env+" (env) "+maskString(v))}
		}
	}

	return []CheckResult{newFail(catConfig, "API Key", "not configured — run tofi init or set env var")}
}

// validateKeyFormat does a basic format check for known providers.
func validateKeyFormat(provider, key string) string {
	switch provider {
	case "anthropic":
		if !strings.HasPrefix(key, "sk-ant-") {
			return "expected sk-ant-* prefix for Anthropic"
		}
	case "openai":
		if !strings.HasPrefix(key, "sk-") {
			return "expected sk-* prefix for OpenAI"
		}
	}
	return ""
}

// maskString shows first 4 and last 4 chars, masking the middle.
func maskString(s string) string {
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "..." + s[len(s)-4:]
}
