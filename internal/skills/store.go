package skills

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"tofi-core/internal/models"
)

// SkillInfo describes a skill visible to a user.
type SkillInfo struct {
	Name    string // skill directory name
	Scope   string // "global", "user", or "system"
	Version string
	Desc    string
	Source  string // "GitHub", "Local", "Built-in"
	Dir     string // absolute path to the skill directory
}

// LocalStore 管理本地文件系统中的技能目录
// Global skills: {TOFI_HOME}/skills/
// User skills:   {TOFI_HOME}/users/{uid}/skills/
type LocalStore struct {
	homeDir string // e.g. /home/user (OS home)
	baseDir string // e.g. /home/user/.tofi/skills (global)
}

// NewLocalStore 创建本地技能存储
// homeDir is TOFI_HOME (e.g., ~/.tofi), NOT the OS home directory.
// Skills are stored at TOFI_HOME/skills/ (global) and TOFI_HOME/users/{uid}/skills/ (per-user).
func NewLocalStore(homeDir string) *LocalStore {
	dir := filepath.Join(homeDir, "skills")
	return &LocalStore{homeDir: homeDir, baseDir: dir}
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
			log.Printf("[skills] warning: skipping invalid skill directory %s: %v", entry.Name(), err)
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

// --- User-scope operations ---

// UserSkillDir returns the skills directory for a specific user.
func (s *LocalStore) UserSkillDir(userID string) string {
	return filepath.Join(s.homeDir, "users", userID, "skills")
}

// ActivateGlobalSkill creates a symlink from user's skills dir to a global skill directory.
// globalDirName is the directory name in the global store (e.g., "stock-analysis" or "stock-analysis-a1b2c3").
// The symlink is named by skillName (without hash) so the user sees a clean name.
func (s *LocalStore) ActivateGlobalSkill(userID, skillName, globalDirName string) error {
	userDir := s.UserSkillDir(userID)
	if err := os.MkdirAll(userDir, 0755); err != nil {
		return fmt.Errorf("create user skills dir: %w", err)
	}

	link := filepath.Join(userDir, skillName)

	// If already exists, remove old symlink to update to new version
	if fi, err := os.Lstat(link); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			os.Remove(link) // remove old symlink to re-point
		} else {
			return nil // real dir (user-uploaded), don't overwrite
		}
	}

	target := filepath.Join(s.baseDir, globalDirName)
	return os.Symlink(target, link)
}

// SaveGlobal saves a skill to the global store with an optional hash suffix.
// Uses atomic write: writes to temp dir first, renames to final path.
// If the target directory already exists (same hash), returns the existing path.
// Returns the global directory name (e.g., "stock-analysis-a1b2c3").
func (s *LocalStore) SaveGlobal(name, hash, content string) (string, error) {
	if err := s.EnsureDir(); err != nil {
		return "", err
	}

	dirName := name
	if hash != "" {
		dirName = name + "-" + hash
	}
	destDir := filepath.Join(s.baseDir, dirName)

	// Already exists (same hash) — skip
	if _, err := os.Stat(destDir); err == nil {
		return dirName, nil
	}

	// Atomic write: temp dir → rename
	tmpDir, err := os.MkdirTemp(s.baseDir, ".tmp-"+name+"-")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}

	if err := os.WriteFile(filepath.Join(tmpDir, "SKILL.md"), []byte(content), 0644); err != nil {
		os.RemoveAll(tmpDir)
		return "", fmt.Errorf("write SKILL.md: %w", err)
	}

	if err := os.Rename(tmpDir, destDir); err != nil {
		os.RemoveAll(tmpDir)
		// Race: another process created it
		if _, statErr := os.Stat(destDir); statErr == nil {
			return dirName, nil
		}
		return "", fmt.Errorf("rename to final dir: %w", err)
	}

	return dirName, nil
}

// SaveUserLocal saves user-uploaded content directly to the user's skill directory.
func (s *LocalStore) SaveUserLocal(userID, name, content string) error {
	_, err := Parse([]byte(content))
	if err != nil {
		return fmt.Errorf("invalid SKILL.md content: %w", err)
	}

	userDir := s.UserSkillDir(userID)
	dir := filepath.Join(userDir, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644)
}

// DeactivateSkill removes a user's skill (symlink or real dir) and runs GC on global.
func (s *LocalStore) DeactivateSkill(userID, skillName string) error {
	userPath := filepath.Join(s.UserSkillDir(userID), skillName)

	// Check if it's a symlink (global) before removing
	fi, err := os.Lstat(userPath)
	if err != nil {
		return fmt.Errorf("skill %q not found for user: %w", skillName, err)
	}
	isSymlink := fi.Mode()&os.ModeSymlink != 0

	if err := os.RemoveAll(userPath); err != nil {
		return fmt.Errorf("remove skill: %w", err)
	}

	// GC: if it was a global symlink, check if anyone else still uses it
	if isSymlink {
		return s.gcGlobalSkill(skillName)
	}
	return nil
}

// gcGlobalSkill checks if any user still links to a global skill.
// If no users reference it, deletes the global copy.
func (s *LocalStore) gcGlobalSkill(skillName string) error {
	globalPath := filepath.Join(s.baseDir, skillName)
	if _, err := os.Stat(globalPath); os.IsNotExist(err) {
		return nil // already gone
	}

	usersDir := filepath.Join(s.homeDir, "users")
	entries, err := os.ReadDir(usersDir)
	if err != nil {
		// No users dir = no references
		return os.RemoveAll(globalPath)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		link := filepath.Join(usersDir, entry.Name(), "skills", skillName)
		if _, err := os.Lstat(link); err == nil {
			return nil // someone still uses it
		}
	}

	// No users reference it → delete global copy
	return os.RemoveAll(globalPath)
}

// ListUserSkills returns all skills visible to a user (symlinks + real dirs).
func (s *LocalStore) ListUserSkills(userID string) ([]SkillInfo, error) {
	userDir := s.UserSkillDir(userID)
	if err := os.MkdirAll(userDir, 0755); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(userDir)
	if err != nil {
		return nil, err
	}

	var skills []SkillInfo
	for _, entry := range entries {
		name := entry.Name()
		fullPath := filepath.Join(userDir, name)

		info := SkillInfo{Name: name, Dir: fullPath}

		// Determine scope by checking if symlink
		fi, err := os.Lstat(fullPath)
		if err != nil {
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			info.Scope = "global"
			// Resolve symlink to get actual dir
			resolved, err := os.Readlink(fullPath)
			if err == nil {
				if !filepath.IsAbs(resolved) {
					resolved = filepath.Join(userDir, resolved)
				}
				info.Dir = resolved
			}
		} else {
			info.Scope = "user"
		}

		// Parse SKILL.md for metadata
		skill, err := ParseDir(info.Dir)
		if err != nil {
			info.Desc = "(invalid SKILL.md)"
			skills = append(skills, info)
			continue
		}

		info.Version = skill.Manifest.Version
		info.Desc = skill.Manifest.Description
		if info.Version == "" {
			info.Version = "1.0"
		}

		skills = append(skills, info)
	}

	return skills, nil
}

// LoadSkill loads a single user skill from the filesystem by parsing its SKILL.md.
func (s *LocalStore) LoadSkill(userID, skillName string) (*models.SkillFile, error) {
	skillDir := filepath.Join(s.UserSkillDir(userID), skillName)
	resolved, err := filepath.EvalSymlinks(skillDir)
	if err != nil {
		return nil, err
	}
	return ParseDir(resolved)
}

// SaveUserSkill copies a skill directory into a user's private skills dir.
func (s *LocalStore) SaveUserSkill(userID, skillName, srcDir string) error {
	userDir := s.UserSkillDir(userID)
	if err := os.MkdirAll(userDir, 0755); err != nil {
		return err
	}

	destDir := filepath.Join(userDir, skillName)
	return copyDir(srcDir, destDir)
}
