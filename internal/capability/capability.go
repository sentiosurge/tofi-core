package capability

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"tofi-core/internal/agent"
)

// ParsedCapabilities represents the decoded capability config from an Agent.
type ParsedCapabilities struct {
	MCPServers map[string]MCPServerDef `json:"mcp_servers,omitempty"`
	WebSearch  *WebSearchConfig        `json:"web_search,omitempty"`
	Notify     *NotifyConfig           `json:"notify,omitempty"`
}

// MCPServerDef defines an MCP server to connect to.
type MCPServerDef struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

// WebSearchConfig enables web search capability.
type WebSearchConfig struct {
	Enabled bool `json:"enabled"`
}

// NotifyConfig enables push notification capability.
type NotifyConfig struct {
	Channels []string `json:"channels"` // "discord", "telegram"
}

// SecretGetter resolves a secret name to its decrypted value.
type SecretGetter func(name string) (string, error)

// Parse decodes capability JSON into a structured config.
// Returns nil if the JSON is empty or "{}".
func Parse(capsJSON string) (*ParsedCapabilities, error) {
	capsJSON = strings.TrimSpace(capsJSON)
	if capsJSON == "" || capsJSON == "{}" {
		return nil, nil
	}
	var caps ParsedCapabilities
	if err := json.Unmarshal([]byte(capsJSON), &caps); err != nil {
		return nil, fmt.Errorf("invalid capabilities JSON: %w", err)
	}
	return &caps, nil
}

// secretPlaceholder matches {{secret:KEY_NAME}} patterns.
var secretPlaceholder = regexp.MustCompile(`\{\{secret:([^}]+)\}\}`)

// ResolveSecrets replaces all {{secret:KEY_NAME}} placeholders in MCP server
// env vars with actual decrypted values from the secrets store.
func ResolveSecrets(caps *ParsedCapabilities, getter SecretGetter) error {
	if caps == nil || getter == nil {
		return nil
	}
	for name, server := range caps.MCPServers {
		for envKey, envVal := range server.Env {
			resolved, err := resolveSecretString(envVal, getter)
			if err != nil {
				return fmt.Errorf("MCP server %q env %q: %w", name, envKey, err)
			}
			server.Env[envKey] = resolved
		}
		caps.MCPServers[name] = server
	}
	return nil
}

// resolveSecretString replaces {{secret:KEY}} in a string with the actual value.
func resolveSecretString(s string, getter SecretGetter) (string, error) {
	matches := secretPlaceholder.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s, nil
	}

	var result strings.Builder
	lastEnd := 0
	for _, match := range matches {
		// match[0:1] = full match, match[2:3] = capture group
		result.WriteString(s[lastEnd:match[0]])
		secretName := s[match[2]:match[3]]
		val, err := getter(secretName)
		if err != nil {
			return "", fmt.Errorf("secret %q not found: %w", secretName, err)
		}
		result.WriteString(val)
		lastEnd = match[1]
	}
	result.WriteString(s[lastEnd:])
	return result.String(), nil
}

// BuildMCPServers converts parsed MCP server definitions to MCPServerConfig
// entries ready for injection into AgentConfig.
func BuildMCPServers(caps *ParsedCapabilities) []agent.MCPServerConfig {
	if caps == nil {
		return nil
	}
	var servers []agent.MCPServerConfig
	for name, def := range caps.MCPServers {
		servers = append(servers, agent.MCPServerConfig{
			Name:    name,
			Command: def.Command,
			Args:    def.Args,
			Env:     def.Env,
		})
	}
	return servers
}

// BuildExtraTools collects all capability-provided tools (web_search, notify, etc.)
// Note: web_search is now handled exclusively by the web-search system skill
// (richer functionality: Brave LLM Context + DuckDuckGo fallback + news + summarize).
// The old capability-based BuildWebSearchTool has been removed.
func BuildExtraTools(caps *ParsedCapabilities, getter SecretGetter) []agent.ExtraBuiltinTool {
	if caps == nil {
		return nil
	}
	var tools []agent.ExtraBuiltinTool

	// Web Search — now via system skill only (no longer a capability tool)
	// Notify — now handled by connect.InjectNotifyTool (connector system)

	return tools
}

// BuildNonSearchTools collects capability tools excluding web_search (for when web-search skill is used instead).
func BuildNonSearchTools(caps *ParsedCapabilities, getter SecretGetter) []agent.ExtraBuiltinTool {
	if caps == nil {
		return nil
	}
	var tools []agent.ExtraBuiltinTool

	// Notify — now handled by connect.InjectNotifyTool (connector system)

	return tools
}
