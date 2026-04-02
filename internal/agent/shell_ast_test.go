package agent

import (
	"testing"
)

func TestParseCommandsAST(t *testing.T) {
	tests := []struct {
		command  string
		wantCmds []string // expected command names
	}{
		// Simple
		{"echo hello", []string{"echo"}},
		{"ls -la", []string{"ls"}},

		// Pipeline
		{"cat file.txt | grep pattern | head -5", []string{"cat", "grep", "head"}},

		// Compound: && and ||
		{"cd /tmp && rm -rf build && echo done", []string{"cd", "rm", "echo"}},

		// Variable assignment — NOT a command
		{"rm=myvar", []string{}}, // should produce 0 or skip assignment

		// Subshell
		{"echo $(date)", []string{"echo", "date"}},

		// Semicolons
		{"echo a; echo b; echo c", []string{"echo", "echo", "echo"}},

		// With env vars
		{"FOO=bar python3 script.py", []string{"python3"}},
	}

	for _, tt := range tests {
		cmds := parseCommandsAST(tt.command)
		gotNames := make([]string, len(cmds))
		for i, c := range cmds {
			gotNames[i] = c.Name
		}

		if len(gotNames) != len(tt.wantCmds) {
			t.Errorf("parseCommandsAST(%q): got %d commands %v, want %d commands %v",
				tt.command, len(gotNames), gotNames, len(tt.wantCmds), tt.wantCmds)
			continue
		}
		for i, want := range tt.wantCmds {
			if gotNames[i] != want {
				t.Errorf("parseCommandsAST(%q)[%d] = %q, want %q",
					tt.command, i, gotNames[i], want)
			}
		}
	}
}

func TestDetectDestructiveAST_FalsePositives(t *testing.T) {
	// These should NOT be flagged as destructive — regex would false-positive on these
	safeCommands := []string{
		`rm=myvar`,                           // assignment, not deletion
		`echo "rm -rf /"`,                    // string literal
		`echo 'DROP TABLE users'`,            // string literal
		`grep "rm -rf" logfile.txt`,          // searching for the pattern
		`export rm=/usr/bin/rm`,              // env var
	}

	for _, cmd := range safeCommands {
		level, warning := DetectDestructiveAST(cmd)
		if level >= DestructiveCommand {
			t.Errorf("DetectDestructiveAST(%q) = Destructive (%s), should be Safe (regex false positive avoided)",
				cmd, warning)
		}
	}
}

func TestDetectDestructiveAST_TruePositives(t *testing.T) {
	// These SHOULD be flagged
	tests := []struct {
		command string
		level   DestructiveLevel
	}{
		{"rm -rf /tmp/build", DestructiveCommand},
		{"rm -f important.db", DestructiveCommand},
		{"git reset --hard HEAD", DestructiveCommand},
		{"git push --force origin main", DestructiveCommand},
		{"git clean -fd", DestructiveCommand},
		{"kubectl delete pod myapp", DestructiveCommand},
		{"terraform destroy", DestructiveCommand},

		// In pipelines — should still detect
		{"ls | xargs rm -rf", DestructiveCommand},
		{"echo yes | git push --force", DestructiveCommand},

		// In compound — should detect the dangerous part
		{"cd /tmp && rm -rf build", DestructiveCommand},

		// Caution level
		{"rm file.txt", CautionCommand},
		{"mv old.txt new.txt", CautionCommand},
		{"git checkout .", CautionCommand},
	}

	for _, tt := range tests {
		level, _ := DetectDestructiveAST(tt.command)
		if level < tt.level {
			t.Errorf("DetectDestructiveAST(%q) = level %d, want >= %d",
				tt.command, level, tt.level)
		}
	}
}

func TestDetectDestructiveAST_SubshellDetection(t *testing.T) {
	// Commands in subshells should still be detected
	tests := []struct {
		command string
		level   DestructiveLevel
	}{
		{"$(rm -rf /tmp/data)", DestructiveCommand},
		{"result=$(git reset --hard)", DestructiveCommand},
	}

	for _, tt := range tests {
		level, _ := DetectDestructiveAST(tt.command)
		if level < tt.level {
			t.Errorf("DetectDestructiveAST(%q) = level %d, want >= %d (subshell)",
				tt.command, level, tt.level)
		}
	}
}
