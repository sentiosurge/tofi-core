package agent

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"tofi-core/internal/provider"
)

// ──────────────────────────────────────────────────────────────
// Tool naming convention:
//   Internal ID:   tofi__read, tofi__write, tofi__edit, tofi__glob, tofi__grep
//   Display Name:  Read File, Write File, Edit File, Find Files, Search Content
//
// Internal IDs are what the LLM sees in tool_call.
// Display names are what users see in TUI / Web UI.
// ──────────────────────────────────────────────────────────────

// buildFileTools creates all 5 core file tools scoped to the given sandbox directory.
// Legacy names (tofi_read, tofi_write) are kept as aliases — new code uses tofi__ prefix.
func buildFileTools(sandboxDir string) []ExtraBuiltinTool {
	return []ExtraBuiltinTool{
		buildReadTool(sandboxDir),
		buildWriteTool(sandboxDir),
		buildEditTool(sandboxDir),
		buildGlobTool(sandboxDir),
		buildGrepTool(sandboxDir),
	}
}

// ──────────────────────────────────────────────────────────────
// 1. tofi__read — Read File
// ──────────────────────────────────────────────────────────────

func buildReadTool(sandboxDir string) ExtraBuiltinTool {
	return ExtraBuiltinTool{
		Schema: provider.Tool{
			Name: "tofi_read",
			Description: "Read file contents or list a directory. Supports text files with line numbers. " +
				"Detects binary files and reports metadata instead of raw content. " +
				"Paths are relative to the workspace root.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File or directory path (relative to workspace)",
					},
					"offset": map[string]any{
						"type":        "integer",
						"description": "Start reading from this line number (1-based, default: 1)",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of lines to read (default: 200, max: 2000)",
					},
				},
				"required": []string{"path"},
			},
		},
		Handler: func(args map[string]any) (string, error) {
			rawPath, _ := args["path"].(string)
			if rawPath == "" {
				return "Error: path is required", nil
			}

			absPath, err := validateFilePath(rawPath, sandboxDir)
			if err != nil {
				return fmt.Sprintf("Error: %v", err), nil
			}

			info, err := os.Stat(absPath)
			if err != nil {
				if os.IsNotExist(err) {
					return fmt.Sprintf("Error: file not found: %s", rawPath), nil
				}
				return fmt.Sprintf("Error: %v", err), nil
			}

			if info.IsDir() {
				return listDirectory(absPath, rawPath)
			}

			// Binary file detection — check first 512 bytes
			if isBinaryFile(absPath) {
				return fmt.Sprintf("Binary file: %s (%d bytes, %s)\nUse tofi_shell to process binary files.",
					rawPath, info.Size(), info.ModTime().Format("2006-01-02 15:04:05")), nil
			}

			// Large file warning
			if info.Size() > 10*1024*1024 { // 10MB
				return fmt.Sprintf("File too large: %s (%d bytes). Use offset/limit to read portions, or tofi_shell for processing.",
					rawPath, info.Size()), nil
			}

			offset := 1
			if o, ok := args["offset"].(float64); ok && o > 0 {
				offset = int(o)
			}
			limit := 200
			if l, ok := args["limit"].(float64); ok && l > 0 {
				limit = int(l)
				if limit > 2000 {
					limit = 2000
				}
			}

			return readFileLines(absPath, offset, limit)
		},
	}
}

// ──────────────────────────────────────────────────────────────
// 2. tofi__write — Write File
// ──────────────────────────────────────────────────────────────

func buildWriteTool(sandboxDir string) ExtraBuiltinTool {
	return ExtraBuiltinTool{
		Schema: provider.Tool{
			Name: "tofi_write",
			Description: "Write content to a file. Creates the file and parent directories if they don't exist. " +
				"Paths are relative to the workspace root.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path (relative to workspace)",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "Content to write",
					},
					"append": map[string]any{
						"type":        "boolean",
						"description": "If true, append to file instead of overwriting (default: false)",
					},
				},
				"required": []string{"path", "content"},
			},
		},
		Handler: func(args map[string]any) (string, error) {
			rawPath, _ := args["path"].(string)
			if rawPath == "" {
				return "Error: path is required", nil
			}
			content, _ := args["content"].(string)

			absPath, err := validateFilePath(rawPath, sandboxDir)
			if err != nil {
				return fmt.Sprintf("Error: %v", err), nil
			}

			dir := filepath.Dir(absPath)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Sprintf("Error creating directory: %v", err), nil
			}

			appendMode, _ := args["append"].(bool)
			var flag int
			if appendMode {
				flag = os.O_WRONLY | os.O_CREATE | os.O_APPEND
			} else {
				flag = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
			}

			f, err := os.OpenFile(absPath, flag, 0644)
			if err != nil {
				return fmt.Sprintf("Error opening file: %v", err), nil
			}
			defer f.Close()

			n, err := f.WriteString(content)
			if err != nil {
				return fmt.Sprintf("Error writing file: %v", err), nil
			}

			lines := strings.Count(content, "\n")
			if len(content) > 0 && content[len(content)-1] != '\n' {
				lines++
			}

			action := "Written"
			if appendMode {
				action = "Appended"
			}
			return fmt.Sprintf("%s %d bytes (%d lines) to %s", action, n, lines, rawPath), nil
		},
	}
}

// ──────────────────────────────────────────────────────────────
// 3. tofi__edit — Edit File (find-and-replace)
// ──────────────────────────────────────────────────────────────

func buildEditTool(sandboxDir string) ExtraBuiltinTool {
	return ExtraBuiltinTool{
		Schema: provider.Tool{
			Name: "tofi_edit",
			Description: "Edit a file by replacing exact string matches. The old_string must be unique in the file " +
				"(unless replace_all is true). Use this for surgical edits instead of rewriting entire files.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path (relative to workspace)",
					},
					"old_string": map[string]any{
						"type":        "string",
						"description": "Exact string to find and replace",
					},
					"new_string": map[string]any{
						"type":        "string",
						"description": "Replacement string",
					},
					"replace_all": map[string]any{
						"type":        "boolean",
						"description": "Replace all occurrences (default: false, requires unique match)",
					},
				},
				"required": []string{"path", "old_string", "new_string"},
			},
		},
		Handler: func(args map[string]any) (string, error) {
			rawPath, _ := args["path"].(string)
			if rawPath == "" {
				return "Error: path is required", nil
			}
			oldStr, _ := args["old_string"].(string)
			newStr, _ := args["new_string"].(string)
			replaceAll, _ := args["replace_all"].(bool)

			if oldStr == newStr {
				return "Error: old_string and new_string are identical", nil
			}

			absPath, err := validateFilePath(rawPath, sandboxDir)
			if err != nil {
				return fmt.Sprintf("Error: %v", err), nil
			}

			data, err := os.ReadFile(absPath)
			if err != nil {
				if os.IsNotExist(err) {
					return fmt.Sprintf("Error: file not found: %s", rawPath), nil
				}
				return fmt.Sprintf("Error reading file: %v", err), nil
			}

			content := string(data)
			count := strings.Count(content, oldStr)

			if count == 0 {
				return fmt.Sprintf("Error: old_string not found in %s", rawPath), nil
			}

			if count > 1 && !replaceAll {
				return fmt.Sprintf("Error: old_string found %d times in %s. "+
					"Provide more context to make it unique, or set replace_all=true.", count, rawPath), nil
			}

			var newContent string
			if replaceAll {
				newContent = strings.ReplaceAll(content, oldStr, newStr)
			} else {
				newContent = strings.Replace(content, oldStr, newStr, 1)
			}

			if err := os.WriteFile(absPath, []byte(newContent), 0644); err != nil {
				return fmt.Sprintf("Error writing file: %v", err), nil
			}

			if replaceAll {
				return fmt.Sprintf("Replaced %d occurrences in %s", count, rawPath), nil
			}
			return fmt.Sprintf("Edited %s (1 replacement)", rawPath), nil
		},
	}
}

// ──────────────────────────────────────────────────────────────
// 4. tofi__glob — Find Files
// ──────────────────────────────────────────────────────────────

func buildGlobTool(sandboxDir string) ExtraBuiltinTool {
	return ExtraBuiltinTool{
		Schema: provider.Tool{
			Name: "tofi_glob",
			Description: "Find files matching a glob pattern. Supports ** for recursive matching. " +
				"Returns up to 100 matching file paths sorted by modification time (newest first). " +
				"Paths are relative to the workspace root.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "Glob pattern (e.g., '**/*.py', 'src/**/*.go', '*.json')",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Directory to search in (relative to workspace, default: workspace root)",
					},
				},
				"required": []string{"pattern"},
			},
		},
		Handler: func(args map[string]any) (string, error) {
			pattern, _ := args["pattern"].(string)
			if pattern == "" {
				return "Error: pattern is required", nil
			}

			searchDir := sandboxDir
			if p, ok := args["path"].(string); ok && p != "" {
				resolved, err := validateFilePath(p, sandboxDir)
				if err != nil {
					return fmt.Sprintf("Error: %v", err), nil
				}
				searchDir = resolved
			}

			type fileEntry struct {
				path    string
				modTime int64
			}

			var matches []fileEntry
			maxResults := 100

			err := filepath.Walk(searchDir, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return nil // skip errors
				}
				// Skip hidden directories
				if info.IsDir() && strings.HasPrefix(info.Name(), ".") && path != searchDir {
					return filepath.SkipDir
				}
				if info.IsDir() {
					return nil
				}

				// Get relative path for matching
				relPath, _ := filepath.Rel(searchDir, path)
				matched, _ := filepath.Match(pattern, relPath)
				if !matched {
					// Try matching just the filename for simple patterns
					matched, _ = filepath.Match(pattern, info.Name())
				}
				if !matched && strings.Contains(pattern, "**") {
					// Manual ** support: match against all path segments
					matched = matchDoublestar(pattern, relPath)
				}

				if matched {
					matches = append(matches, fileEntry{
						path:    relPath,
						modTime: info.ModTime().UnixNano(),
					})
				}
				return nil
			})
			if err != nil {
				return fmt.Sprintf("Error searching: %v", err), nil
			}

			// Sort by modification time (newest first)
			sort.Slice(matches, func(i, j int) bool {
				return matches[i].modTime > matches[j].modTime
			})

			if len(matches) == 0 {
				return fmt.Sprintf("No files matching '%s' found", pattern), nil
			}

			truncated := len(matches) > maxResults
			if truncated {
				matches = matches[:maxResults]
			}

			var sb strings.Builder
			fmt.Fprintf(&sb, "Found %d files", len(matches))
			if truncated {
				sb.WriteString(" (showing first 100)")
			}
			sb.WriteString(":\n")
			for _, m := range matches {
				sb.WriteString(m.path)
				sb.WriteString("\n")
			}
			return sb.String(), nil
		},
	}
}

// ──────────────────────────────────────────────────────────────
// 5. tofi__grep — Search Content
// ──────────────────────────────────────────────────────────────

func buildGrepTool(sandboxDir string) ExtraBuiltinTool {
	return ExtraBuiltinTool{
		Schema: provider.Tool{
			Name: "tofi_grep",
			Description: "Search file contents using regex patterns. Returns matching lines with file paths and line numbers. " +
				"Uses ripgrep (rg) if available, otherwise falls back to built-in search. " +
				"Paths are relative to the workspace root.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "Regex pattern to search for",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "File or directory to search (default: workspace root)",
					},
					"glob": map[string]any{
						"type":        "string",
						"description": "File glob filter (e.g., '*.py', '*.go')",
					},
					"case_insensitive": map[string]any{
						"type":        "boolean",
						"description": "Case insensitive search (default: false)",
					},
					"context_lines": map[string]any{
						"type":        "integer",
						"description": "Number of context lines before and after each match (default: 0)",
					},
					"max_results": map[string]any{
						"type":        "integer",
						"description": "Maximum number of matching lines to return (default: 50, max: 200)",
					},
				},
				"required": []string{"pattern"},
			},
		},
		Handler: func(args map[string]any) (string, error) {
			pattern, _ := args["pattern"].(string)
			if pattern == "" {
				return "Error: pattern is required", nil
			}

			searchPath := sandboxDir
			if p, ok := args["path"].(string); ok && p != "" {
				resolved, err := validateFilePath(p, sandboxDir)
				if err != nil {
					return fmt.Sprintf("Error: %v", err), nil
				}
				searchPath = resolved
			}

			caseInsensitive, _ := args["case_insensitive"].(bool)
			contextLines := 0
			if c, ok := args["context_lines"].(float64); ok && c > 0 {
				contextLines = int(c)
				if contextLines > 5 {
					contextLines = 5
				}
			}
			maxResults := 50
			if m, ok := args["max_results"].(float64); ok && m > 0 {
				maxResults = int(m)
				if maxResults > 200 {
					maxResults = 200
				}
			}
			globFilter, _ := args["glob"].(string)

			// Try ripgrep first (much faster for large codebases)
			if rgPath, err := exec.LookPath("rg"); err == nil {
				// Verify rg is a real binary, not a shell alias/function
				if info, statErr := os.Stat(rgPath); statErr == nil && !info.IsDir() {
					return grepWithRipgrep(rgPath, pattern, searchPath, sandboxDir, globFilter, caseInsensitive, contextLines, maxResults)
				}
			}

			// Fallback: built-in Go grep
			return grepBuiltin(pattern, searchPath, sandboxDir, globFilter, caseInsensitive, maxResults)
		},
	}
}

// ──────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────

// validateFilePath resolves a relative path within sandboxDir and ensures
// it doesn't escape via directory traversal.
func validateFilePath(requestedPath, sandboxDir string) (string, error) {
	if filepath.IsAbs(requestedPath) {
		return "", fmt.Errorf("absolute paths not allowed, use relative paths")
	}
	absPath := filepath.Clean(filepath.Join(sandboxDir, requestedPath))
	if !strings.HasPrefix(absPath, filepath.Clean(sandboxDir)) {
		return "", fmt.Errorf("path escapes workspace boundary")
	}
	return absPath, nil
}

// readFileLines reads lines [offset, offset+limit) from a file with line numbers.
func readFileLines(path string, offset, limit int) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("Error reading file: %v", err), nil
	}

	lines := strings.Split(string(data), "\n")
	totalLines := len(lines)
	if totalLines > 0 && lines[totalLines-1] == "" {
		totalLines--
		lines = lines[:totalLines]
	}

	if totalLines == 0 {
		return "(empty file)", nil
	}

	startIdx := offset - 1
	if startIdx < 0 {
		startIdx = 0
	}
	if startIdx >= totalLines {
		return fmt.Sprintf("(file has %d lines, offset %d is beyond end)", totalLines, offset), nil
	}

	endIdx := startIdx + limit
	if endIdx > totalLines {
		endIdx = totalLines
	}

	var sb strings.Builder
	for i := startIdx; i < endIdx; i++ {
		fmt.Fprintf(&sb, "%4d│%s\n", i+1, lines[i])
	}

	if endIdx < totalLines {
		fmt.Fprintf(&sb, "\n... (%d more lines, use offset=%d to continue)", totalLines-endIdx, endIdx+1)
	}

	return sb.String(), nil
}

// listDirectory returns a formatted directory listing.
func listDirectory(absPath, displayPath string) (string, error) {
	entries, err := os.ReadDir(absPath)
	if err != nil {
		return fmt.Sprintf("Error reading directory: %v", err), nil
	}

	if len(entries) == 0 {
		return fmt.Sprintf("Directory '%s' is empty", displayPath), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Directory: %s (%d entries)\n\n", displayPath, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if e.IsDir() {
			fmt.Fprintf(&sb, "  %s/\n", e.Name())
		} else {
			fmt.Fprintf(&sb, "  %s  (%d bytes)\n", e.Name(), info.Size())
		}
	}
	return sb.String(), nil
}

// isBinaryFile checks if a file is binary by reading the first 512 bytes.
func isBinaryFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil || n == 0 {
		return false
	}
	buf = buf[:n]

	// Check for null bytes (strong binary indicator)
	for _, b := range buf {
		if b == 0 {
			return true
		}
	}

	// Check if content is valid UTF-8
	return !utf8.Valid(buf)
}

// matchDoublestar provides basic ** glob matching.
func matchDoublestar(pattern, path string) bool {
	// Split pattern by **
	parts := strings.Split(pattern, "**")
	if len(parts) != 2 {
		return false // only support single **
	}

	prefix := strings.TrimSuffix(parts[0], "/")
	suffix := strings.TrimPrefix(parts[1], "/")

	// Check prefix (if any)
	if prefix != "" && !strings.HasPrefix(path, prefix+"/") {
		return false
	}

	// Check suffix (file pattern like *.go)
	if suffix != "" {
		filename := filepath.Base(path)
		matched, _ := filepath.Match(suffix, filename)
		return matched
	}

	return true
}

// grepWithRipgrep uses rg for fast searching.
func grepWithRipgrep(rgPath, pattern, searchPath, sandboxDir, globFilter string, caseInsensitive bool, contextLines, maxResults int) (string, error) {
	rgArgs := []string{
		"--no-heading",
		"--line-number",
		"--max-columns", "500",
		"--max-count", fmt.Sprintf("%d", maxResults),
	}

	if caseInsensitive {
		rgArgs = append(rgArgs, "-i")
	}
	if contextLines > 0 {
		rgArgs = append(rgArgs, "-C", fmt.Sprintf("%d", contextLines))
	}
	if globFilter != "" {
		rgArgs = append(rgArgs, "--glob", globFilter)
	}

	rgArgs = append(rgArgs, "--", pattern, searchPath)

	cmd := exec.Command(rgPath, rgArgs...)
	output, err := cmd.Output()

	// rg returns exit code 1 for "no matches" — not an error
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return fmt.Sprintf("No matches for pattern '%s'", pattern), nil
		}
		// Exit code 2 = error
		return fmt.Sprintf("Search error: %v", err), nil
	}

	result := string(output)

	// Make paths relative to sandbox
	result = strings.ReplaceAll(result, sandboxDir+"/", "")

	lines := strings.Split(result, "\n")
	if len(lines) > maxResults+10 { // some slack for context lines
		result = strings.Join(lines[:maxResults], "\n")
		result += fmt.Sprintf("\n\n... (truncated at %d results)", maxResults)
	}

	return result, nil
}

// grepBuiltin provides a pure Go fallback grep when ripgrep is not available.
func grepBuiltin(pattern, searchPath, sandboxDir, globFilter string, caseInsensitive bool, maxResults int) (string, error) {
	flags := ""
	if caseInsensitive {
		flags = "(?i)"
	}
	re, err := regexp.Compile(flags + pattern)
	if err != nil {
		return fmt.Sprintf("Error: invalid regex pattern: %v", err), nil
	}

	var results []string
	count := 0

	err = filepath.Walk(searchPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		// Skip hidden directories within the search path (not the search path itself)
		if info.IsDir() {
			relToSearch, _ := filepath.Rel(searchPath, path)
			if relToSearch != "." && strings.HasPrefix(info.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Size() > 1024*1024 { // skip files > 1MB
			return nil
		}
		// Apply glob filter
		if globFilter != "" {
			matched, _ := filepath.Match(globFilter, info.Name())
			if !matched {
				return nil
			}
		}
		// Skip binary files
		if isBinaryFile(path) {
			return nil
		}

		relPath, _ := filepath.Rel(sandboxDir, path)

		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				results = append(results, fmt.Sprintf("%s:%d:%s", relPath, lineNum, line))
				count++
				if count >= maxResults {
					return fmt.Errorf("max results reached")
				}
			}
		}
		return nil
	})

	if len(results) == 0 {
		return fmt.Sprintf("No matches for pattern '%s'", pattern), nil
	}

	var sb strings.Builder
	sb.WriteString(strings.Join(results, "\n"))
	if count >= maxResults {
		fmt.Fprintf(&sb, "\n\n... (truncated at %d results)", maxResults)
	}
	return sb.String(), nil
}
