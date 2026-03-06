package executor

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DockerExecutor runs commands inside per-user Docker containers.
// Each user gets a persistent container with volume-mounted storage.
type DockerExecutor struct {
	imageName string // Docker image name (e.g. "tofi-sandbox:latest")
	dataDir   string // Host data directory for volume mounts
}

// NewDockerExecutor creates a DockerExecutor after verifying Docker is available.
func NewDockerExecutor(dataDir, imageName string) (*DockerExecutor, error) {
	// Check Docker CLI is available
	if err := exec.Command("docker", "info").Run(); err != nil {
		return nil, fmt.Errorf("docker is not available: %v (is Docker running?)", err)
	}
	return &DockerExecutor{
		imageName: imageName,
		dataDir:   dataDir,
	}, nil
}

// containerName returns the Docker container name for a user.
func (d *DockerExecutor) containerName(userID string) string {
	if userID == "" {
		userID = "default"
	}
	return "tofi-sandbox-" + userID
}

// ensureContainer makes sure the user's container exists and is running.
func (d *DockerExecutor) ensureContainer(userID string) error {
	name := d.containerName(userID)

	// Check if container exists
	out, err := exec.Command("docker", "inspect", "--format", "{{.State.Running}}", name).Output()
	if err == nil {
		// Container exists
		if strings.TrimSpace(string(out)) == "true" {
			return nil // already running
		}
		// Exists but stopped — start it
		return exec.Command("docker", "start", name).Run()
	}

	// Container doesn't exist — create and start it
	userDir := filepath.Join(d.dataDir, "users", userID)
	args := []string{
		"run", "-d",
		"--name", name,
		"-v", userDir + ":/home/user",
		"--memory", "512m",
		"--cpus", "1",
		d.imageName,
	}
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create container %s: %v\n%s", name, err, string(out))
	}
	return nil
}

// CreateSandbox ensures the user's container is running and creates a task workspace.
func (d *DockerExecutor) CreateSandbox(cfg SandboxConfig) (string, error) {
	if err := d.ensureContainer(cfg.UserID); err != nil {
		return "", fmt.Errorf("failed to ensure container: %v", err)
	}

	// Create task workspace inside container
	sandboxPath := "/workspace/" + cfg.CardID
	name := d.containerName(cfg.UserID)
	if out, err := exec.Command("docker", "exec", name, "mkdir", "-p", sandboxPath+"/tmp").CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to create workspace: %v\n%s", err, string(out))
	}

	return sandboxPath, nil
}

// Execute runs a command inside the user's container.
// No ValidateCommand needed — the container itself provides isolation.
func (d *DockerExecutor) Execute(ctx context.Context, sandboxPath, userDir, command string, timeoutSec int) (string, error) {
	if timeoutSec <= 0 {
		timeoutSec = 60
	}
	if timeoutSec > MaxTimeout {
		timeoutSec = MaxTimeout
	}

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	// Extract userID from userDir or use default
	userID := "default"
	if userDir != "" {
		userID = filepath.Base(userDir)
	}
	name := d.containerName(userID)

	cmd := exec.CommandContext(execCtx, "docker", "exec", "-w", sandboxPath, name, "sh", "-c", command)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &stdout, limit: MaxOutputBytes}
	cmd.Stderr = &limitedWriter{w: &stderr, limit: MaxOutputBytes}

	err := cmd.Run()

	output := stdout.String()
	if errOut := stderr.String(); errOut != "" {
		if output != "" {
			output += "\n"
		}
		output += errOut
	}

	if execCtx.Err() == context.DeadlineExceeded {
		return output, fmt.Errorf("command timed out after %d seconds", timeoutSec)
	}

	if err != nil {
		return output, fmt.Errorf("command failed: %v\n%s", err, output)
	}

	return strings.TrimRight(output, "\n"), nil
}

// Cleanup removes the task workspace inside the container (not the container itself).
func (d *DockerExecutor) Cleanup(sandboxPath string) {
	if sandboxPath == "" || !strings.HasPrefix(sandboxPath, "/workspace/") {
		return
	}
	// Extract userID — we don't have it here, so clean up from all running containers
	// In practice, the caller should track which container owns which sandbox
	containers, err := exec.Command("docker", "ps", "--filter", "name=tofi-sandbox-", "--format", "{{.Names}}").Output()
	if err != nil {
		return
	}
	for _, name := range strings.Split(strings.TrimSpace(string(containers)), "\n") {
		if name == "" {
			continue
		}
		_ = exec.Command("docker", "exec", name, "rm", "-rf", sandboxPath).Run()
	}
}
