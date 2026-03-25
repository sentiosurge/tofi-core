package server

import (
	"fmt"
	"log"
	"os"
	"strings"

	"tofi-core/internal/provider"
	"tofi-core/internal/storage"
)

// resolveModelAndKey 智能检测可用的 API Key 和模型
// 优先级：用户指定 model > Settings 中有 key 的 provider > 环境变量中有 key 的 provider
func (s *Server) resolveModelAndKey(userID, requestedModel string) (model, apiKey, providerName string, err error) {
	// 1. 用户指定了 model → 根据 model 找 key
	if requestedModel != "" {
		providerName = detectProvider(requestedModel)
		apiKey = s.findAPIKey(providerName, userID)
		if apiKey != "" {
			return requestedModel, apiKey, providerName, nil
		}
		return "", "", "", &apiKeyError{
			Code:     ErrNoAIKeyForModel,
			Message:  fmt.Sprintf("No API key found for model '%s' (provider: %s)", requestedModel, providerName),
			Hint:     fmt.Sprintf("Set your API key: PUT /api/v1/user/settings/ai-key with {\"provider\": \"%s\", \"api_key\": \"your-key\"}", providerName),
			Provider: providerName,
		}
	}

	// 2. 用户偏好模型
	if preferred, _ := s.db.GetSetting("preferred_model", userID); preferred != "" {
		providerName = detectProvider(preferred)
		if key := s.findAPIKey(providerName, userID); key != "" {
			log.Printf("🔑 Using preferred model: %s (provider: %s)", preferred, providerName)
			return preferred, key, providerName, nil
		}
	}

	// 3. 自动检测：按优先级尝试各 provider
	providers := []struct {
		name         string
		defaultModel string
		envKey       string
	}{
		{"anthropic", "claude-sonnet-4-20250514", "TOFI_ANTHROPIC_API_KEY"},
		{"openai", "gpt-5-mini", "TOFI_OPENAI_API_KEY"},
		{"gemini", "gemini-2.0-flash", "TOFI_GEMINI_API_KEY"},
		{"deepseek", "deepseek-chat", "TOFI_DEEPSEEK_API_KEY"},
	}

	for _, p := range providers {
		key := s.findAPIKey(p.name, userID)
		if key != "" {
			log.Printf("🔑 Auto-detected provider: %s", p.name)
			return p.defaultModel, key, p.name, nil
		}
	}

	return "", "", "", &apiKeyError{
		Code:    ErrNoAIKey,
		Message: "No AI provider configured. Set an API key to start using Tofi.",
		Hint:    "PUT /api/v1/user/settings/ai-key with {\"provider\": \"openai\", \"api_key\": \"sk-...\"}. Supported providers: openai, anthropic, gemini, deepseek, groq, openrouter",
	}
}

// findAPIKey 从 Settings 表和环境变量中查找 API Key
func (s *Server) findAPIKey(provider, userID string) string {
	// 1. Settings 表（user > system）
	key, err := s.db.ResolveAIKey(provider, userID)
	if err == nil && key != "" {
		return key
	}
	// claude → anthropic alias
	if provider == "claude" {
		key, err = s.db.ResolveAIKey("anthropic", userID)
		if err == nil && key != "" {
			return key
		}
	}

	// 2. 环境变量
	envMap := map[string]string{
		"openai":    "TOFI_OPENAI_API_KEY",
		"anthropic": "TOFI_ANTHROPIC_API_KEY",
		"claude":    "TOFI_ANTHROPIC_API_KEY",
		"gemini":    "TOFI_GEMINI_API_KEY",
		"deepseek":  "TOFI_DEEPSEEK_API_KEY",
	}
	if envName, ok := envMap[provider]; ok {
		if v := os.Getenv(envName); v != "" {
			return v
		}
	}

	return ""
}

// detectProvider 从模型名推断 provider (delegates to provider.DetectProvider)
func detectProvider(model string) string {
	return provider.DetectProvider(model)
}

// buildSkillDescriptions 构建 Skill 描述列表
func buildSkillDescriptions(skills []*storage.SkillRecord) string {
	if len(skills) == 0 {
		return "(No skills installed)"
	}

	var parts []string
	for _, s := range skills {
		desc := fmt.Sprintf("- **%s**: %s", s.Name, s.Description)
		if s.Source == "git" && s.SourceURL != "" {
			desc += fmt.Sprintf(" (from %s)", s.SourceURL)
		}
		parts = append(parts, desc)
	}
	return strings.Join(parts, "\n")
}
