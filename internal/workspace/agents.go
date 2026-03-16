package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"tofi-core/internal/apps"
	"tofi-core/internal/storage"

	"gopkg.in/yaml.v3"
)

// Agent file names
const (
	AppYAMLFile  = "tofi_app.yaml"
	AgentsMDFile = "AGENTS.md"
	SoulMDFile   = "SOUL.md"
	IdentityFile = "IDENTITY.md"
	MemoryDir    = "memory"
	MemoryIndex  = "MEMORY.md"
)

// WriteAgent writes an agent definition as agent.md + tofi_app.yaml.
func (w *Workspace) WriteAgent(userID string, def *apps.AgentDef) error {
	if def.Config.Name == "" {
		return fmt.Errorf("agent name is required")
	}

	agentDir := w.AgentDir(userID, def.Config.Name)
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		return fmt.Errorf("create agent dir: %w", err)
	}

	// Write tofi_app.yaml
	yamlData, err := yaml.Marshal(def.Config)
	if err != nil {
		return fmt.Errorf("marshal tofi_app.yaml: %w", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, AppYAMLFile), yamlData, 0644); err != nil {
		return fmt.Errorf("write tofi_app.yaml: %w", err)
	}

	// Write AGENTS.md (operational instructions)
	if def.AgentsMD != "" {
		if err := os.WriteFile(filepath.Join(agentDir, AgentsMDFile), []byte(def.AgentsMD), 0644); err != nil {
			return fmt.Errorf("write AGENTS.md: %w", err)
		}
	}

	// Write SOUL.md (personality, principles, boundaries)
	if def.SoulMD != "" {
		if err := os.WriteFile(filepath.Join(agentDir, SoulMDFile), []byte(def.SoulMD), 0644); err != nil {
			return fmt.Errorf("write SOUL.md: %w", err)
		}
	}

	// Write IDENTITY.md (name, emoji, vibe)
	if def.IdentityMD != "" {
		if err := os.WriteFile(filepath.Join(agentDir, IdentityFile), []byte(def.IdentityMD), 0644); err != nil {
			return fmt.Errorf("write IDENTITY.md: %w", err)
		}
	}

	// Ensure memory directory exists
	os.MkdirAll(filepath.Join(agentDir, MemoryDir), 0755)

	return nil
}

// ReadAgent reads an agent from agent.md + tofi_app.yaml.
func (w *Workspace) ReadAgent(userID, agentName string) (*apps.AgentDef, error) {
	agentDir := w.AgentDir(userID, agentName)
	return ReadAgentDir(agentDir)
}

// ReadAgentDir reads an agent definition from a directory.
func ReadAgentDir(dir string) (*apps.AgentDef, error) {
	// Read tofi_app.yaml (required)
	yamlPath := filepath.Join(dir, AppYAMLFile)
	yamlData, err := os.ReadFile(yamlPath)
	if err != nil {
		return nil, fmt.Errorf("read tofi_app.yaml: %w", err)
	}

	var config apps.AppConfig
	if err := yaml.Unmarshal(yamlData, &config); err != nil {
		return nil, fmt.Errorf("parse tofi_app.yaml: %w", err)
	}

	def := &apps.AgentDef{
		Config: config,
		Dir:    dir,
	}

	// Read AGENTS.md (optional)
	if data, err := os.ReadFile(filepath.Join(dir, AgentsMDFile)); err == nil {
		def.AgentsMD = string(data)
	}

	// Read SOUL.md (optional)
	if data, err := os.ReadFile(filepath.Join(dir, SoulMDFile)); err == nil {
		def.SoulMD = string(data)
	}

	// Read IDENTITY.md (optional)
	if data, err := os.ReadFile(filepath.Join(dir, IdentityFile)); err == nil {
		def.IdentityMD = string(data)
	}

	// Check for scripts/
	scriptsDir := filepath.Join(dir, "scripts")
	if entries, err := os.ReadDir(scriptsDir); err == nil && len(entries) > 0 {
		def.HasScripts = true
	}

	return def, nil
}

// ListAgents returns all agent names for a user.
func (w *Workspace) ListAgents(userID string) ([]string, error) {
	agentsDir := w.AgentsDir(userID)
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read agents dir: %w", err)
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Must contain tofi_app.yaml
		yamlPath := filepath.Join(agentsDir, entry.Name(), AppYAMLFile)
		if _, err := os.Stat(yamlPath); err == nil {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

// DeleteAgent removes an agent directory entirely.
func (w *Workspace) DeleteAgent(userID, agentName string) error {
	agentDir := w.AgentDir(userID, agentName)
	if _, err := os.Stat(agentDir); os.IsNotExist(err) {
		return fmt.Errorf("agent %q not found", agentName)
	}
	return os.RemoveAll(agentDir)
}

// AgentDefToRecord converts an AgentDef to a storage.AppRecord for DB indexing.
func AgentDefToRecord(userID string, def *apps.AgentDef) *storage.AppRecord {
	cfg := def.Config

	skillsJSON := "[]"
	if len(cfg.Skills) > 0 {
		if b, err := json.Marshal(cfg.Skills); err == nil {
			skillsJSON = string(b)
		}
	}

	capsJSON := "{}"
	if cfg.Capabilities != nil {
		if b, err := json.Marshal(cfg.Capabilities); err == nil {
			capsJSON = string(b)
		}
	}

	scheduleJSON := ""
	if cfg.Schedule != nil {
		if b, err := json.Marshal(cfg.Schedule); err == nil {
			scheduleJSON = string(b)
		}
	}

	paramDefsJSON := "{}"
	if cfg.Parameters != nil {
		if b, err := json.Marshal(cfg.Parameters); err == nil {
			paramDefsJSON = string(b)
		}
	}

	configJSON := "{}"
	if b, err := json.Marshal(cfg); err == nil {
		configJSON = string(b)
	}

	return &storage.AppRecord{
		Name:             cfg.Name,
		Description:      cfg.Description,
		Prompt:           def.AgentsMD,
		SystemPrompt:     def.SystemPrompt(),
		Model:            cfg.Model,
		Skills:           skillsJSON,
		ScheduleRules:    scheduleJSON,
		Capabilities:     capsJSON,
		BufferSize:       cfg.BufferSize,
		RenewalThreshold: cfg.RenewalThreshold,
		UserID:           userID,
		ParameterDefs:    paramDefsJSON,
		Source:           "local",
		Version:          cfg.Version,
		Author:           cfg.Author,
		ManifestJSON:     configJSON,
		HasScripts:       def.HasScripts,
	}
}

// RecordToAgentDef converts a storage.AppRecord back to an AgentDef for writing to files.
func RecordToAgentDef(record *storage.AppRecord) *apps.AgentDef {
	cfg := apps.AppConfig{
		Name:             record.Name,
		Description:      record.Description,
		Model:            record.Model,
		Version:          record.Version,
		Author:           record.Author,
		BufferSize:       record.BufferSize,
		RenewalThreshold: record.RenewalThreshold,
	}

	// Parse capabilities
	if record.Capabilities != "" && record.Capabilities != "{}" {
		var caps map[string]any
		if err := json.Unmarshal([]byte(record.Capabilities), &caps); err == nil {
			cfg.Capabilities = caps
		}
	}

	// Parse parameter defs
	if record.ParameterDefs != "" && record.ParameterDefs != "{}" {
		var params map[string]*apps.AppParameter
		if err := json.Unmarshal([]byte(record.ParameterDefs), &params); err == nil {
			cfg.Parameters = params
		}
	}

	// Parse schedule
	if record.ScheduleRules != "" && record.ScheduleRules != "[]" && record.ScheduleRules != "{}" {
		var schedule apps.AppConfigSchedule
		if err := json.Unmarshal([]byte(record.ScheduleRules), &schedule); err == nil {
			cfg.Schedule = &schedule
		}
	}

	// Parse skills
	if record.Skills != "" && record.Skills != "[]" {
		var skills []string
		if err := json.Unmarshal([]byte(record.Skills), &skills); err == nil {
			cfg.Skills = skills
		}
	}

	return &apps.AgentDef{
		Config:   cfg,
		AgentsMD: record.Prompt,
		SoulMD:   record.SystemPrompt, // best-effort: full system prompt goes to SOUL.md
	}
}
