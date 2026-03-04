package skills

import (
	"fmt"
	"os"
	"os/exec"
)

// GitInstaller 通过 git clone 安装技能
type GitInstaller struct{}

// NewGitInstaller 创建 Git 安装器
func NewGitInstaller() *GitInstaller {
	return &GitInstaller{}
}

// Clone 克隆 git repo 到目标目录
func (g *GitInstaller) Clone(repoURL, destDir string) error {
	// 检查目标目录是否已存在
	if _, err := os.Stat(destDir); err == nil {
		return fmt.Errorf("directory already exists: %s", destDir)
	}

	// 执行 git clone (浅克隆以节省空间)
	cmd := exec.Command("git", "clone", "--depth", "1", repoURL, destDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		// 清理可能部分创建的目录
		os.RemoveAll(destDir)
		return fmt.Errorf("git clone failed: %w", err)
	}

	// 验证 SKILL.md 存在
	if _, err := ParseDir(destDir); err != nil {
		os.RemoveAll(destDir)
		return fmt.Errorf("cloned repo is not a valid skill: %w", err)
	}

	return nil
}

// Update 更新已安装的 skill（git pull）
func (g *GitInstaller) Update(skillDir string) error {
	cmd := exec.Command("git", "-C", skillDir, "pull", "--rebase")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git pull failed: %w", err)
	}

	// 重新验证
	if _, err := ParseDir(skillDir); err != nil {
		return fmt.Errorf("updated skill is invalid: %w", err)
	}

	return nil
}
