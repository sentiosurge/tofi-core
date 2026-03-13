package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"tofi-core/internal/provider"
)

// buildFileTools creates file_read and file_write ExtraBuiltinTools
// scoped to the given sandbox directory.
func buildFileTools(sandboxDir string) []ExtraBuiltinTool {
	return []ExtraBuiltinTool{
		buildFileReadTool(sandboxDir),
		buildFileWriteTool(sandboxDir),
	}
}

func buildFileReadTool(sandboxDir string) ExtraBuiltinTool {
	return ExtraBuiltinTool{
		Schema: provider.Tool{
			Name:        "file_read",
			Description: "Read the contents of a file or list a directory. Paths are relative to the workspace root.",
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
						"description": "Maximum number of lines to read (default: 200, max: 500)",
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

			// Directory listing
			if info.IsDir() {
				return listDirectory(absPath, rawPath)
			}

			// File read
			offset := 1
			if o, ok := args["offset"].(float64); ok && o > 0 {
				offset = int(o)
			}
			limit := 200
			if l, ok := args["limit"].(float64); ok && l > 0 {
				limit = int(l)
				if limit > 500 {
					limit = 500
				}
			}

			return readFileLines(absPath, offset, limit)
		},
	}
}

func buildFileWriteTool(sandboxDir string) ExtraBuiltinTool {
	return ExtraBuiltinTool{
		Schema: provider.Tool{
			Name:        "file_write",
			Description: "Write content to a file. Creates the file and parent directories if they don't exist. Paths are relative to the workspace root.",
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

			// Ensure parent directory exists
			dir := filepath.Dir(absPath)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Sprintf("Error creating directory: %v", err), nil
			}

			// Write or append
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

// validateFilePath resolves a relative path within sandboxDir and ensures
// it doesn't escape via directory traversal.
func validateFilePath(requestedPath, sandboxDir string) (string, error) {
	// Reject absolute paths
	if filepath.IsAbs(requestedPath) {
		return "", fmt.Errorf("absolute paths not allowed, use relative paths")
	}

	// Resolve and clean
	absPath := filepath.Clean(filepath.Join(sandboxDir, requestedPath))

	// Ensure it's still within sandbox
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
	// Remove trailing empty line from Split
	if totalLines > 0 && lines[totalLines-1] == "" {
		totalLines--
		lines = lines[:totalLines]
	}

	if totalLines == 0 {
		return "(empty file)", nil
	}

	// Adjust offset (1-based)
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
			fmt.Fprintf(&sb, "  📁 %s/\n", e.Name())
		} else {
			fmt.Fprintf(&sb, "  📄 %s  (%d bytes)\n", e.Name(), info.Size())
		}
	}
	return sb.String(), nil
}
