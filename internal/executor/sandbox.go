package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// blockedPatterns detects dangerous patterns anywhere in a command via regex.
// This is a best-effort layer — OS-level sandbox-exec is the true defense.
var blockedPatterns = []*regexp.Regexp{
	// Pipe-to-shell (remote code execution)
	regexp.MustCompile(`\|\s*(sh|bash|zsh|dash)\b`),
	regexp.MustCompile(`\|\s*python3?\s`),

	// Reverse shell patterns
	regexp.MustCompile(`\bnc\s+.*-e\b`),
	regexp.MustCompile(`\bncat\s+.*-e\b`),
	regexp.MustCompile(`/dev/tcp/`),
	regexp.MustCompile(`/dev/udp/`),
	regexp.MustCompile(`\bmkfifo\b.*\bnc\b`),
	regexp.MustCompile(`\bsocat\b.*\bexec\b`),

	// Data exfiltration (curl/wget with command substitution)
	regexp.MustCompile(`(curl|wget)\s+.*\$\(`),
	regexp.MustCompile(`(curl|wget)\s+.*-d\s+@/`),

	// Sensitive file access
	regexp.MustCompile(`\.(ssh|aws|gnupg|kube)/`),
	regexp.MustCompile(`Library/Keychains`),

	// Encoded bypass: base64 decode | shell
	regexp.MustCompile(`base64\s+-[dD]\b.*\|\s*(sh|bash)\b`),
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
func (d *DirectExecutor) Execute(ctx context.Context, sandboxPath, userDir, command string, timeoutSec int, env map[string]string) (string, error) {
	homeDir := d.homeDir
	if homeDir == "" {
		// Infer from sandboxPath: {homeDir}/sandbox/{cardID}
		homeDir = filepath.Dir(filepath.Dir(sandboxPath))
	}
	return executeInSandboxInternal(ctx, sandboxPath, homeDir, command, timeoutSec, env)
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

// buildSafePATH constructs a whitelisted PATH instead of inheriting the full host PATH.
// Includes shared packages, standard system paths, and version managers (pyenv/nvm).
func buildSafePATH(pkgDir string) string {
	paths := []string{
		filepath.Join(pkgDir, ".local/bin"),
		filepath.Join(pkgDir, "node_modules/.bin"),
		"/opt/homebrew/bin",
		"/opt/homebrew/sbin",
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
		"/usr/sbin",
		"/sbin",
	}
	// Preserve version manager paths (pyenv, nvm, volta) from host
	for _, p := range strings.Split(os.Getenv("PATH"), ":") {
		if strings.Contains(p, ".pyenv") ||
			strings.Contains(p, ".nvm") ||
			strings.Contains(p, ".volta") {
			paths = append(paths, p)
		}
	}
	return strings.Join(paths, ":")
}

// logCommandAudit writes a timestamped entry to the sandbox audit log.
func logCommandAudit(sandboxPath, command string) {
	auditPath := filepath.Join(sandboxPath, "audit.log")
	f, err := os.OpenFile(auditPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "[%s] %s\n", time.Now().Format(time.RFC3339), command)
}

func executeInSandboxInternal(parentCtx context.Context, sandboxPath, homeDir, command string, timeoutSec int, extraEnv map[string]string) (string, error) {
	// Enforce timeout ceiling
	if timeoutSec <= 0 {
		timeoutSec = 60
	}
	if timeoutSec > MaxTimeout {
		timeoutSec = MaxTimeout
	}

	// Fix unbalanced quotes (common LLM generation artifact)
	command = fixUnbalancedQuotes(command)

	// Audit log
	logCommandAudit(sandboxPath, command)

	ctx, cancel := context.WithTimeout(parentCtx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	// Layer 3: Use macOS sandbox-exec if available
	var cmd *exec.Cmd
	if seatbeltAvailable {
		profile := buildSeatbeltProfile(defaultSeatbeltConfig())
		cmd = exec.CommandContext(ctx, "sandbox-exec", "-p", profile, "sh", "-c", command)
		log.Printf("🔒 [sandbox] Executing with seatbelt: %.80s...", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Dir = sandboxPath

	// Layer 2: Build safe PATH (whitelist instead of full host PATH)
	pkgDir := filepath.Join(homeDir, "packages")
	sandboxBinPath := buildSafePATH(pkgDir)

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

	// Inject extra environment variables (e.g., skill secrets)
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
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
	return executeInSandboxInternal(parentCtx, sandboxPath, homeDir, command, timeoutSec, nil)
}

// CleanupSandbox removes the sandbox directory (legacy).
func CleanupSandbox(sandboxPath string) {
	NewDirectExecutor("").Cleanup(sandboxPath)
}

// fixUnbalancedQuotes removes trailing unmatched double quotes from commands.
// LLMs sometimes generate commands with an extra trailing " (e.g. `--flag value"`),
// which causes sh to fail with "Unterminated quoted string".
func fixUnbalancedQuotes(command string) string {
	n := strings.Count(command, `"`)
	if n%2 != 0 && strings.HasSuffix(strings.TrimSpace(command), `"`) {
		command = strings.TrimSpace(command)
		command = command[:len(command)-1]
	}
	return command
}

// ─── ValidateCommand ────────────────────────────────────────

// ValidateCommand performs security checks on a command before execution.
// Layer 1: blocks dangerous prefixes and regex patterns.
// This is best-effort — OS-level sandbox-exec (Layer 3) is the true defense.
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

	// Check blocked patterns (regex-based detection)
	for _, pattern := range blockedPatterns {
		if pattern.MatchString(command) {
			return fmt.Errorf("blocked pattern: potentially dangerous command detected")
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
