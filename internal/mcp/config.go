package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// MCPConfigDefinition matches the standard MCP configuration format used by Claude/Cursor
type MCPConfigDefinition struct {
	MCPServers map[string]MCPServerDefinition `json:"mcpServers"`
}

type MCPServerDefinition struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

// LoadUserMCPConfig loads the mcp_config.json for a specific user
// Path: .tofi/{user}/mcp_config.json
func LoadUserMCPConfig(homeDir, user string) (*MCPConfigDefinition, error) {
	// Default to 'default' user if not specified, or handle as per Tofi's user model
	if user == "" {
		user = "default"
	}

	configPath := filepath.Join(homeDir, user, "mcp_config.json")
	
	// Check if file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, nil // Return nil if no config exists, acting as empty
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read mcp config: %v", err)
	}

	var config MCPConfigDefinition
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse mcp config: %v", err)
	}

	return &config, nil
}
