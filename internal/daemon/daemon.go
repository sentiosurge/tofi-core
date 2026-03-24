// Package daemon manages the Tofi engine background process lifecycle.
// It handles PID file management, process spawning, and health checking
// via the engine's HTTP API on localhost.
package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	PIDFile      = "tofi.pid"
	HealthPath   = "/health"
	StatsPath    = "/api/v1/stats"
	StartTimeout = 10 * time.Second
	StopTimeout  = 30 * time.Second
)

// GetDefaultPort returns the default port from TOFI_PORT env var, or 8321.
func GetDefaultPort() int {
	if p := os.Getenv("TOFI_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil && port > 0 {
			return port
		}
	}
	return 8321
}

// Status represents the current engine state.
type Status struct {
	Running bool   `json:"running"`
	PID     int    `json:"pid,omitempty"`
	Port    int    `json:"port,omitempty"`
	Uptime  string `json:"uptime,omitempty"`
	Version string `json:"version,omitempty"`
}

// EngineStats is the response from /api/v1/stats.
type EngineStats struct {
	ActiveWorkers int    `json:"active_workers"`
	MaxWorkers    int    `json:"max_workers"`
	QueueSize     int    `json:"queue_size"`
	Uptime        string `json:"uptime"`
}

// pidPath returns the full path to the PID file.
func pidPath(homeDir string) string {
	return filepath.Join(homeDir, PIDFile)
}

// ReadPID reads the PID from the PID file. Returns 0 if not found.
func ReadPID(homeDir string) (int, error) {
	data, err := os.ReadFile(pidPath(homeDir))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("invalid PID file content: %w", err)
	}
	return pid, nil
}

// WritePID writes the current process PID to the PID file.
func WritePID(homeDir string, pid int) error {
	return os.WriteFile(pidPath(homeDir), []byte(strconv.Itoa(pid)), 0644)
}

// RemovePID removes the PID file.
func RemovePID(homeDir string) {
	os.Remove(pidPath(homeDir))
}

// IsRunning checks if a process with the given PID is alive.
func IsRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess always succeeds. Send signal 0 to check.
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

// CheckHealth pings the engine's health endpoint.
func CheckHealth(port int) bool {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d%s", port, HealthPath))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// GetStats fetches engine statistics from the stats endpoint.
func GetStats(port int) (*EngineStats, error) {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d%s", port, StatsPath))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var stats EngineStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, err
	}
	return &stats, nil
}

// GetStatus returns the full engine status.
func GetStatus(homeDir string, port int) Status {
	pid, _ := ReadPID(homeDir)
	if pid == 0 || !IsRunning(pid) {
		RemovePID(homeDir) // Clean up stale PID
		return Status{Running: false}
	}

	s := Status{
		Running: true,
		PID:     pid,
		Port:    port,
	}

	if stats, err := GetStats(port); err == nil {
		s.Uptime = stats.Uptime
	}

	return s
}

// Start launches the engine as a background daemon process.
// It re-execs the same binary with "start --foreground" and detaches.
func Start(homeDir string, port int, foreground bool) (int, error) {
	// Check if already running
	pid, _ := ReadPID(homeDir)
	if pid > 0 && IsRunning(pid) {
		return pid, fmt.Errorf("engine already running (pid %d)", pid)
	}

	// Clean up stale PID
	RemovePID(homeDir)

	if foreground {
		// In foreground mode, the caller handles the server directly.
		// Just write PID and return.
		WritePID(homeDir, os.Getpid())
		return os.Getpid(), nil
	}

	// Find our own executable
	exe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("cannot find executable: %w", err)
	}

	// Launch as background process
	cmd := exec.Command(exe, "start", "--foreground",
		"--home", homeDir,
		"--port", strconv.Itoa(port))

	// Redirect output to log file
	logFile := filepath.Join(homeDir, "logs", "engine.log")
	os.MkdirAll(filepath.Join(homeDir, "logs"), 0755)
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return 0, fmt.Errorf("cannot open log file: %w", err)
	}

	cmd.Stdout = f
	cmd.Stderr = f
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true, // Detach from terminal
	}

	if err := cmd.Start(); err != nil {
		f.Close()
		return 0, fmt.Errorf("failed to start engine: %w", err)
	}
	f.Close()

	pid = cmd.Process.Pid

	// Wait for health check
	deadline := time.Now().Add(StartTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		if CheckHealth(port) {
			return pid, nil
		}
	}

	return pid, fmt.Errorf("engine started (pid %d) but health check timed out", pid)
}

// Stop sends a graceful shutdown signal to the engine.
func Stop(homeDir string, force bool) error {
	pid, err := ReadPID(homeDir)
	if err != nil {
		return err
	}
	if pid == 0 {
		return fmt.Errorf("engine is not running (no PID file)")
	}
	if !IsRunning(pid) {
		RemovePID(homeDir)
		return fmt.Errorf("engine is not running (stale PID %d)", pid)
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}

	if force {
		if err := process.Signal(syscall.SIGKILL); err != nil {
			return fmt.Errorf("failed to kill process: %w", err)
		}
		RemovePID(homeDir)
		return nil
	}

	// Graceful: SIGTERM
	if err := process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("failed to send stop signal: %w", err)
	}

	// Wait for process to exit
	deadline := time.Now().Add(StopTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		if !IsRunning(pid) {
			RemovePID(homeDir)
			return nil
		}
	}

	// Timeout — force kill
	process.Signal(syscall.SIGKILL)
	RemovePID(homeDir)
	return fmt.Errorf("engine did not stop gracefully, force killed")
}
