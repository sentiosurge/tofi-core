package agent

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// parseCommandsAST extracts all simple command names from a bash command string
// using a real bash parser. This avoids false positives from regex matching
// (e.g., "rm=myvar" is an assignment, not a delete command).
//
// Returns a list of CommandInfo structs with the command name, full args, and context.
func parseCommandsAST(command string) []CommandInfo {
	parser := syntax.NewParser(syntax.KeepComments(false), syntax.Variant(syntax.LangBash))

	prog, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		// If parsing fails, fall back to simple split
		return []CommandInfo{{Name: extractBaseCommand(command), FullArgs: command, InSubshell: false}}
	}

	var commands []CommandInfo
	syntax.Walk(prog, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.CallExpr:
			// Pure assignment (FOO=bar) with no command — skip
			if len(n.Args) == 0 {
				return true
			}

			// Assignment with command (FOO=bar cmd) — the first Arg is the real command
			// But if Assigns are present and first Arg looks like assignment (contains =), skip
			name := wordToString(n.Args[0])
			if name == "" {
				return true
			}

			// Detect "rm=myvar" pattern — parser sees it as a CallExpr with name "rm=myvar"
			if strings.Contains(name, "=") {
				return true // this is an assignment, not a command
			}

			// Collect all args as a string
			var argParts []string
			for _, arg := range n.Args {
				argParts = append(argParts, wordToString(arg))
			}

			// Check if we're inside a subshell
			inSubshell := isInSubshell(prog, node)

			commands = append(commands, CommandInfo{
				Name:       name,
				FullArgs:   strings.Join(argParts, " "),
				InSubshell: inSubshell,
			})

		case *syntax.Subshell:
			// Will recurse into children automatically
			return true
		}
		return true
	})

	if len(commands) == 0 {
		// Check if the entire command is just an assignment (rm=myvar)
		trimmed := strings.TrimSpace(command)
		if strings.Contains(trimmed, "=") && !strings.Contains(trimmed, " ") {
			return nil // pure assignment, no command
		}
		// Fallback for unparseable constructs
		return []CommandInfo{{Name: extractBaseCommand(command), FullArgs: command}}
	}

	return commands
}

// CommandInfo holds parsed info about a single command in a pipeline/compound.
type CommandInfo struct {
	Name       string // base command name (e.g., "rm", "git", "python3")
	FullArgs   string // full command with all arguments
	InSubshell bool   // true if inside $(...) or (...)
}

// DetectDestructiveAST uses bash AST parsing for accurate destructive command detection.
// Unlike regex-based detection, this correctly handles:
//   - rm=myvar (assignment, not deletion)
//   - echo "rm -rf /" (string literal, not command)
//   - $(rm file) (subshell command — IS destructive)
func DetectDestructiveAST(command string) (DestructiveLevel, string) {
	commands := parseCommandsAST(command)

	maxLevel := SafeCommand
	var warnings []string

	for _, cmd := range commands {
		level, warning := classifyCommand(cmd)
		if level > maxLevel {
			maxLevel = level
		}
		if warning != "" {
			warnings = append(warnings, warning)
		}
	}

	return maxLevel, strings.Join(warnings, "; ")
}

// classifyCommand determines the destructive level of a single parsed command.
func classifyCommand(cmd CommandInfo) (DestructiveLevel, string) {
	name := cmd.Name
	args := cmd.FullArgs

	switch name {
	case "rm":
		if hasFlag(args, "-r", "-R", "-rf", "-fr", "-Rf", "-fR") {
			return DestructiveCommand, "Recursive/force file deletion: " + args
		}
		if hasFlag(args, "-f") {
			return DestructiveCommand, "Force file deletion: " + args
		}
		return CautionCommand, "File deletion: " + args

	case "git":
		subCmd := secondWord(args)
		switch subCmd {
		case "reset":
			if strings.Contains(args, "--hard") {
				return DestructiveCommand, "Discards all uncommitted changes"
			}
		case "push":
			if strings.Contains(args, "--force") || strings.Contains(args, "-f") {
				return DestructiveCommand, "Force push overwrites remote history"
			}
		case "clean":
			if hasFlag(args, "-f", "-fd", "-fx", "-fxd", "-fdx") {
				return DestructiveCommand, "Removes untracked files permanently"
			}
		case "checkout":
			if strings.TrimSpace(args) == "git checkout ." {
				return CautionCommand, "Discards uncommitted working directory changes"
			}
		case "stash":
			if strings.Contains(args, "drop") {
				return CautionCommand, "Permanently deletes stashed changes"
			}
		case "branch":
			if strings.Contains(args, "-D") {
				return CautionCommand, "Force deletes a branch"
			}
		}

	case "kubectl":
		if secondWord(args) == "delete" {
			return DestructiveCommand, "Deletes Kubernetes resources"
		}

	case "terraform":
		if secondWord(args) == "destroy" {
			return DestructiveCommand, "Destroys infrastructure"
		}

	case "docker":
		sub := secondWord(args)
		if sub == "rm" || sub == "rmi" {
			return CautionCommand, "Removes Docker " + sub
		}

	case "mv":
		return CautionCommand, "Moves/renames files"

	case "chmod":
		return CautionCommand, "Changes file permissions"
	}

	// Check for SQL keywords — only for SQL tools, not for echo/grep/cat
	sqlTools := map[string]bool{"mysql": true, "psql": true, "sqlite3": true, "sqlcmd": true, "mongosh": true}
	if sqlTools[name] {
		upper := strings.ToUpper(args)
		if strings.Contains(upper, "DROP TABLE") || strings.Contains(upper, "DROP DATABASE") {
			return DestructiveCommand, "Drops database objects"
		}
		if strings.Contains(upper, "TRUNCATE TABLE") {
			return DestructiveCommand, "Deletes all rows from table"
		}
		if strings.Contains(upper, "DELETE FROM") {
			return CautionCommand, "Deletes rows from table"
		}
	}

	// xargs passes args to another command — check what it's running
	if name == "xargs" {
		// Extract the command after xargs flags
		parts := strings.Fields(args)
		for i := 1; i < len(parts); i++ {
			if !strings.HasPrefix(parts[i], "-") {
				// Found the actual command
				subCmd := strings.Join(parts[i:], " ")
				subInfo := CommandInfo{Name: parts[i], FullArgs: subCmd}
				return classifyCommand(subInfo)
			}
		}
	}

	return SafeCommand, ""
}

// ── helpers ──

func wordToString(word *syntax.Word) string {
	var sb strings.Builder
	syntax.NewPrinter().Print(&sb, word)
	return sb.String()
}

func isInSubshell(prog *syntax.File, target syntax.Node) bool {
	// Simple heuristic: check position against known subshell nodes
	// For a full implementation, we'd track parent nodes during Walk
	// This is a placeholder — the parser already handles $() correctly
	_ = prog
	_ = target
	return false
}

func hasFlag(args string, flags ...string) bool {
	parts := strings.Fields(args)
	for _, part := range parts {
		for _, flag := range flags {
			if part == flag {
				return true
			}
		}
	}
	return false
}

func secondWord(s string) string {
	parts := strings.Fields(s)
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}
