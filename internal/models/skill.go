package models

// models/skill.go — Skill 相关数据模型

// SkillManifest 表示解析后的 SKILL.md 前置元数据
// 遵循 Agent Skills 开放标准 (agentskills.io/specification)
type SkillManifest struct {
	// === 标准字段 (Agent Skills Open Standard) ===

	// Name 技能唯一标识符
	// 必填，1-64 字符，仅 [a-z0-9-]，不能以 - 开头/结尾，不能有 --
	Name string `yaml:"name" json:"name"`

	// Description 描述技能做什么以及何时使用
	// 必填，1-1024 字符
	Description string `yaml:"description" json:"description"`

	// License 许可证
	License string `yaml:"license,omitempty" json:"license,omitempty"`

	// Compatibility 环境依赖需求
	Compatibility string `yaml:"compatibility,omitempty" json:"compatibility,omitempty"`

	// Metadata 扩展元数据 (key-value 都是 string)
	Metadata map[string]string `yaml:"metadata,omitempty" json:"metadata,omitempty"`

	// AllowedTools 预批准的工具列表 (空格分隔)
	AllowedTools string `yaml:"allowed-tools,omitempty" json:"allowed_tools,omitempty"`

	// === 扩展字段 (Claude Code / Tofi) ===

	// ArgumentHint 参数提示，如 "[issue-number]"
	ArgumentHint string `yaml:"argument-hint,omitempty" json:"argument_hint,omitempty"`

	// Model 技能激活时使用的模型
	Model string `yaml:"model,omitempty" json:"model,omitempty"`

	// Context "fork" = 在隔离子上下文中运行
	Context string `yaml:"context,omitempty" json:"context,omitempty"`

	// Agent 子代理类型 (需 context: fork)
	Agent string `yaml:"agent,omitempty" json:"agent,omitempty"`

	// === Tofi 3.0 扩展 ===

	// Inputs 结构化输入定义
	Inputs map[string]*SkillInput `yaml:"inputs,omitempty" json:"inputs,omitempty"`

	// Output 输出格式定义
	Output *SkillOutput `yaml:"output,omitempty" json:"output,omitempty"`
}

// SkillInput 定义 Skill 的一个输入参数
type SkillInput struct {
	Type        string   `yaml:"type" json:"type"`                                   // text, number, file, select, boolean
	Description string   `yaml:"description" json:"description"`                     // 参数描述
	Required    bool     `yaml:"required" json:"required"`                           // 是否必填
	Default     string   `yaml:"default,omitempty" json:"default,omitempty"`         // 默认值
	Options     []string `yaml:"options,omitempty" json:"options,omitempty"`         // type=select 时的选项列表
}

// SkillOutput 定义 Skill 的输出格式
type SkillOutput struct {
	Type        string `yaml:"type" json:"type"`                 // text, json, markdown, file
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// SkillFile 表示完整的解析后 SKILL.md 文件
type SkillFile struct {
	Manifest SkillManifest `json:"manifest"`
	Body     string        `json:"body"` // Markdown 正文 (AI 指令)

	// 文件系统信息 (运行时填充)
	Dir        string   `json:"dir,omitempty"`     // 技能目录路径
	ScriptDirs []string `json:"scripts,omitempty"` // scripts/ 中的文件列表
}

// AllowedToolsList 将空格分隔的 allowed-tools 字符串解析为列表
func (m *SkillManifest) AllowedToolsList() []string {
	if m.AllowedTools == "" {
		return nil
	}
	var tools []string
	for _, t := range splitSpaces(m.AllowedTools) {
		if t != "" {
			tools = append(tools, t)
		}
	}
	return tools
}

// RequiredEnvVars 从 metadata 中提取所需的环境变量
func (m *SkillManifest) RequiredEnvVars() []string {
	if m.Metadata == nil {
		return nil
	}
	// 约定: metadata.requires.env 是逗号分隔的环境变量列表
	// 但标准中 metadata 是 flat map[string]string
	// 我们检查常见的 key
	var envVars []string
	for _, key := range []string{"requires_env", "required_env", "env"} {
		if val, ok := m.Metadata[key]; ok && val != "" {
			for _, v := range splitCommaOrSpace(val) {
				if v != "" {
					envVars = append(envVars, v)
				}
			}
		}
	}
	return envVars
}

// splitSpaces 按空白字符分割
func splitSpaces(s string) []string {
	var parts []string
	current := ""
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(r)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}

// splitCommaOrSpace 按逗号或空格分割
func splitCommaOrSpace(s string) []string {
	var parts []string
	current := ""
	for _, r := range s {
		if r == ',' || r == ' ' || r == '\t' {
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(r)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}
