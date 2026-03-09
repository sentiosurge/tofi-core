package executor

import "context"

// SandboxConfig holds configuration for sandbox creation.
type SandboxConfig struct {
	HomeDir string // Tofi data directory (e.g. ~/.tofi)
	UserID  string // User identifier for persistent storage
	CardID  string // Task/card identifier for ephemeral workspace
}

// Executor abstracts sandbox command execution.
// DirectExecutor runs commands on the host; DockerExecutor (future) runs in containers.
type Executor interface {
	// CreateSandbox prepares an isolated execution environment.
	// Returns the sandbox path (task-level working directory).
	CreateSandbox(cfg SandboxConfig) (sandboxPath string, err error)

	// Execute runs a shell command in the sandbox with timeout.
	// userDir is the user's persistent directory (for installed tools); empty string if none.
	// env is an optional map of extra environment variables to inject (nil = none).
	Execute(ctx context.Context, sandboxPath, userDir, command string, timeoutSec int, env map[string]string) (output string, err error)

	// Cleanup removes the task-level sandbox directory (keeps user data).
	Cleanup(sandboxPath string)
}
