package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"tofi-core/internal/models"

	"gopkg.in/yaml.v3"
)

// nameRegex 验证 Skill 名称格式
// 仅小写字母、数字、连字符；不能以 - 开头/结尾
var nameRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// ParseFile 从文件路径解析 SKILL.md
func ParseFile(path string) (*models.SkillFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read skill file: %w", err)
	}

	skill, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	// 填充文件系统信息
	skill.Dir = filepath.Dir(path)

	// 扫描 scripts/ 目录
	scriptsDir := filepath.Join(skill.Dir, "scripts")
	if entries, err := os.ReadDir(scriptsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				skill.ScriptDirs = append(skill.ScriptDirs, e.Name())
			}
		}
	}

	return skill, nil
}

// Parse 从字节数据解析 SKILL.md 内容
// SKILL.md 格式: --- YAML frontmatter --- Markdown body
func Parse(data []byte) (*models.SkillFile, error) {
	content := string(data)

	// 分离 frontmatter 和 body
	frontmatter, body, err := splitFrontmatter(content)
	if err != nil {
		return nil, err
	}

	// 解析 YAML frontmatter
	var manifest models.SkillManifest
	if err := yaml.Unmarshal([]byte(frontmatter), &manifest); err != nil {
		return nil, fmt.Errorf("invalid YAML frontmatter: %w", err)
	}

	// 验证必填字段
	if err := validateManifest(&manifest); err != nil {
		return nil, err
	}

	return &models.SkillFile{
		Manifest: manifest,
		Body:     strings.TrimSpace(body),
	}, nil
}

// splitFrontmatter 将 SKILL.md 内容分为 frontmatter 和 body
func splitFrontmatter(content string) (frontmatter, body string, err error) {
	content = strings.TrimSpace(content)

	// 必须以 --- 开头
	if !strings.HasPrefix(content, "---") {
		return "", "", fmt.Errorf("SKILL.md must start with '---' (YAML frontmatter delimiter)")
	}

	// 找到第二个 ---
	rest := content[3:]

	// 跳过第一行的换行符
	if idx := strings.IndexByte(rest, '\n'); idx >= 0 {
		rest = rest[idx+1:]
	}

	// 找结束分隔符
	endIdx := strings.Index(rest, "\n---")
	if endIdx < 0 {
		// 也尝试 \r\n---
		endIdx = strings.Index(rest, "\r\n---")
		if endIdx < 0 {
			return "", "", fmt.Errorf("SKILL.md missing closing '---' for YAML frontmatter")
		}
	}

	frontmatter = rest[:endIdx]

	// body 是 --- 之后的内容
	afterDelimiter := rest[endIdx+4:] // skip \n---
	body = strings.TrimLeft(afterDelimiter, "\r\n")

	return frontmatter, body, nil
}

// validateManifest 验证 SkillManifest 的必填字段和格式
func validateManifest(m *models.SkillManifest) error {
	// name 必填
	if m.Name == "" {
		return fmt.Errorf("skill 'name' is required")
	}

	// name 长度 1-64
	if len(m.Name) > 64 {
		return fmt.Errorf("skill 'name' must be at most 64 characters, got %d", len(m.Name))
	}

	// name 格式
	if !nameRegex.MatchString(m.Name) {
		return fmt.Errorf("skill 'name' must be lowercase alphanumeric with hyphens (no leading/trailing hyphens): %q", m.Name)
	}

	// name 不能有连续连字符
	if strings.Contains(m.Name, "--") {
		return fmt.Errorf("skill 'name' must not contain consecutive hyphens: %q", m.Name)
	}

	// description 必填
	if m.Description == "" {
		return fmt.Errorf("skill 'description' is required")
	}

	// description 长度限制
	if len(m.Description) > 1024 {
		return fmt.Errorf("skill 'description' must be at most 1024 characters, got %d", len(m.Description))
	}

	// compatibility 长度限制
	if len(m.Compatibility) > 500 {
		return fmt.Errorf("skill 'compatibility' must be at most 500 characters, got %d", len(m.Compatibility))
	}

	return nil
}

// ParseDir 解析一个技能目录（包含 SKILL.md）
func ParseDir(dir string) (*models.SkillFile, error) {
	skillPath := filepath.Join(dir, "SKILL.md")
	if _, err := os.Stat(skillPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("SKILL.md not found in %s", dir)
	}
	return ParseFile(skillPath)
}
