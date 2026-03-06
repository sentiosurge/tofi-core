package tasks

import (
	"encoding/json"
	"fmt"
	"strings"
	"tofi-core/internal/models"
	"tofi-core/internal/pkg/logger"
	"tofi-core/internal/storage"
)

// SkillStore 定义获取 Skill 所需的 DB 接口
// 使用接口避免循环引用
type SkillStore interface {
	GetSkill(id string) (*storage.SkillRecord, error)
	ResolveAIKey(provider, userID string) (string, error)
}

// Skill 节点类型：执行已安装的 Agent Skill
// 支持结构化输入、沙箱超时、Settings AI Key 解析
type Skill struct{}

func (s *Skill) Execute(config map[string]interface{}, ctx *models.ExecutionContext) (string, error) {
	// 1. 获取 skill_id
	skillID, _ := config["skill_id"].(string)
	if skillID == "" {
		return "", fmt.Errorf("config.skill_id is required")
	}

	// 2. 从 DB 加载 Skill
	db, ok := ctx.DB.(SkillStore)
	if !ok {
		return "", fmt.Errorf("database connection required for skill execution")
	}

	skill, err := db.GetSkill(skillID)
	if err != nil {
		return "", fmt.Errorf("skill '%s' not found: %v", skillID, err)
	}

	// 3. 构建 prompt — 支持结构化输入和自由文本
	prompt, err := buildPromptFromConfig(config, skill)
	if err != nil {
		return "", fmt.Errorf("input error: %v", err)
	}

	// 4. 解析 manifest
	var manifest models.SkillManifest
	json.Unmarshal([]byte(skill.ManifestJSON), &manifest)

	// 5. 确定模型
	model := resolveModel(config, manifest)

	// 6. 确定 provider
	provider := detectProviderFromModel(model)

	// 7. 解析 API Key — 优先级: config > settings(user > system) > env
	apiKey, err := resolveSkillAPIKey(config, db, provider, ctx)
	if err != nil {
		return "", fmt.Errorf("API key error for provider '%s': %v", provider, err)
	}

	// 8. 构建 AI 执行配置
	aiConfig := map[string]interface{}{
		"system":  buildSkillSystemPrompt(skill, &manifest),
		"prompt":  prompt,
		"model":   model,
		"api_key": apiKey,
	}

	// 传递 provider（如果手动指定）
	if p, ok := config["provider"].(string); ok && p != "" {
		aiConfig["provider"] = p
	}

	// 传递 endpoint（如果手动指定）
	if ep, ok := config["endpoint"].(string); ok && ep != "" {
		aiConfig["endpoint"] = ep
	}

	// max_tokens
	if mt, ok := config["max_tokens"]; ok {
		aiConfig["max_tokens"] = mt
	}

	// 9. 如果 Skill 声明了 allowed-tools，配置 MCP
	tools := skill.AllowedToolsList()
	if len(tools) > 0 {
		if mcpServers, ok := config["mcp_servers"]; ok {
			aiConfig["mcp_servers"] = mcpServers
		}
		logger.Printf("[%s] skill '%s' declares tools: %v", ctx.ExecutionID, skill.Name, tools)
	}

	// 10. 执行
	logger.Printf("[%s] executing skill '%s' (model=%s, provider=%s)", ctx.ExecutionID, skill.Name, model, provider)

	ai := &AI{}
	result, err := ai.Execute(aiConfig, ctx)
	if err != nil {
		return "", fmt.Errorf("skill '%s' execution failed: %v", skill.Name, err)
	}

	return result, nil
}

func (s *Skill) Validate(n *models.Node) error {
	skillID, ok := n.Config["skill_id"]
	if !ok || fmt.Sprint(skillID) == "" {
		return fmt.Errorf("config.skill_id is required")
	}
	return nil
}

// buildPromptFromConfig 从 config 构建 prompt
// 支持三种模式：
//  1. config["prompt"] — 自由文本（简单模式）
//  2. config["inputs"] — 结构化输入（key-value map）
//  3. 两者结合 — inputs 格式化后追加到 prompt
func buildPromptFromConfig(config map[string]interface{}, skill *storage.SkillRecord) (string, error) {
	prompt, _ := config["prompt"].(string)
	inputsRaw, hasInputs := config["inputs"]

	if !hasInputs && prompt == "" {
		// 检查旧式 user_input
		if ui, ok := config["user_input"].(string); ok && ui != "" {
			return ui, nil
		}
		return "", fmt.Errorf("prompt or inputs is required")
	}

	if !hasInputs {
		return prompt, nil
	}

	// 解析结构化输入
	inputsMap, ok := inputsRaw.(map[string]interface{})
	if !ok {
		return prompt, nil
	}

	// 验证 required 字段
	if skill.InputSchema != "" && skill.InputSchema != "{}" {
		var schema map[string]*models.SkillInput
		if err := json.Unmarshal([]byte(skill.InputSchema), &schema); err == nil {
			for name, def := range schema {
				if def.Required {
					if v, exists := inputsMap[name]; !exists || fmt.Sprint(v) == "" {
						return "", fmt.Errorf("required input '%s' is missing", name)
					}
				}
			}
		}
	}

	// 将结构化输入格式化为 prompt 部分
	var parts []string
	for k, v := range inputsMap {
		parts = append(parts, fmt.Sprintf("**%s**: %v", k, v))
	}
	structuredText := strings.Join(parts, "\n")

	if prompt != "" {
		return prompt + "\n\n" + structuredText, nil
	}
	return structuredText, nil
}

// resolveModel 确定执行模型（优先级: config > manifest > default）
func resolveModel(config map[string]interface{}, manifest models.SkillManifest) string {
	if model, ok := config["model"].(string); ok && model != "" {
		return model
	}
	if manifest.Model != "" {
		return manifest.Model
	}
	return "claude-sonnet-4-20250514"
}

// resolveSkillAPIKey 解析 API Key
// 优先级: config 直接指定 > Settings 表(user > system) > 环境变量 > use_system_key 兼容
func resolveSkillAPIKey(config map[string]interface{}, db SkillStore, provider string, ctx *models.ExecutionContext) (string, error) {
	// 1. config 直接指定
	if apiKey, ok := config["api_key"].(string); ok && apiKey != "" {
		return apiKey, nil
	}

	// 2. 从 Settings 表解析（user key > system key）
	apiKey, err := db.ResolveAIKey(provider, ctx.User)
	if err == nil && apiKey != "" {
		logger.Printf("[%s] using settings AI key for provider '%s'", ctx.ExecutionID, provider)
		return apiKey, nil
	}

	// 也尝试 "claude" -> "anthropic" 的映射
	if provider == "claude" {
		apiKey, err = db.ResolveAIKey("anthropic", ctx.User)
		if err == nil && apiKey != "" {
			return apiKey, nil
		}
	}

	// 3. 兼容旧模式：use_system_key + 环境变量
	useSystemKey, _ := config["use_system_key"].(bool)
	if useSystemKey {
		return resolveAPIKey(config, provider, ctx)
	}

	// 4. 环境变量兜底
	envKey, err := resolveAPIKey(config, provider, ctx)
	if err == nil && envKey != "" {
		return envKey, nil
	}

	return "", fmt.Errorf("no API key found — configure in Settings or set use_system_key")
}

// buildSkillSystemPrompt 从 Skill 记录构建 system prompt
func buildSkillSystemPrompt(skill *storage.SkillRecord, manifest *models.SkillManifest) string {
	prompt := skill.Instructions

	// 如果有描述，加入上下文前缀
	if skill.Description != "" {
		prompt = fmt.Sprintf("# %s\n\n> %s\n\n%s", skill.Name, skill.Description, prompt)
	}

	// 如果有结构化输入定义，附加输入 schema 说明
	if manifest != nil && manifest.Inputs != nil && len(manifest.Inputs) > 0 {
		prompt += "\n\n## Input Parameters\n"
		for name, input := range manifest.Inputs {
			req := ""
			if input.Required {
				req = " (required)"
			}
			prompt += fmt.Sprintf("- **%s** (%s)%s: %s\n", name, input.Type, req, input.Description)
			if len(input.Options) > 0 {
				prompt += fmt.Sprintf("  Options: %s\n", strings.Join(input.Options, ", "))
			}
		}
	}

	// 如果有输出格式定义
	if manifest != nil && manifest.Output != nil {
		prompt += fmt.Sprintf("\n\n## Output Format\nReturn your response as **%s**.", manifest.Output.Type)
		if manifest.Output.Description != "" {
			prompt += " " + manifest.Output.Description
		}
		prompt += "\n"
	}

	return prompt
}
