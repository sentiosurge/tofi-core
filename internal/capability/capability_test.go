package capability

import (
	"fmt"
	"testing"
)

func TestParseEmpty(t *testing.T) {
	for _, input := range []string{"", "{}", "  "} {
		caps, err := Parse(input)
		if err != nil {
			t.Errorf("Parse(%q) error: %v", input, err)
		}
		if caps != nil {
			t.Errorf("Parse(%q) = %+v, want nil", input, caps)
		}
	}
}

func TestParseMCPServers(t *testing.T) {
	json := `{
		"mcp_servers": {
			"github": {
				"command": "npx",
				"args": ["-y", "@modelcontextprotocol/server-github"],
				"env": {"GITHUB_TOKEN": "{{secret:GITHUB_TOKEN}}"}
			}
		}
	}`
	caps, err := Parse(json)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if caps == nil {
		t.Fatal("expected non-nil caps")
	}
	if len(caps.MCPServers) != 1 {
		t.Fatalf("expected 1 MCP server, got %d", len(caps.MCPServers))
	}
	gh := caps.MCPServers["github"]
	if gh.Command != "npx" {
		t.Errorf("command = %q, want npx", gh.Command)
	}
	if gh.Env["GITHUB_TOKEN"] != "{{secret:GITHUB_TOKEN}}" {
		t.Errorf("env token = %q", gh.Env["GITHUB_TOKEN"])
	}
}

func TestParseWebSearch(t *testing.T) {
	json := `{"web_search": {"enabled": true}}`
	caps, err := Parse(json)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if caps.WebSearch == nil || !caps.WebSearch.Enabled {
		t.Error("expected web_search enabled")
	}
}

func TestParseNotify(t *testing.T) {
	json := `{"notify": {"channels": ["discord", "telegram"]}}`
	caps, err := Parse(json)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if caps.Notify == nil || len(caps.Notify.Channels) != 2 {
		t.Error("expected 2 notify channels")
	}
}

func TestResolveSecrets(t *testing.T) {
	caps := &ParsedCapabilities{
		MCPServers: map[string]MCPServerDef{
			"test": {
				Command: "cmd",
				Env: map[string]string{
					"TOKEN":  "{{secret:MY_TOKEN}}",
					"PLAIN":  "no-secret-here",
					"MULTI":  "prefix-{{secret:A}}-mid-{{secret:B}}-suffix",
				},
			},
		},
	}

	secrets := map[string]string{
		"MY_TOKEN": "real-token-123",
		"A":        "aaa",
		"B":        "bbb",
	}
	getter := func(name string) (string, error) {
		v, ok := secrets[name]
		if !ok {
			return "", fmt.Errorf("not found")
		}
		return v, nil
	}

	err := ResolveSecrets(caps, getter)
	if err != nil {
		t.Fatalf("ResolveSecrets error: %v", err)
	}

	env := caps.MCPServers["test"].Env
	if env["TOKEN"] != "real-token-123" {
		t.Errorf("TOKEN = %q, want real-token-123", env["TOKEN"])
	}
	if env["PLAIN"] != "no-secret-here" {
		t.Errorf("PLAIN = %q", env["PLAIN"])
	}
	if env["MULTI"] != "prefix-aaa-mid-bbb-suffix" {
		t.Errorf("MULTI = %q, want prefix-aaa-mid-bbb-suffix", env["MULTI"])
	}
}

func TestResolveSecretsMissing(t *testing.T) {
	caps := &ParsedCapabilities{
		MCPServers: map[string]MCPServerDef{
			"test": {
				Env: map[string]string{"KEY": "{{secret:MISSING}}"},
			},
		},
	}

	getter := func(name string) (string, error) {
		return "", fmt.Errorf("not found")
	}

	err := ResolveSecrets(caps, getter)
	if err == nil {
		t.Error("expected error for missing secret")
	}
}

func TestBuildMCPServers(t *testing.T) {
	caps := &ParsedCapabilities{
		MCPServers: map[string]MCPServerDef{
			"github": {Command: "npx", Args: []string{"-y", "server-github"}},
		},
	}

	servers := BuildMCPServers(caps)
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	if servers[0].Name != "github" {
		t.Errorf("name = %q", servers[0].Name)
	}
	if servers[0].Command != "npx" {
		t.Errorf("command = %q", servers[0].Command)
	}
}

func TestBuildMCPServersNil(t *testing.T) {
	servers := BuildMCPServers(nil)
	if servers != nil {
		t.Error("expected nil for nil caps")
	}
}
