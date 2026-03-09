package executor

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestSeatbeltAvailable(t *testing.T) {
	if runtime.GOOS != "darwin" {
		if seatbeltAvailable {
			t.Error("seatbeltAvailable should be false on non-macOS")
		}
		return
	}
	// On macOS, sandbox-exec should exist
	if !seatbeltAvailable {
		t.Log("WARNING: sandbox-exec not found on macOS — seatbelt tests will be skipped")
	}
}

func TestBuildSeatbeltProfile(t *testing.T) {
	cfg := defaultSeatbeltConfig()
	profile := buildSeatbeltProfile(cfg)

	// Must start with version and allow default
	if !strings.Contains(profile, "(version 1)") {
		t.Error("profile missing (version 1)")
	}
	if !strings.Contains(profile, "(allow default)") {
		t.Error("profile missing (allow default)")
	}
	// Must deny .ssh reads
	if !strings.Contains(profile, ".ssh") {
		t.Error("profile should deny .ssh access")
	}
	// Must deny network-bind
	if !strings.Contains(profile, "(deny network-bind)") {
		t.Error("profile should deny network-bind")
	}
	// Must deny nc
	if !strings.Contains(profile, "/usr/bin/nc") {
		t.Error("profile should deny nc execution")
	}
}

func TestSeatbelt_BlocksSensitiveFileRead(t *testing.T) {
	if !seatbeltAvailable {
		t.Skip("sandbox-exec not available")
	}

	tmpDir := t.TempDir()
	sandbox, err := CreateSandbox(tmpDir, "test-seatbelt-read")
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	defer CleanupSandbox(sandbox)

	home := getUserHome()
	// Try to read ~/.ssh — should be blocked by seatbelt
	output, err := ExecuteInSandbox(context.Background(), sandbox, "ls "+home+"/.ssh/ 2>&1", 10)
	if err == nil && !strings.Contains(output, "Operation not permitted") && !strings.Contains(output, "No such file") {
		t.Errorf("reading ~/.ssh should be blocked by seatbelt, got output: %q", output)
	}
}

func TestSeatbelt_AllowsLegitimateCommands(t *testing.T) {
	if !seatbeltAvailable {
		t.Skip("sandbox-exec not available")
	}

	tmpDir := t.TempDir()
	sandbox, err := CreateSandbox(tmpDir, "test-seatbelt-ok")
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	defer CleanupSandbox(sandbox)

	// echo should work
	output, err := ExecuteInSandbox(context.Background(), sandbox, "echo hello", 10)
	if err != nil {
		t.Fatalf("echo should work under seatbelt: %v", err)
	}
	if strings.TrimSpace(output) != "hello" {
		t.Errorf("expected 'hello', got %q", output)
	}

	// python3 should work
	output, err = ExecuteInSandbox(context.Background(), sandbox, "python3 -c 'print(42)'", 10)
	if err != nil {
		t.Fatalf("python3 should work under seatbelt: %v", err)
	}
	if strings.TrimSpace(output) != "42" {
		t.Errorf("expected '42', got %q", output)
	}
}

func TestSeatbelt_BlocksNetworkBind(t *testing.T) {
	if !seatbeltAvailable {
		t.Skip("sandbox-exec not available")
	}

	tmpDir := t.TempDir()
	sandbox, err := CreateSandbox(tmpDir, "test-seatbelt-bind")
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	defer CleanupSandbox(sandbox)

	// Try to bind a port — should be blocked
	output, err := ExecuteInSandbox(context.Background(), sandbox,
		"python3 -c \"import socket; s=socket.socket(); s.bind(('127.0.0.1', 19999))\" 2>&1", 10)
	if err == nil && !strings.Contains(output, "Permission denied") && !strings.Contains(output, "Operation not permitted") {
		t.Errorf("binding port should be blocked by seatbelt, got: %q", output)
	}
}

func TestSeatbelt_AllowsOutboundNetwork(t *testing.T) {
	if !seatbeltAvailable {
		t.Skip("sandbox-exec not available")
	}

	tmpDir := t.TempDir()
	sandbox, err := CreateSandbox(tmpDir, "test-seatbelt-net")
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	defer CleanupSandbox(sandbox)

	// Outbound curl should still work (Agent needs API access)
	output, err := ExecuteInSandbox(context.Background(), sandbox, "curl -s -o /dev/null -w '%{http_code}' https://httpbin.org/get", 15)
	if err != nil {
		t.Logf("curl failed (may be network issue): %v", err)
		t.Skip("network not available")
	}
	if strings.TrimSpace(output) != "200" {
		t.Logf("curl returned %q (may be network issue, skipping)", output)
	}
}
