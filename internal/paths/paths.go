// Package paths provides a single source of truth for all Tofi filesystem paths.
//
// Terminology:
//
//	$HOME       = user's OS home directory (e.g., /Users/jackzhao)
//	TOFI_HOME   = $HOME/.tofi — the root of all Tofi data
//
// All paths in Tofi MUST be derived from these two roots via this package.
// Never manually join ".tofi" to anything — use paths.TofiHome() instead.
package paths

import (
	"os"
	"path/filepath"
	"sync"
)

var (
	once     sync.Once
	tofiHome string
)

// TofiHome returns the root Tofi data directory (~/.tofi).
// Can be overridden via TOFI_HOME environment variable.
func TofiHome() string {
	once.Do(func() {
		if env := os.Getenv("TOFI_HOME"); env != "" {
			tofiHome = env
		} else {
			home, _ := os.UserHomeDir()
			tofiHome = filepath.Join(home, ".tofi")
		}
	})
	return tofiHome
}

// SetTofiHome overrides the default for testing or custom deployments.
// Must be called before any path function. Not goroutine-safe with TofiHome().
func SetTofiHome(dir string) {
	tofiHome = dir
	// Reset once so it doesn't re-initialize
	once = sync.Once{}
	once.Do(func() {}) // mark as done
}

// ──────────────────────────────────────────────────────────────
// Top-level directories under TOFI_HOME
// ──────────────────────────────────────────────────────────────

// DB returns the path to the SQLite database.
func DB() string {
	return filepath.Join(TofiHome(), "tofi.db")
}

// Config returns the path to the config file.
func Config() string {
	return filepath.Join(TofiHome(), "config.yaml")
}

// LogsDir returns the directory for log files.
func LogsDir() string {
	return filepath.Join(TofiHome(), "logs")
}

// SkillsDir returns the global skills directory.
func SkillsDir() string {
	return filepath.Join(TofiHome(), "skills")
}

// PackagesDir returns the shared packages directory (pip, npm, etc.).
func PackagesDir() string {
	return filepath.Join(TofiHome(), "packages")
}

// ──────────────────────────────────────────────────────────────
// Per-user directories under TOFI_HOME/users/{userID}/
// ──────────────────────────────────────────────────────────────

// UserDir returns the root directory for a specific user.
func UserDir(userID string) string {
	return filepath.Join(TofiHome(), "users", userID)
}

// UserChatDir returns the chat sessions directory for a user.
func UserChatDir(userID string) string {
	return filepath.Join(UserDir(userID), "chat")
}

// UserSkillsDir returns the skills directory for a user.
func UserSkillsDir(userID string) string {
	return filepath.Join(UserDir(userID), "skills")
}

// UserAppsDir returns the apps directory for a user.
func UserAppsDir(userID string) string {
	return filepath.Join(UserDir(userID), "apps")
}

// UserSandboxDir returns the sandbox root for a user's task execution.
func UserSandboxDir(userID, taskID string) string {
	return filepath.Join(UserDir(userID), "sandbox", taskID)
}

// UserMemoryDir returns the memory directory for a user.
func UserMemoryDir(userID string) string {
	return filepath.Join(UserDir(userID), "memory")
}

// UserTranscriptsDir returns the transcript directory for crash recovery.
func UserTranscriptsDir(userID string) string {
	return filepath.Join(UserDir(userID), "transcripts")
}

// UserUploadsDir returns the uploads directory for a user.
func UserUploadsDir(userID string) string {
	return filepath.Join(UserDir(userID), "uploads")
}

// UserArtifactsDir returns the artifacts directory for a user.
func UserArtifactsDir(userID string) string {
	return filepath.Join(UserDir(userID), "artifacts")
}

// ──────────────────────────────────────────────────────────────
// Scoped chat directories (agents/apps have their own chat dirs)
// ──────────────────────────────────────────────────────────────

// ScopedChatDir returns the chat directory for a specific scope (agent/app).
func ScopedChatDir(userID, scope string) string {
	if scope == "" {
		return UserChatDir(userID)
	}
	return filepath.Join(UserDir(userID), "agents", scope, "chat")
}

// ──────────────────────────────────────────────────────────────
// Skill-specific paths
// ──────────────────────────────────────────────────────────────

// GlobalSkillDir returns the directory for a specific global skill.
func GlobalSkillDir(skillName string) string {
	return filepath.Join(SkillsDir(), skillName)
}

// UserSkillDir returns the directory for a user's specific skill.
func UserSkillDir(userID, skillName string) string {
	return filepath.Join(UserSkillsDir(userID), skillName)
}
