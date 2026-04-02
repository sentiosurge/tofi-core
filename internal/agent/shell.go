package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"tofi-core/internal/executor"
)

// ──────────────────────────────────────────────────────────────
// Shell command enhancement layer
// Sits between agent loop and executor, adding:
// - Timeout classification (install commands get more time)
// - Destructive command detection + confirmation
// - Auto-backgrounding (long commands report progress)
// - Exit code semantic interpretation
// - Structured result format
// ──────────────────────────────────────────────────────────────

// ShellResult holds the structured result of a shell command execution.
type ShellResult struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	TimedOut   bool   `json:"timed_out"`
	DurationMs int64  `json:"duration_ms"`

	// Backgrounded is true if the command was auto-backgrounded.
	Backgrounded bool   `json:"backgrounded,omitempty"`
	TaskID       string `json:"task_id,omitempty"`

	// Semantic interpretation of exit code (e.g., "No matches found" for grep exit 1).
	Interpretation string `json:"interpretation,omitempty"`
}

// FormatForAgent returns a string representation suitable for the agent's context.
func (r *ShellResult) FormatForAgent() string {
	var sb strings.Builder

	if r.TimedOut {
		fmt.Fprintf(&sb, "[Timed out after %dms]\n", r.DurationMs)
	}
	if r.Backgrounded {
		fmt.Fprintf(&sb, "[Backgrounded — task_id: %s]\n", r.TaskID)
	}

	output := r.Stdout
	if r.Stderr != "" {
		if output != "" {
			output += "\n"
		}
		output += r.Stderr
	}

	if output != "" {
		sb.WriteString(output)
	}

	if r.ExitCode != 0 && !r.TimedOut {
		if r.Interpretation != "" {
			fmt.Fprintf(&sb, "\n[Exit %d: %s]", r.ExitCode, r.Interpretation)
		} else {
			fmt.Fprintf(&sb, "\n[Exit code: %d]", r.ExitCode)
		}
	}

	return sb.String()
}

// ──────────────────────────────────────────────────────────────
// Timeout Classification
// ──────────────────────────────────────────────────────────────

const (
	TimeoutDefault = 60  // seconds — normal commands
	TimeoutInstall = 300 // seconds — package install, build, download
	TimeoutMax     = 600 // seconds — absolute ceiling
)

// classifyTimeout determines the appropriate timeout based on command content.
func classifyTimeout(command string, requestedTimeout int) int {
	if requestedTimeout > 0 {
		if requestedTimeout > TimeoutMax {
			return TimeoutMax
		}
		return requestedTimeout
	}

	lower := strings.ToLower(command)

	// Install / build / download commands get longer timeout
	installPatterns := []string{
		"pip install", "pip3 install", "python3 -m pip",
		"npm install", "npm ci", "npx create-",
		"yarn add", "yarn install",
		"pnpm install", "pnpm add",
		"go get", "go install", "go build",
		"cargo install", "cargo build",
		"apt install", "apt-get install",
		"brew install",
		"git clone",
		"wget ", "curl -o", "curl -O",
		"make", "cmake",
		"docker build", "docker pull",
	}

	for _, pattern := range installPatterns {
		if strings.Contains(lower, pattern) {
			return TimeoutInstall
		}
	}

	return TimeoutDefault
}

// ──────────────────────────────────────────────────────────────
// Destructive Command Detection
// ──────────────────────────────────────────────────────────────

// DestructiveLevel indicates how dangerous a command is.
type DestructiveLevel int

const (
	// SafeCommand is a normal command with no destructive potential.
	SafeCommand DestructiveLevel = iota
	// CautionCommand might modify state but is generally safe.
	CautionCommand
	// DestructiveCommand can cause data loss or system damage.
	DestructiveCommand
)

var destructivePatterns = []struct {
	pattern *regexp.Regexp
	level   DestructiveLevel
	warning string
}{
	// File deletion
	{regexp.MustCompile(`\brm\s+(-[rRf]+\s+|.*-[rRf])`), DestructiveCommand, "Recursive/force file deletion"},
	{regexp.MustCompile(`\brm\s+`), CautionCommand, "File deletion"},

	// Git destructive
	{regexp.MustCompile(`git\s+reset\s+--hard`), DestructiveCommand, "Discards all uncommitted changes"},
	{regexp.MustCompile(`git\s+push\s+.*--force`), DestructiveCommand, "Force push overwrites remote history"},
	{regexp.MustCompile(`git\s+clean\s+-[fdxX]`), DestructiveCommand, "Removes untracked files permanently"},
	{regexp.MustCompile(`git\s+checkout\s+\.`), CautionCommand, "Discards uncommitted changes in working directory"},
	{regexp.MustCompile(`git\s+stash\s+drop`), CautionCommand, "Permanently deletes stashed changes"},
	{regexp.MustCompile(`git\s+branch\s+-D`), CautionCommand, "Force deletes a branch"},

	// Database
	{regexp.MustCompile(`(?i)\bDROP\s+(TABLE|DATABASE|INDEX|VIEW)\b`), DestructiveCommand, "Drops database objects"},
	{regexp.MustCompile(`(?i)\bTRUNCATE\s+TABLE\b`), DestructiveCommand, "Deletes all rows from table"},
	{regexp.MustCompile(`(?i)\bDELETE\s+FROM\b`), CautionCommand, "Deletes rows from table"},

	// Infrastructure
	{regexp.MustCompile(`kubectl\s+delete`), DestructiveCommand, "Deletes Kubernetes resources"},
	{regexp.MustCompile(`terraform\s+destroy`), DestructiveCommand, "Destroys infrastructure"},
	{regexp.MustCompile(`docker\s+rm\s`), CautionCommand, "Removes Docker containers"},
	{regexp.MustCompile(`docker\s+rmi\s`), CautionCommand, "Removes Docker images"},

	// File overwrites
	{regexp.MustCompile(`>\s*/`), CautionCommand, "Redirects output to absolute path"},
	{regexp.MustCompile(`\bmv\s+`), CautionCommand, "Moves/renames files"},
	{regexp.MustCompile(`\bchmod\s+`), CautionCommand, "Changes file permissions"},
}

// DetectDestructive checks if a command is potentially destructive.
// Returns the highest destructive level and a warning message.
func DetectDestructive(command string) (DestructiveLevel, string) {
	maxLevel := SafeCommand
	var warnings []string

	for _, dp := range destructivePatterns {
		if dp.pattern.MatchString(command) {
			if dp.level > maxLevel {
				maxLevel = dp.level
			}
			warnings = append(warnings, dp.warning)
		}
	}

	if len(warnings) == 0 {
		return SafeCommand, ""
	}
	return maxLevel, strings.Join(warnings, "; ")
}

// ──────────────────────────────────────────────────────────────
// Exit Code Interpretation
// ──────────────────────────────────────────────────────────────

// interpretExitCode provides semantic meaning for common exit codes.
func interpretExitCode(command string, exitCode int) string {
	if exitCode == 0 {
		return ""
	}

	cmd := extractBaseCommand(command)

	switch cmd {
	case "grep", "rg", "ag", "ack":
		if exitCode == 1 {
			return "No matches found"
		}
	case "diff":
		if exitCode == 1 {
			return "Files differ"
		}
	case "test", "[":
		if exitCode == 1 {
			return "Condition is false"
		}
	case "find":
		if exitCode == 1 {
			return "Some directories inaccessible"
		}
	case "curl":
		switch exitCode {
		case 6:
			return "Could not resolve host"
		case 7:
			return "Failed to connect"
		case 28:
			return "Operation timed out"
		}
	case "git":
		if exitCode == 1 {
			return "No changes or no matches"
		}
		if exitCode == 128 {
			return "Fatal error (invalid repo, missing ref, etc.)"
		}
	case "python3", "python":
		if exitCode == 1 {
			return "Python script error"
		}
		if exitCode == 2 {
			return "Python command line syntax error"
		}
	}

	// Generic interpretation
	switch exitCode {
	case 1:
		return "General error"
	case 2:
		return "Misuse of shell command"
	case 126:
		return "Permission denied (cannot execute)"
	case 127:
		return "Command not found"
	case 130:
		return "Interrupted (Ctrl+C)"
	case 137:
		return "Killed (SIGKILL, possibly OOM)"
	case 139:
		return "Segmentation fault"
	case 143:
		return "Terminated (SIGTERM)"
	}

	return ""
}

func extractBaseCommand(command string) string {
	// Strip leading env vars, sudo, nice, etc.
	cmd := strings.TrimSpace(command)
	for {
		if strings.HasPrefix(cmd, "env ") || strings.HasPrefix(cmd, "sudo ") || strings.HasPrefix(cmd, "nohup ") {
			idx := strings.IndexByte(cmd, ' ')
			cmd = strings.TrimSpace(cmd[idx+1:])
			continue
		}
		// nice can have args like "nice -n 10 cmd"
		if strings.HasPrefix(cmd, "nice ") {
			// Skip "nice" and any flags starting with -
			parts := strings.Fields(cmd)
			i := 1
			for i < len(parts) && strings.HasPrefix(parts[i], "-") {
				i++
				// -n takes a value
				if i < len(parts) && !strings.HasPrefix(parts[i], "-") {
					i++
				}
			}
			if i < len(parts) {
				cmd = strings.Join(parts[i:], " ")
				continue
			}
		}
		// Skip env var assignments: FOO=bar command
		if idx := strings.IndexByte(cmd, '='); idx > 0 && idx < strings.IndexByte(cmd, ' ') {
			spaceIdx := strings.IndexByte(cmd, ' ')
			if spaceIdx > 0 {
				cmd = strings.TrimSpace(cmd[spaceIdx+1:])
				continue
			}
		}
		break
	}

	// Extract just the command name (before space or pipe)
	for _, sep := range []string{" ", "\t", "|", ";", "&&", "||"} {
		if idx := strings.Index(cmd, sep); idx > 0 {
			cmd = cmd[:idx]
			break
		}
	}

	return filepath.Base(cmd)
}

// ──────────────────────────────────────────────────────────────
// Auto-Backgrounding
// ──────────────────────────────────────────────────────────────

const (
	// BackgroundThreshold is the time after which a command is auto-backgrounded.
	BackgroundThreshold = 15 * time.Second
)

// BackgroundTask represents a command running in the background.
type BackgroundTask struct {
	ID        string
	Command   string
	StartTime time.Time
	Done      chan ShellResult
	cancel    context.CancelFunc
}

// BackgroundTaskManager tracks background shell tasks.
type BackgroundTaskManager struct {
	mu    sync.Mutex
	tasks map[string]*BackgroundTask
	seq   int
}

// NewBackgroundTaskManager creates a new manager.
func NewBackgroundTaskManager() *BackgroundTaskManager {
	return &BackgroundTaskManager{
		tasks: make(map[string]*BackgroundTask),
	}
}

// RunWithAutoBackground runs a command, auto-backgrounding it if it exceeds the threshold.
// Returns the result immediately if the command completes within the threshold,
// or a backgrounded result with a task ID if it takes longer.
func (m *BackgroundTaskManager) RunWithAutoBackground(
	parentCtx context.Context,
	exec executor.Executor,
	sandboxDir, userDir, command string,
	timeoutSec int,
	env map[string]string,
	onProgress func(status string),
) ShellResult {
	start := time.Now()
	ctx, cancel := context.WithCancel(parentCtx)

	// Run command in goroutine
	resultCh := make(chan ShellResult, 1)
	go func() {
		output, err := exec.Execute(ctx, sandboxDir, userDir, command, timeoutSec, env)
		result := ShellResult{
			Stdout:     output,
			DurationMs: time.Since(start).Milliseconds(),
		}

		if err != nil {
			result.Stderr = err.Error()
			result.ExitCode = 1
			if strings.Contains(err.Error(), "timed out") {
				result.TimedOut = true
			}
		}

		result.Interpretation = interpretExitCode(command, result.ExitCode)
		resultCh <- result
	}()

	// Wait up to threshold
	select {
	case result := <-resultCh:
		cancel()
		return result
	case <-time.After(BackgroundThreshold):
		// Auto-background
		m.mu.Lock()
		m.seq++
		taskID := fmt.Sprintf("sh_%d", m.seq)
		task := &BackgroundTask{
			ID:        taskID,
			Command:   command,
			StartTime: start,
			Done:      resultCh,
			cancel:    cancel,
		}
		m.tasks[taskID] = task
		m.mu.Unlock()

		if onProgress != nil {
			onProgress(fmt.Sprintf("Command running in background (task: %s)", taskID))
		}

		return ShellResult{
			Backgrounded: true,
			TaskID:       taskID,
			DurationMs:   time.Since(start).Milliseconds(),
			Stdout:       fmt.Sprintf("Command backgrounded after %ds. Task ID: %s\nUse tofi_shell with command 'tofi_task_status %s' to check status.", int(BackgroundThreshold.Seconds()), taskID, taskID),
		}
	case <-parentCtx.Done():
		cancel()
		return ShellResult{
			ExitCode: 130,
			Stderr:   "Cancelled",
		}
	}
}

// GetResult checks if a background task has completed.
// Returns the result if done, or nil if still running.
func (m *BackgroundTaskManager) GetResult(taskID string) *ShellResult {
	m.mu.Lock()
	task, ok := m.tasks[taskID]
	m.mu.Unlock()

	if !ok {
		return nil
	}

	select {
	case result := <-task.Done:
		// Clean up
		m.mu.Lock()
		delete(m.tasks, taskID)
		m.mu.Unlock()
		return &result
	default:
		return nil // still running
	}
}

// WaitResult blocks until the background task completes.
func (m *BackgroundTaskManager) WaitResult(taskID string, timeout time.Duration) *ShellResult {
	m.mu.Lock()
	task, ok := m.tasks[taskID]
	m.mu.Unlock()

	if !ok {
		return nil
	}

	select {
	case result := <-task.Done:
		m.mu.Lock()
		delete(m.tasks, taskID)
		m.mu.Unlock()
		return &result
	case <-time.After(timeout):
		return nil
	}
}

// CancelTask cancels a running background task.
func (m *BackgroundTaskManager) CancelTask(taskID string) bool {
	m.mu.Lock()
	task, ok := m.tasks[taskID]
	m.mu.Unlock()

	if !ok {
		return false
	}

	task.cancel()
	return true
}

// ActiveCount returns the number of running background tasks.
func (m *BackgroundTaskManager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.tasks)
}
