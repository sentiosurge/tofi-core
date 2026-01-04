package tasks

import (
	"fmt"
	"strings"
	"tofi-core/internal/executor"
	"tofi-core/internal/mcp"
	"tofi-core/internal/models"

	"github.com/tidwall/gjson"
)

type AI struct{}

func (a *AI) Execute(config map[string]interface{}, ctx *models.ExecutionContext) (string, error) {
	// 1. Check for Agent Mode (MCP Servers)
	if serversRaw, ok := config["mcp_servers"].([]interface{}); ok && len(serversRaw) > 0 {
		return a.executeAgent(config, serversRaw, ctx)
	}

	// 2. Standard Generation Mode

	provider := strings.ToLower(fmt.Sprint(config["provider"]))

	var endpoint string

	if ep, ok := config["endpoint"].(string); ok {

		endpoint = ep

	}

	// Apply default endpoints if not specified

	if endpoint == "" {

		switch provider {
		case "openai", "":
			endpoint = "https://api.openai.com/v1"
		case "anthropic":
			endpoint = "https://api.anthropic.com/v1"
		case "gemini":
			endpoint = "https://generativelanguage.googleapis.com/v1beta"
		}
	}

	apiKey := fmt.Sprint(config["api_key"])
	model := fmt.Sprint(config["model"])

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

	agentCfg.AI.Provider = strings.ToLower(fmt.Sprint(config["provider"]))

	agentCfg.AI.Model = fmt.Sprint(config["model"])

	agentCfg.AI.APIKey = fmt.Sprint(config["api_key"])

	

	// Handle BaseURL/Endpoint

	var endpoint string

	if ep, ok := config["endpoint"].(string); ok {

		endpoint = ep

	}

	provider := agentCfg.AI.Provider

	model := agentCfg.AI.Model



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
	provider, _ := n.Config["provider"].(string)
	endpoint, hasEndpoint := n.Config["endpoint"]

	// If endpoint is missing, check if provider is known
	if !hasEndpoint || fmt.Sprint(endpoint) == "" {
		knownProviders := map[string]bool{"openai": true, "anthropic": true, "gemini": true}
		if !knownProviders[strings.ToLower(provider)] {
			// If provider is also missing or unknown, then endpoint is required
			return fmt.Errorf("config.endpoint is required for custom provider '%s'", provider)
		}
	}

	if _, ok := n.Config["model"]; !ok {
		return fmt.Errorf("config.model is required")
	}
	return nil
}
