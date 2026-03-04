package skills

import (
	"fmt"
	"os"
	"path/filepath"

	"tofi-core/internal/models"
)

// LocalStore 管理本地文件系统中的技能目录
// 技能存储在 {homeDir}/.tofi/skills/ 下
type LocalStore struct {
	baseDir string // e.g. /home/user/.tofi/skills
}

// NewLocalStore 创建本地技能存储
func NewLocalStore(homeDir string) *LocalStore {
	dir := filepath.Join(homeDir, ".tofi", "skills")
	return &LocalStore{baseDir: dir}
}

// EnsureDir 确保技能目录存在
func (s *LocalStore) EnsureDir() error {
	return os.MkdirAll(s.baseDir, 0755)
}

// BaseDir 返回技能存储根目录
func (s *LocalStore) BaseDir() string {
	return s.baseDir
}

// SkillDir 返回特定技能的目录路径
func (s *LocalStore) SkillDir(name string) string {
	return filepath.Join(s.baseDir, name)
}

// List 列出本地所有已安装的技能
func (s *LocalStore) List() ([]*models.SkillFile, error) {
	if err := s.EnsureDir(); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return nil, fmt.Errorf("read skills directory: %w", err)
	}

	var skills []*models.SkillFile
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillDir := filepath.Join(s.baseDir, entry.Name())
		skill, err := ParseDir(skillDir)
		if err != nil {
			// 跳过无效的技能目录，但记录警告
			fmt.Printf("[skills] warning: skipping invalid skill directory %s: %v\n", entry.Name(), err)
			continue
		}
		skills = append(skills, skill)
	}

	return skills, nil
}

// Get 获取单个技能
func (s *LocalStore) Get(name string) (*models.SkillFile, error) {
	dir := s.SkillDir(name)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, fmt.Errorf("skill %q not installed", name)
	}
	return ParseDir(dir)
}

// Exists 检查技能是否存在
func (s *LocalStore) Exists(name string) bool {
	skillPath := filepath.Join(s.SkillDir(name), "SKILL.md")
	_, err := os.Stat(skillPath)
	return err == nil
}

// Remove 删除本地技能
func (s *LocalStore) Remove(name string) error {
	dir := s.SkillDir(name)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("skill %q not installed", name)
	}
	return os.RemoveAll(dir)
}

// SaveLocal 将 SKILL.md 内容保存为本地技能
// 用于用户手动创建或编辑技能
func (s *LocalStore) SaveLocal(name, content string) error {
	if err := s.EnsureDir(); err != nil {
		return err
	}

	// 先验证内容
	_, err := Parse([]byte(content))
	if err != nil {
		return fmt.Errorf("invalid SKILL.md content: %w", err)
	}

	dir := s.SkillDir(name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644)
}
