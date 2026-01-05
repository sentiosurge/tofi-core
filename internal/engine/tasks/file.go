package tasks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"tofi-core/internal/models"
)

type File struct{}

func (f *File) Execute(config map[string]interface{}, ctx *models.ExecutionContext) (string, error) {
	operation, _ := config["operation"].(string)
	pathRaw, _ := config["path"].(string)
	base, _ := config["base"].(string) // artifacts, uploads
	
	if pathRaw == "" {
		return "", fmt.Errorf("config.path is required")
	}

	// 确定基准目录
	var baseDir string
	switch strings.ToLower(base) {
	case "uploads":
		baseDir = ctx.Paths.Uploads
	case "artifacts", "":
		baseDir = ctx.Paths.Artifacts
	default:
		return "", fmt.Errorf("unknown base directory: %s (supported: artifacts, uploads)", base)
	}

	if baseDir == "" {
		return "", fmt.Errorf("base directory '%s' is not initialized", base)
	}

	// 安全路径解析
	cleanPath := filepath.Clean(pathRaw)
	if strings.HasPrefix(cleanPath, "..") || strings.HasPrefix(cleanPath, "/") {
		return "", fmt.Errorf("invalid path: %s (must be relative and cannot contain ..)", pathRaw)
	}

	fullPath := filepath.Join(baseDir, cleanPath)

	switch strings.ToLower(operation) {
	case "write":
		content := fmt.Sprint(config["content"])
		
		// 确保父目录存在
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			return "", fmt.Errorf("failed to create directory: %v", err)
		}

		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			return "", fmt.Errorf("failed to write file: %v", err)
		}
		
		ctx.Log("[File] Written %d bytes to %s", len(content), cleanPath)
		return fullPath, nil

	case "read":
		data, err := os.ReadFile(fullPath)
		if err != nil {
			return "", fmt.Errorf("failed to read file: %v", err)
		}
		ctx.Log("[File] Read %d bytes from %s", len(data), cleanPath)
		return string(data), nil

	default:
		return "", fmt.Errorf("unknown operation: %s (supported: write, read)", operation)
	}
}

func (f *File) Validate(node *models.Node) error {
	if _, ok := node.Config["path"]; !ok {
		return fmt.Errorf("config.path is required")
	}
	op, _ := node.Config["operation"].(string)
	if op != "write" && op != "read" {
		return fmt.Errorf("config.operation must be 'write' or 'read'")
	}
	if op == "write" {
		if _, ok := node.Config["content"]; !ok {
			return fmt.Errorf("config.content is required for write operation")
		}
	}
	return nil
}
