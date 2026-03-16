// Package workspace manages the file-based workspace for Tofi users.
// The workspace is the source of truth for agents, skills, and memory.
//
// Directory layout:
//
//	.tofi/
//	  users/
//	    {user-id}/
//	      agents/
//	        {agent-name}/
//	          APP.md
//	          SYSTEM_PROMPT.md    (optional)
//	          skills.txt          (optional)
//	          scripts/            (optional)
//	          memory/
//	            MEMORY.md
//	            {date}.md
//	      skills/
//	        {skill-name}/
//	          SKILL.md
//	          scripts/
//	      memory/
//	        MEMORY.md
//	        {date}.md
//	      sandbox/
//	        {cardID}/
package workspace

import (
	"os"
	"path/filepath"
)

// Workspace provides file-based access to a Tofi home directory.
type Workspace struct {
	homeDir string // root .tofi/ directory
}

// New creates a Workspace rooted at the given home directory.
func New(homeDir string) *Workspace {
	return &Workspace{homeDir: homeDir}
}

// HomeDir returns the root directory.
func (w *Workspace) HomeDir() string {
	return w.homeDir
}

// UserDir returns the root directory for a specific user.
// Creates the directory if it does not exist.
func (w *Workspace) UserDir(userID string) string {
	dir := filepath.Join(w.homeDir, "users", userID)
	os.MkdirAll(dir, 0755)
	return dir
}

// AgentsDir returns the agents directory for a user.
func (w *Workspace) AgentsDir(userID string) string {
	dir := filepath.Join(w.UserDir(userID), "agents")
	os.MkdirAll(dir, 0755)
	return dir
}

// AgentDir returns the directory for a specific agent.
func (w *Workspace) AgentDir(userID, agentName string) string {
	return filepath.Join(w.AgentsDir(userID), agentName)
}

// SkillsDir returns the user's installed skills directory.
func (w *Workspace) SkillsDir(userID string) string {
	dir := filepath.Join(w.UserDir(userID), "skills")
	os.MkdirAll(dir, 0755)
	return dir
}

// UserMemoryDir returns the user-level memory directory.
func (w *Workspace) UserMemoryDir(userID string) string {
	dir := filepath.Join(w.UserDir(userID), "memory")
	os.MkdirAll(dir, 0755)
	return dir
}

// AgentMemoryDir returns the memory directory for a specific agent.
func (w *Workspace) AgentMemoryDir(userID, agentName string) string {
	dir := filepath.Join(w.AgentDir(userID, agentName), "memory")
	os.MkdirAll(dir, 0755)
	return dir
}
