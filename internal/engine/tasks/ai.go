package tasks

import (
	"fmt"
	"os"
	"strings"
	"tofi-core/internal/executor"
	"tofi-core/internal/mcp"
	"tofi-core/internal/models"
	"tofi-core/internal/pkg/logger"

	"github.com/tidwall/gjson"
)

// resolveAPIKey 解析 API Key，支持系统 key 模式
// 当 use_system_key 为 true 时，根据 provider 从环境变量加载对应的 API key
// 也尝试从 Settings 表解析（如果 DB 可用）
func resolveAPIKey(config map[string]interface{}, provider string, ctx *models.ExecutionContext) (string, error) {
	// 检查是否使用系统 key
	useSystemKey, _ := config["use_system_key"].(bool)

	if useSystemKey {
		// 先尝试从 Settings 表获取
		if db, ok := ctx.DB.(SkillStore); ok {
			apiKey, err := db.ResolveAIKey(provider, ctx.User)
			if err == nil && apiKey != "" {
				logger.Printf("[%s] Using settings AI key for provider '%s'", ctx.ExecutionID, provider)
				return apiKey, nil
			}
			// claude -> anthropic fallback
			if provider == "claude" {
				apiKey, err = db.ResolveAIKey("anthropic", ctx.User)
				if err == nil && apiKey != "" {
					return apiKey, nil
				}
			}
		}

		// 回退到环境变量
		var envKey string
		switch provider {
		case "openai":
			envKey = "TOFI_OPENAI_API_KEY"
		case "anthropic", "claude":
			envKey = "TOFI_ANTHROPIC_API_KEY"
		case "gemini":
			envKey = "TOFI_GEMINI_API_KEY"
		default:
			envKey = "TOFI_OPENAI_API_KEY"
		}

		apiKey := os.Getenv(envKey)
		if apiKey == "" {
			return "", fmt.Errorf("system API key not configured (env: %s)", envKey)
		}

		logger.Printf("[%s] Using env API key for provider '%s'", ctx.ExecutionID, provider)
		return apiKey, nil
	}

	// 使用用户自己的 key（从 config 或 settings）
	if apiKey, ok := config["api_key"].(string); ok && apiKey != "" {
		return apiKey, nil
	}

	// 最后尝试 settings 表
	if db, ok := ctx.DB.(SkillStore); ok {
		apiKey, err := db.ResolveAIKey(provider, ctx.User)
		if err == nil && apiKey != "" {
			return apiKey, nil
		}
	}

	return "", nil
}

type AI struct{}

// detectProviderFromModel infers the AI provider from the model name
func detectProviderFromModel(model string) string {
	modelLower := strings.ToLower(model)

	// Claude models
	if strings.HasPrefix(modelLower, "claude") {
		return "claude"
	}

	// Gemini models
	if strings.HasPrefix(modelLower, "gemini") {
		return "gemini"
	}

	// OpenAI models (gpt-*, o1-*, o3-*, etc.)
	if strings.HasPrefix(modelLower, "gpt-") ||
	   strings.HasPrefix(modelLower, "o1-") ||
	   strings.HasPrefix(modelLower, "o3-") ||
	   strings.HasPrefix(modelLower, "text-") ||
	   strings.HasPrefix(modelLower, "davinci") {
		return "openai"
	}

	// Default to OpenAI for unknown models
	return "openai"
}

func (a *AI) Execute(config map[string]interface{}, ctx *models.ExecutionContext) (string, error) {
	// 1. Check for Agent Mode (MCP Servers)
	if serversRaw, ok := config["mcp_servers"].([]interface{}); ok && len(serversRaw) > 0 {
		return a.executeAgent(config, serversRaw, ctx)
	}

	// 2. Standard Generation Mode

	model := fmt.Sprint(config["model"])

	// Handle openai-compatible mode: user provides full endpoint, use OpenAI format
	if model == "openai-compatible" {
		endpoint, _ := config["endpoint"].(string)
		if endpoint == "" {
			return "", fmt.Errorf("endpoint is required when model is 'openai-compatible'")
		}
		return a.executeOpenAICompatible(config, endpoint, ctx)
	}

	// Auto-detect provider from model name if not explicitly set
	provider := strings.ToLower(fmt.Sprint(config["provider"]))
	if provider == "" || provider == "<nil>" {
		provider = detectProviderFromModel(model)
	}

	var endpoint string
	if ep, ok := config["endpoint"].(string); ok {
		endpoint = ep
	}

	// Apply default endpoints if not specified
	if endpoint == "" {
		switch provider {
		case "openai":
			endpoint = "https://api.openai.com/v1"
		case "anthropic", "claude":
			endpoint = "https://api.anthropic.com/v1"
		case "gemini":
			endpoint = "https://generativelanguage.googleapis.com/v1beta"
		default:
			endpoint = "https://api.openai.com/v1" // Default to OpenAI
		}
	}

	// 解析 API Key（支持系统 key 模式）
	apiKey, err := resolveAPIKey(config, provider, ctx)
	if err != nil {
		return "", err
	}

	system := fmt.Sprint(config["system"])
	prompt := fmt.Sprint(config["prompt"])

	if prompt == "" {
		return "", fmt.Errorf("AI prompt cannot be empty")
	}

	headers := make(map[string]string)
	var payload map[string]interface{}

	switch provider {
	case "gemini":
		// Construct full Gemini URL if it's a base URL
		if !strings.Contains(endpoint, ":generateContent") {
			endpoint = fmt.Sprintf("%s/models/%s:generateContent", strings.TrimRight(endpoint, "/"), model)
		}
		// API Key is passed via header (x-goog-api-key) or query param?
		// Tofi uses header.
		headers["x-goog-api-key"] = apiKey
		payload = map[string]interface{}{
			"contents": []interface{}{
				map[string]interface{}{
					"parts": []map[string]string{{"text": system + "\n" + prompt}},
				},
			},
		}
	case "claude":
		if !strings.Contains(endpoint, "/messages") {
			endpoint = strings.TrimRight(endpoint, "/") + "/messages"
		}
		headers["x-api-key"] = apiKey
		headers["anthropic-version"] = "2023-06-01"
		payload = map[string]interface{}{
			"model":      model,
			"messages":   []map[string]string{{"role": "user", "content": prompt}},
			"system":     system,
			"max_tokens": 1024,
		}
	default: // OpenAI 兼容格式
		if apiKey != "" {
			headers["Authorization"] = "Bearer " + apiKey
		}

		useResponsesAPI := strings.HasPrefix(model, "gpt-5") || strings.Contains(endpoint, "/v1/responses")

		// Auto-append path if missing
		if !strings.Contains(endpoint, "/chat/completions") && !strings.Contains(endpoint, "/responses") {
			base := strings.TrimRight(endpoint, "/")
			if useResponsesAPI {
				endpoint = base + "/responses"
			} else {
				endpoint = base + "/chat/completions"
			}
		}

		if useResponsesAPI {
			input := []map[string]string{}
			if system != "" {
				input = append(input, map[string]string{"role": "system", "content": system})
			}
			input = append(input, map[string]string{"role": "user", "content": prompt})

			payload = map[string]interface{}{
				"model": model,
				"input": input,
				"reasoning": map[string]string{
					"effort": "low",
				},
			}
		} else {
			payload = map[string]interface{}{
				"model": model,
				"messages": []map[string]string{
					{"role": "system", "content": system},
					{"role": "user", "content": prompt},
				},
			}
		}
	}

	resp, err := executor.PostJSON(endpoint, headers, payload, 60)
	if err != nil {
		return "", err
	}

	paths := []string{
		"output.#(type==\"message\").content.0.text",
		"choices.0.message.content",
		"candidates.0.content.parts.0.text",
		"content.0.text",
	}
	for _, path := range paths {
		if res := gjson.Get(resp, path); res.Exists() {
			return res.String(), nil
		}
	}
	return resp, fmt.Errorf("AI response parsing failed")
}

// executeOpenAICompatible handles custom OpenAI-compatible endpoints (e.g., Ollama, vLLM)
// User provides the full endpoint URL, we just call it with OpenAI chat format
func (a *AI) executeOpenAICompatible(config map[string]interface{}, endpoint string, ctx *models.ExecutionContext) (string, error) {
	system := fmt.Sprint(config["system"])
	prompt := fmt.Sprint(config["prompt"])

	if prompt == "" {
		return "", fmt.Errorf("AI prompt cannot be empty")
	}

	headers := make(map[string]string)
	headers["Content-Type"] = "application/json"

	// API key is optional for local services like Ollama
	if apiKey, ok := config["api_key"].(string); ok && apiKey != "" {
		headers["Authorization"] = "Bearer " + apiKey
	}

	// Use "default" as model name, the actual model is configured on the server side
	payload := map[string]interface{}{
		"model": "default",
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": prompt},
		},
	}

	resp, err := executor.PostJSON(endpoint, headers, payload, 60)
	if err != nil {
		return "", err
	}

	// Try standard OpenAI response format
	if res := gjson.Get(resp, "choices.0.message.content"); res.Exists() {
		return res.String(), nil
	}

	return resp, fmt.Errorf("AI response parsing failed for openai-compatible endpoint")
}

func (a *AI) executeAgent(config map[string]interface{}, serverIDs []interface{}, ctx *models.ExecutionContext) (string, error) {

	// Load user MCP config

	userConfig, err := mcp.LoadUserMCPConfig(ctx.Paths.Home, ctx.User)

	if err != nil {

		return "", fmt.Errorf("failed to load user MCP config: %v", err)

	}

	if userConfig == nil {

		return "", fmt.Errorf("no mcp_config.json found for user '%s'", ctx.User)

	}



	// Resolve servers

	var activeServers []mcp.MCPServerConfig

	for _, idRaw := range serverIDs {

		id := fmt.Sprint(idRaw)

		if def, ok := userConfig.MCPServers[id]; ok {

			activeServers = append(activeServers, mcp.MCPServerConfig{

				Name:    id,

				Command: def.Command,

				Args:    def.Args,

				Env:     def.Env,

			})

		} else {

			return "", fmt.Errorf("MCP server '%s' not found in user config", id)

		}

	}



	// Prepare Agent Config

	agentCfg := mcp.AgentConfig{

		System:     fmt.Sprint(config["system"]),

		Prompt:     fmt.Sprint(config["prompt"]),

		MCPServers: activeServers,

	}

	model := fmt.Sprint(config["model"])
	agentCfg.AI.Model = model

	// Auto-detect provider from model if not explicitly set
	provider := strings.ToLower(fmt.Sprint(config["provider"]))
	if provider == "" || provider == "<nil>" {
		provider = detectProviderFromModel(model)
	}
	agentCfg.AI.Provider = provider

	// 解析 API Key（支持系统 key 模式）
	apiKey, err := resolveAPIKey(config, provider, ctx)
	if err != nil {
		return "", err
	}
	agentCfg.AI.APIKey = apiKey



	// Handle BaseURL/Endpoint

	var endpoint string

	if ep, ok := config["endpoint"].(string); ok {

		endpoint = ep

	}



	if endpoint == "" {

		switch provider {

		case "openai":

			endpoint = "https://api.openai.com/v1"

		case "anthropic":

			endpoint = "https://api.anthropic.com/v1"

		case "gemini":

			endpoint = "https://generativelanguage.googleapis.com/v1beta"

		}

	}



	// Path Construction

	switch provider {

	case "gemini":

		if !strings.Contains(endpoint, ":generateContent") {

			endpoint = fmt.Sprintf("%s/models/%s:generateContent", strings.TrimRight(endpoint, "/"), model)

		}

	case "claude":

		if !strings.Contains(endpoint, "/messages") {

			endpoint = strings.TrimRight(endpoint, "/") + "/messages"

		}

	default: // OpenAI

		if !strings.Contains(endpoint, "/chat/completions") && !strings.Contains(endpoint, "/responses") {

			endpoint = strings.TrimRight(endpoint, "/") + "/chat/completions"

		}

	}

	

	agentCfg.AI.Endpoint = endpoint



	// Run Loop

	return mcp.RunAgentLoop(agentCfg, ctx)

}



func (a *AI) Validate(n *models.Node) error {
	// model is required
	model, ok := n.Config["model"]
	if !ok || fmt.Sprint(model) == "" {
		return fmt.Errorf("config.model is required")
	}

	// When model is "openai-compatible", endpoint is required
	if fmt.Sprint(model) == "openai-compatible" {
		endpoint, _ := n.Config["endpoint"].(string)
		if endpoint == "" {
			return fmt.Errorf("config.endpoint is required when model is 'openai-compatible'")
		}
	}

	return nil
}
