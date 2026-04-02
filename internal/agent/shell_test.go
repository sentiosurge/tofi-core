package agent

import (
	"strings"
	"testing"
)

func TestClassifyTimeout(t *testing.T) {
	tests := []struct {
		command   string
		requested int
		want      int
	}{
		// Default timeout for normal commands
		{"echo hello", 0, TimeoutDefault},
		{"ls -la", 0, TimeoutDefault},
		{"python3 script.py", 0, TimeoutDefault},

		// Install commands get longer timeout
		{"pip install pandas", 0, TimeoutInstall},
		{"pip3 install -r requirements.txt", 0, TimeoutInstall},
		{"python3 -m pip install flask", 0, TimeoutInstall},
		{"npm install express", 0, TimeoutInstall},
		{"npm ci", 0, TimeoutInstall},
		{"npx create-react-app myapp", 0, TimeoutInstall},
		{"yarn add lodash", 0, TimeoutInstall},
		{"go get github.com/gin-gonic/gin", 0, TimeoutInstall},
		{"cargo build", 0, TimeoutInstall},
		{"git clone https://github.com/foo/bar", 0, TimeoutInstall},
		{"docker build -t myapp .", 0, TimeoutInstall},
		{"make", 0, TimeoutInstall},
		{"wget https://example.com/file.zip", 0, TimeoutInstall},

		// Explicit timeout respected
		{"echo hello", 30, 30},
		{"pip install pandas", 90, 90},

		// Explicit timeout capped at max
		{"echo hello", 9999, TimeoutMax},
	}

	for _, tt := range tests {
		got := classifyTimeout(tt.command, tt.requested)
		if got != tt.want {
			t.Errorf("classifyTimeout(%q, %d) = %d, want %d", tt.command, tt.requested, got, tt.want)
		}
	}
}

func TestDetectDestructive(t *testing.T) {
	tests := []struct {
		command string
		level   DestructiveLevel
	}{
		// Safe
		{"echo hello", SafeCommand},
		{"ls -la", SafeCommand},
		{"cat file.txt", SafeCommand},
		{"python3 script.py", SafeCommand},
		{"git status", SafeCommand},
		{"git add .", SafeCommand},
		{"git commit -m 'test'", SafeCommand},

		// Caution
		{"rm file.txt", CautionCommand},
		{"mv old.txt new.txt", CautionCommand},
		{"chmod 755 script.sh", CautionCommand},
		{"git checkout .", CautionCommand},
		{"git stash drop", CautionCommand},
		{"git branch -D feature", CautionCommand},
		{"DELETE FROM users WHERE id = 5", CautionCommand},
		{"docker rm container1", CautionCommand},

		// Destructive
		{"rm -rf /tmp/data", DestructiveCommand},
		{"rm -f important.db", DestructiveCommand},
		{"git reset --hard HEAD~3", DestructiveCommand},
		{"git push --force origin main", DestructiveCommand},
		{"git clean -fd", DestructiveCommand},
		{"DROP TABLE users", DestructiveCommand},
		{"TRUNCATE TABLE logs", DestructiveCommand},
		{"kubectl delete pod myapp", DestructiveCommand},
		{"terraform destroy", DestructiveCommand},
	}

	for _, tt := range tests {
		level, warning := DetectDestructive(tt.command)
		if level != tt.level {
			t.Errorf("DetectDestructive(%q) level = %d, want %d (warning: %s)", tt.command, level, tt.level, warning)
		}
	}
}

func TestInterpretExitCode(t *testing.T) {
	tests := []struct {
		command string
		code    int
		want    string
	}{
		// Success
		{"echo hello", 0, ""},
		{"grep pattern file", 0, ""},

		// grep no match = not an error
		{"grep pattern file", 1, "No matches found"},
		{"rg pattern", 1, "No matches found"},

		// diff = files differ
		{"diff a.txt b.txt", 1, "Files differ"},

		// git
		{"git status", 1, "No changes or no matches"},
		{"git push origin main", 128, "Fatal error (invalid repo, missing ref, etc.)"},

		// curl
		{"curl https://example.com", 7, "Failed to connect"},
		{"curl https://example.com", 28, "Operation timed out"},

		// Generic codes
		{"some_cmd", 126, "Permission denied (cannot execute)"},
		{"some_cmd", 127, "Command not found"},
		{"some_cmd", 137, "Killed (SIGKILL, possibly OOM)"},
	}

	for _, tt := range tests {
		got := interpretExitCode(tt.command, tt.code)
		if got != tt.want {
			t.Errorf("interpretExitCode(%q, %d) = %q, want %q", tt.command, tt.code, got, tt.want)
		}
	}
}

func TestExtractBaseCommand(t *testing.T) {
	tests := []struct {
		command string
		want    string
	}{
		{"echo hello", "echo"},
		{"python3 script.py", "python3"},
		{"env FOO=bar python3 script.py", "python3"},
		{"nice -n 10 python3 script.py", "python3"},
		{"FOO=bar BAZ=qux grep pattern", "grep"},
		{"git status", "git"},
		{"ls -la | head", "ls"},
		{"/usr/bin/python3 script.py", "python3"},
	}

	for _, tt := range tests {
		got := extractBaseCommand(tt.command)
		if got != tt.want {
			t.Errorf("extractBaseCommand(%q) = %q, want %q", tt.command, got, tt.want)
		}
	}
}

func TestShellResult_FormatForAgent(t *testing.T) {
	// Normal success
	r := ShellResult{Stdout: "hello world", ExitCode: 0, DurationMs: 50}
	if r.FormatForAgent() != "hello world" {
		t.Errorf("success format wrong: %q", r.FormatForAgent())
	}

	// Error with interpretation
	r = ShellResult{Stdout: "", ExitCode: 127, DurationMs: 10, Interpretation: "Command not found"}
	formatted := r.FormatForAgent()
	if formatted != "\n[Exit 127: Command not found]" {
		t.Errorf("error format wrong: %q", formatted)
	}

	// Timeout
	r = ShellResult{Stdout: "partial output", TimedOut: true, DurationMs: 60000}
	formatted = r.FormatForAgent()
	if !contains(formatted, "Timed out") {
		t.Errorf("timeout format should mention timeout: %q", formatted)
	}

	// Backgrounded
	r = ShellResult{Backgrounded: true, TaskID: "sh_1", Stdout: "Command backgrounded"}
	formatted = r.FormatForAgent()
	if !contains(formatted, "sh_1") {
		t.Errorf("background format should mention task ID: %q", formatted)
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
