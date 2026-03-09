package skills

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"tofi-core/internal/models"
)

// agentsSkillRegex 从 AGENTS.md 中提取 <skill name="xxx" .../> 的 name 属性
var agentsSkillRegex = regexp.MustCompile(`<skill\s+[^>]*name="([^"]+)"`)

// parseAgentsMD 从 AGENTS.md 内容中提取所有技能名称
func parseAgentsMD(data []byte) []string {
	matches := agentsSkillRegex.FindAllSubmatch(data, -1)
	names := make([]string, 0, len(matches))
	seen := make(map[string]bool)
	for _, m := range matches {
		name := string(m[1])
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// skipDirs 递归搜索时跳过的目录
var skipDirs = map[string]bool{
	"node_modules": true, ".git": true, "dist": true, "build": true,
	"__pycache__": true, ".next": true, ".nuxt": true, "vendor": true,
	"target": true, "bin": true, "obj": true,
}

// priorityDirs SKILL.md 优先搜索的目录（多技能仓库标准）
var priorityDirs = []string{
	"skills", ".agents/skills", ".agent/skills",
	".claude", ".cursor", ".continue", ".openhands",
}

const maxDiscoveryDepth = 5

// SkillInstaller 技能安装器
// 支持 GitHub shorthand (owner/repo@skill)、Git URL、本地路径
type SkillInstaller struct {
	store *LocalStore
}

// NewSkillInstaller 创建安装器
func NewSkillInstaller(store *LocalStore) *SkillInstaller {
	return &SkillInstaller{store: store}
}

// InstallResult 安装结果
type InstallResult struct {
	Skills []*models.SkillFile // 安装的技能列表
	Source *ParsedSource       // 解析后的来源
}

// Install 从 source 字符串安装技能
// 支持格式: owner/repo, owner/repo@skill, Git URL, 本地路径
func (si *SkillInstaller) Install(source string) (*InstallResult, error) {
	// 1. 解析来源
	ps, err := ParseSource(source)
	if err != nil {
		return nil, fmt.Errorf("invalid source: %w", err)
	}

	// 2. 获取源代码到临时目录
	sourceDir, cleanup, err := si.fetchSource(ps)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// 3. 在源码中发现所有 SKILL.md
	discovered, err := DiscoverSkills(sourceDir)
	if err != nil {
		return nil, fmt.Errorf("no skills found: %w", err)
	}

	if len(discovered) == 0 {
		return nil, fmt.Errorf("no valid SKILL.md files found in %s", ps.DisplayURL())
	}

	// 4. 如果有 skillFilter，过滤
	if ps.SkillFilter != "" {
		filtered := filterByName(discovered, ps.SkillFilter)
		if len(filtered) == 0 {
			names := make([]string, len(discovered))
			for i, s := range discovered {
				names[i] = s.Manifest.Name
			}
			return nil, fmt.Errorf("skill %q not found in repo, available: [%s]",
				ps.SkillFilter, strings.Join(names, ", "))
		}
		discovered = filtered
	}

	// 5. 安装每个发现的技能到本地 store
	if err := si.store.EnsureDir(); err != nil {
		return nil, fmt.Errorf("create skills directory: %w", err)
	}

	var installed []*models.SkillFile
	for _, skill := range discovered {
		if err := si.installOne(skill); err != nil {
			log.Printf("[skills] warning: failed to install %s: %v", skill.Manifest.Name, err)
			continue
		}
		installed = append(installed, skill)
	}

	if len(installed) == 0 {
		return nil, fmt.Errorf("failed to install any skills from %s", ps.DisplayURL())
	}

	return &InstallResult{
		Skills: installed,
		Source: ps,
	}, nil
}

// fetchSource 获取源代码到本地目录
func (si *SkillInstaller) fetchSource(ps *ParsedSource) (dir string, cleanup func(), err error) {
	noop := func() {}

	switch ps.Type {
	case SourceLocal:
		// 本地路径直接使用
		return ps.LocalPath, noop, nil

	case SourceGitHub, SourceGitLab, SourceGit:
		// Git clone 到临时目录
		tmpDir, err := os.MkdirTemp("", "tofi-skill-*")
		if err != nil {
			return "", noop, fmt.Errorf("create temp dir: %w", err)
		}

		cleanupFn := func() {
			os.RemoveAll(tmpDir)
		}

		if err := gitClone(ps.CloneURL, ps.Ref, tmpDir); err != nil {
			cleanupFn()
			return "", noop, err
		}

		// 如果有 subpath，返回子目录
		targetDir := tmpDir
		if ps.Subpath != "" {
			targetDir = filepath.Join(tmpDir, ps.Subpath)
			if _, err := os.Stat(targetDir); os.IsNotExist(err) {
				cleanupFn()
				return "", noop, fmt.Errorf("subpath %q not found in repo", ps.Subpath)
			}
		}

		return targetDir, cleanupFn, nil

	default:
		return "", noop, fmt.Errorf("unsupported source type: %s", ps.Type)
	}
}

// gitClone 执行 git clone（浅克隆）
func gitClone(repoURL, ref, destDir string) error {
	args := []string{"clone", "--depth", "1"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, repoURL, destDir)

	cmd := exec.Command("git", args...)
	// 禁止交互式 prompt
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone failed: %s\n%s", err, string(output))
	}

	return nil
}

// DiscoverSkills 在目录中递归发现所有 SKILL.md
// 采用四层搜索策略:
//  1. 直接检查: 目标目录是否直接包含 SKILL.md
//  1.5. AGENTS.md: 解析 agentskills.io 标准索引，定位 skills/<name>/SKILL.md
//  2. 优先目录: 扫描标准 skill 目录 (skills/, .agents/skills/ 等)
//  3. 递归回退: 遍历整个目录树
func DiscoverSkills(rootDir string) ([]*models.SkillFile, error) {
	seen := make(map[string]bool) // 按 name 去重
	var results []*models.SkillFile

	addSkill := func(sf *models.SkillFile) {
		if sf == nil || seen[sf.Manifest.Name] {
			return
		}
		seen[sf.Manifest.Name] = true
		results = append(results, sf)
	}

	// Layer 1: 直接检查根目录
	if sf, err := ParseDir(rootDir); err == nil {
		addSkill(sf)
		return results, nil // 单技能仓库，直接返回
	}

	// Layer 1.5: 检查 AGENTS.md（agentskills.io 标准）
	agentsMDPath := filepath.Join(rootDir, "AGENTS.md")
	if data, err := os.ReadFile(agentsMDPath); err == nil {
		names := parseAgentsMD(data)
		for _, name := range names {
			skillDir := filepath.Join(rootDir, "skills", name)
			if sf, err := ParseDir(skillDir); err == nil {
				addSkill(sf)
			}
		}
		if len(results) > 0 {
			return results, nil
		}
	}

	// Layer 2: 优先目录
	for _, pdir := range priorityDirs {
		scanDir := filepath.Join(rootDir, pdir)
		if info, err := os.Stat(scanDir); err != nil || !info.IsDir() {
			continue
		}

		// 检查优先目录本身是否有 SKILL.md
		if sf, err := ParseDir(scanDir); err == nil {
			addSkill(sf)
			continue
		}

		// 扫描子目录
		entries, err := os.ReadDir(scanDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			subDir := filepath.Join(scanDir, entry.Name())
			if sf, err := ParseDir(subDir); err == nil {
				addSkill(sf)
			}
		}
	}

	if len(results) > 0 {
		return results, nil
	}

	// Layer 3: 递归回退
	discoverRecursive(rootDir, 0, addSkill)

	if len(results) == 0 {
		return nil, fmt.Errorf("no SKILL.md files found in %s", rootDir)
	}

	return results, nil
}

// discoverRecursive 递归搜索 SKILL.md
func discoverRecursive(dir string, depth int, addSkill func(*models.SkillFile)) {
	if depth > maxDiscoveryDepth {
		return
	}

	// 检查当前目录
	if sf, err := ParseDir(dir); err == nil {
		addSkill(sf)
		return // 找到了就不需要继续深入
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() || skipDirs[entry.Name()] {
			continue
		}
		discoverRecursive(filepath.Join(dir, entry.Name()), depth+1, addSkill)
	}
}

// filterByName 按名称过滤（大小写不敏感）
func filterByName(skills []*models.SkillFile, name string) []*models.SkillFile {
	name = strings.ToLower(name)
	var matched []*models.SkillFile
	for _, s := range skills {
		if strings.ToLower(s.Manifest.Name) == name {
			matched = append(matched, s)
		}
	}
	return matched
}

// installOne 安装单个技能到本地 store
func (si *SkillInstaller) installOne(skill *models.SkillFile) error {
	name := skill.Manifest.Name
	destDir := si.store.SkillDir(name)

	// 如果已存在先删除（覆盖安装）
	if _, err := os.Stat(destDir); err == nil {
		os.RemoveAll(destDir)
	}

	// 如果有源目录（从 git clone 来），复制整个 skill 目录
	if skill.Dir != "" {
		return copyDir(skill.Dir, destDir)
	}

	// 否则仅保存 SKILL.md（从内容安装）
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}

	content := buildSkillMDContent(skill)
	return os.WriteFile(filepath.Join(destDir, "SKILL.md"), []byte(content), 0644)
}

// buildSkillMDContent 从 SkillFile 重建 SKILL.md 内容
func buildSkillMDContent(skill *models.SkillFile) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("name: %s\n", skill.Manifest.Name))
	sb.WriteString(fmt.Sprintf("description: %s\n", skill.Manifest.Description))
	if skill.Manifest.Model != "" {
		sb.WriteString(fmt.Sprintf("model: %s\n", skill.Manifest.Model))
	}
	if skill.Manifest.AllowedTools != "" {
		sb.WriteString(fmt.Sprintf("allowed-tools: %s\n", skill.Manifest.AllowedTools))
	}
	sb.WriteString("---\n\n")
	sb.WriteString(skill.Body)
	return sb.String()
}

// copyDir 递归复制目录（排除 .git 等）
func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		// 跳过不需要复制的目录/文件
		if entry.Name() == ".git" || strings.HasPrefix(entry.Name(), "_") {
			continue
		}

		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			data, err := os.ReadFile(srcPath)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dstPath, data, 0644); err != nil {
				return err
			}
		}
	}

	return nil
}

// PreviewInstall 预览安装: clone → discover，但不安装到 local store
// 返回发现的 skills 和 cleanup 函数（调用方控制临时目录生命周期）
func (si *SkillInstaller) PreviewInstall(source string) (*InstallResult, func(), error) {
	ps, err := ParseSource(source)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid source: %w", err)
	}

	sourceDir, cleanup, err := si.fetchSource(ps)
	if err != nil {
		return nil, nil, err
	}

	discovered, err := DiscoverSkills(sourceDir)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("no skills found: %w", err)
	}

	if len(discovered) == 0 {
		cleanup()
		return nil, nil, fmt.Errorf("no valid SKILL.md files found in %s", ps.DisplayURL())
	}

	if ps.SkillFilter != "" {
		filtered := filterByName(discovered, ps.SkillFilter)
		if len(filtered) == 0 {
			cleanup()
			names := make([]string, len(discovered))
			for i, s := range discovered {
				names[i] = s.Manifest.Name
			}
			return nil, nil, fmt.Errorf("skill %q not found in repo, available: [%s]",
				ps.SkillFilter, strings.Join(names, ", "))
		}
		discovered = filtered
	}

	return &InstallResult{Skills: discovered, Source: ps}, cleanup, nil
}

// InstallOne 安装单个已 discover 的技能到本地 store（公开方法，供 confirm 流程使用）
func (si *SkillInstaller) InstallOne(skill *models.SkillFile) error {
	if err := si.store.EnsureDir(); err != nil {
		return fmt.Errorf("create skills directory: %w", err)
	}
	return si.installOne(skill)
}

// Update 更新已安装的 skill（git pull）
func (si *SkillInstaller) Update(skillDir string) error {
	cmd := exec.Command("git", "-C", skillDir, "pull", "--rebase")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git pull failed: %s\n%s", err, string(output))
	}

	// 重新验证
	if _, err := ParseDir(skillDir); err != nil {
		return fmt.Errorf("updated skill is invalid: %w", err)
	}

	return nil
}
