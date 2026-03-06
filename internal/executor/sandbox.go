package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// MaxOutputBytes is the maximum output size from a sandbox command (1MB)
const MaxOutputBytes = 1 << 20

// MaxTimeout is the maximum allowed execution timeout in seconds
const MaxTimeout = 120

// blockedPrefixes are command prefixes that are never allowed (truly dangerous)
var blockedPrefixes = []string{
	"sudo ", "sudo\t",
	"su ", "su\t",
	"rm -rf /",
	"rm -rf /*",
	"mkfs",
	"fdisk",
	"mount ", "umount ",
	"shutdown", "reboot", "halt", "poweroff",
	"iptables", "systemctl ", "service ",
	"> /dev/", "< /dev/",
	"eval ", "exec ",
	"kill -9 1", "kill -9 -1",
	":(){ :|:& };:", // fork bomb
}

// ─── DirectExecutor ─────────────────────────────────────────

// DirectExecutor runs commands directly on the host via sh -c.
// It inherits the full host PATH and uses a shared packages directory.
type DirectExecutor struct {
	homeDir string // tofi data directory for shared packages
}

// NewDirectExecutor creates a new DirectExecutor.
func NewDirectExecutor(homeDir string) *DirectExecutor {
	return &DirectExecutor{homeDir: homeDir}
}

// CreateSandbox creates an isolated directory structure for command execution.
// Creates a task-level sandbox and ensures the shared packages directory exists.
func (d *DirectExecutor) CreateSandbox(cfg SandboxConfig) (string, error) {
	// 1. Ensure shared packages directory exists
	pkgDir := filepath.Join(cfg.HomeDir, "packages")
	for _, sub := range []string{".local/bin", "node_modules/.bin", ".pip"} {
		if err := os.MkdirAll(filepath.Join(pkgDir, sub), 0755); err != nil {
			return "", fmt.Errorf("failed to create packages directory %s: %v", sub, err)
		}
	}

	// 2. Create task sandbox directory
	sandboxDir := filepath.Join(cfg.HomeDir, "sandbox", cfg.CardID)
	if err := os.MkdirAll(filepath.Join(sandboxDir, "tmp"), 0755); err != nil {
		return "", fmt.Errorf("failed to create sandbox directory: %v", err)
	}

	return sandboxDir, nil
}

// Execute runs a shell command inside the sandbox directory.
// Inherits the full host PATH with shared packages prepended.
func (d *DirectExecutor) Execute(ctx context.Context, sandboxPath, userDir, command string, timeoutSec int) (string, error) {
	homeDir := d.homeDir
	if homeDir == "" {
		// Infer from sandboxPath: {homeDir}/sandbox/{cardID}
		homeDir = filepath.Dir(filepath.Dir(sandboxPath))
	}
	return executeInSandboxInternal(ctx, sandboxPath, homeDir, command, timeoutSec)
}

// Cleanup removes the task-level sandbox directory and all its contents.
func (d *DirectExecutor) Cleanup(sandboxPath string) {
	if sandboxPath == "" {
		return
	}
	// Safety: only remove paths that contain "/sandbox/"
	if !strings.Contains(sandboxPath, "/sandbox/") {
		return
	}
	os.RemoveAll(sandboxPath)
}

// ─── Shared internal implementation ─────────────────────────

func executeInSandboxInternal(parentCtx context.Context, sandboxPath, homeDir, command string, timeoutSec int) (string, error) {
	// Enforce timeout ceiling
	if timeoutSec <= 0 {
		timeoutSec = 60
	}
	if timeoutSec > MaxTimeout {
		timeoutSec = MaxTimeout
	}

	ctx, cancel := context.WithTimeout(parentCtx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = sandboxPath

	// Build PATH: shared packages bins → full host PATH
	hostPath := os.Getenv("PATH")
	if hostPath == "" {
		hostPath = "/usr/local/bin:/usr/bin:/bin"
	}

	pkgDir := filepath.Join(homeDir, "packages")
	sandboxBinPath := strings.Join([]string{
		filepath.Join(pkgDir, ".local/bin"),
		filepath.Join(pkgDir, "node_modules/.bin"),
		hostPath,
	}, ":")

	// Build environment: inherit host capabilities, isolate file writes
	env := []string{
		"HOME=" + sandboxPath,
		"PATH=" + sandboxBinPath,
		"TMPDIR=" + filepath.Join(sandboxPath, "tmp"),
		"LANG=en_US.UTF-8",
		"TERM=dumb",
		// Package manager targets: shared packages directory (persistent across tasks)
		"NODE_PATH=" + filepath.Join(pkgDir, "node_modules"),
		"PIP_TARGET=" + filepath.Join(pkgDir, ".pip"),
		"PYTHONPATH=" + filepath.Join(pkgDir, ".pip"),
		"UV_CACHE_DIR=" + filepath.Join(pkgDir, ".cache", "uv"),
		"NPM_CONFIG_PREFIX=" + pkgDir,
	}

	cmd.Env = env

	// Limit output size
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &stdout, limit: MaxOutputBytes}
	cmd.Stderr = &limitedWriter{w: &stderr, limit: MaxOutputBytes}

	err := cmd.Run()

	// Combine output
	output := stdout.String()
	if errOut := stderr.String(); errOut != "" {
		if output != "" {
			output += "\n"
		}
		output += errOut
	}

	// Check for timeout
	if ctx.Err() == context.DeadlineExceeded {
		return output, fmt.Errorf("command timed out after %d seconds", timeoutSec)
	}

	if err != nil {
		return output, fmt.Errorf("command failed: %v\n%s", err, output)
	}

	return strings.TrimRight(output, "\n"), nil
}

// ─── Legacy compatibility wrappers ──────────────────────────

// CreateSandbox creates an isolated directory for command execution (legacy).
func CreateSandbox(homeDir, cardID string) (string, error) {
	return NewDirectExecutor(homeDir).CreateSandbox(SandboxConfig{
		HomeDir: homeDir,
		CardID:  cardID,
	})
}

// ExecuteInSandbox runs a command in the sandbox (legacy).
func ExecuteInSandbox(parentCtx context.Context, sandboxPath, command string, timeoutSec int) (string, error) {
	// Infer homeDir from sandboxPath
	homeDir := filepath.Dir(filepath.Dir(sandboxPath))
	return executeInSandboxInternal(parentCtx, sandboxPath, homeDir, command, timeoutSec)
}

// CleanupSandbox removes the sandbox directory (legacy).
func CleanupSandbox(sandboxPath string) {
	NewDirectExecutor("").Cleanup(sandboxPath)
}

// ─── ValidateCommand ────────────────────────────────────────

// ValidateCommand performs basic security checks on a command before execution.
// Only blocks truly dangerous system commands. Returns nil if safe.
func ValidateCommand(command, sandboxPath string) error {
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("empty command")
	}

	lower := strings.ToLower(command)

	// Check blocked command prefixes
	for _, prefix := range blockedPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return fmt.Errorf("blocked command: '%s' is not allowed", prefix)
		}
		// Also check after shell operators: ; && || |
		for _, sep := range []string{"; ", "&& ", "|| ", "| "} {
			if strings.Contains(lower, sep+prefix) {
				return fmt.Errorf("blocked command: '%s' is not allowed (after '%s')", prefix, strings.TrimSpace(sep))
			}
		}
	}

	return nil
}

// ─── limitedWriter ──────────────────────────────────────────

// limitedWriter wraps a writer and stops writing after limit bytes.
type limitedWriter struct {
	w       io.Writer
	limit   int
	written int
}

func (lw *limitedWriter) Write(p []byte) (n int, err error) {
	if lw.written >= lw.limit {
		return len(p), nil // silently discard
	}
	remaining := lw.limit - lw.written
	if len(p) > remaining {
		p = p[:remaining]
	}
	n, err = lw.w.Write(p)
	lw.written += n
	return len(p), err // report full write to avoid broken pipe
}
