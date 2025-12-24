package tasks

import (
	"fmt"
	"strings"
	"tofi-core/internal/executor"
	"tofi-core/internal/models"

	"github.com/tidwall/gjson"
)

type AI struct{}

func (a *AI) Execute(n *models.Node, ctx *models.ExecutionContext) (string, error) {
	// Config: 静态配置
	endpoint := ctx.ReplaceParams(n.Config["endpoint"])
	apiKey := ctx.ReplaceParams(n.Config["api_key"])
	model := ctx.ReplaceParams(n.Config["model"])
	provider := strings.ToLower(n.Config["provider"])

	// Input: 动态输入
	system, _ := n.Input["system"].(string) // system 是可选的，默认空字符串
	prompt, ok := n.Input["prompt"].(string)
	if !ok {
		return "", fmt.Errorf("AI prompt 必须是字符串")
	}

	system = ctx.ReplaceParams(system)
	prompt = ctx.ReplaceParams(prompt)

	headers := make(map[string]string)
	var payload map[string]interface{}

	// --- 多厂商适配逻辑 ---
	switch provider {
	case "gemini":
		headers["x-goog-api-key"] = apiKey
		payload = map[string]interface{}{
			"contents": []interface{}{
				map[string]interface{}{
					"parts": []map[string]string{{"text": system + "\n" + prompt}},
				},
			},
		}
	case "claude":
		headers["x-api-key"] = apiKey
		headers["anthropic-version"] = "2023-06-01"
		payload = map[string]interface{}{
			"model":      model,
			"messages":   []map[string]string{{"role": "user", "content": prompt}},
			"system":     system,
			"max_tokens": 1024,
		}
	default: // OpenAI 兼容格式 (Ollama, DeepSeek, OpenAI)
		if apiKey != "" {
			headers["Authorization"] = "Bearer " + apiKey
		}
		payload = map[string]interface{}{
			"model": model,
			"messages": []map[string]string{
				{"role": "system", "content": system},
				{"role": "user", "content": prompt},
			},
		}
	}

	resp, err := executor.PostJSON(endpoint, headers, payload, 60)
	if err != nil {
		return "", err
	}

	// 统一结果提取
	paths := []string{
		"choices.0.message.content",
		"candidates.0.content.parts.0.text",
		"content.0.text",
	}
	for _, path := range paths {
		if res := gjson.Get(resp, path); res.Exists() {
			return res.String(), nil
		}
	}
	return resp, fmt.Errorf("AI 响应解析失败")
}

func (a *AI) Validate(n *models.Node) error {
	if n.Config["endpoint"] == "" {
		return fmt.Errorf("config.endpoint is required")
	}
	if n.Config["model"] == "" {
		return fmt.Errorf("config.model is required")
	}
	if _, ok := n.Input["prompt"].(string); !ok {
		return fmt.Errorf("input.prompt is required and must be a string")
	}
	
	// 可选检查 provider
	provider := strings.ToLower(n.Config["provider"])
	if provider != "" && provider != "openai" && provider != "claude" && provider != "gemini" && provider != "ollama" {
		return fmt.Errorf("invalid config.provider: %s", provider)
	}
	return nil
}
