package server

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"tofi-core/internal/capability"
	"tofi-core/internal/crypto"
	"tofi-core/internal/agent"
	"tofi-core/internal/skills"
	"tofi-core/internal/storage"
)

// buildSkillToolsFromRecords builds SkillTool list from skill records and resolves secrets.
// Returns: skillTools, secretEnv, missingSecrets
func (s *Server) buildSkillToolsFromRecords(userID string, skillRecords []*storage.SkillRecord) ([]agent.SkillTool, map[string]string, []string) {
	localStore := skills.NewLocalStore(s.config.HomeDir)
	var skillTools []agent.SkillTool
	secretEnv := make(map[string]string)
	var missingSecrets []string

	for _, skill := range skillRecords {
		st := agent.SkillTool{
			ID:           skill.ID,
			Name:         skill.Name,
			Description:  skill.Description,
			Instructions: skill.Instructions,
		}
		// 如果 skill 有脚本，传入磁盘绝对路径（用于创建 symlink）
		if skill.HasScripts {
			skillDir := localStore.SkillDir(skill.Name)
			if abs, err := filepath.Abs(skillDir); err == nil {
				skillDir = abs
			}
			st.SkillDir = skillDir
		}
		skillTools = append(skillTools, st)

		// Resolve secrets
		for _, secretName := range skill.RequiredSecretsList() {
			if _, ok := secretEnv[secretName]; ok {
				continue // already resolved
			}
			secretRec, err := s.db.GetSecret(userID, secretName)
			if err != nil {
				log.Printf("secret %q for skill %q not found", secretName, skill.Name)
				missingSecrets = append(missingSecrets, fmt.Sprintf("Skill '%s' requires secret '%s'", skill.Name, secretName))
				continue
			}
			val, err := crypto.Decrypt(secretRec.EncryptedValue)
			if err != nil {
				log.Printf("decrypt secret %q failed: %v", secretName, err)
				missingSecrets = append(missingSecrets, fmt.Sprintf("Skill '%s': failed to decrypt secret '%s'", skill.Name, secretName))
				continue
			}
			secretEnv[secretName] = val
		}
	}

	return skillTools, secretEnv, missingSecrets
}

// buildSkillToolsFromNames loads skills by name and builds tools.
// Returns skillTools, skillInstructions (for appending to system prompt), and secretEnv.
// Unlike buildSkillToolsFromRecords, this silently skips missing skills and secrets
// (appropriate for chat context where missing secrets are non-fatal).
func (s *Server) buildSkillToolsFromNames(userID string, skillNames []string) ([]agent.SkillTool, []string, map[string]string) {
	var records []*storage.SkillRecord
	var skillInstructions []string

	for _, skillName := range skillNames {
		skillName = strings.TrimSpace(skillName)
		if skillName == "" {
			continue
		}
		rec, err := s.db.GetSkillByName(userID, skillName)
		if err != nil {
			log.Printf("[chat] skill %q not found: %v", skillName, err)
			continue
		}
		records = append(records, rec)
		if rec.Instructions != "" {
			skillInstructions = append(skillInstructions, rec.Instructions)
		}
	}

	skillTools, secretEnv, _ := s.buildSkillToolsFromRecords(userID, records)
	return skillTools, skillInstructions, secretEnv
}

// buildSkillTools loads skills from embed FS (system) and filesystem (user).
// Does not query the database — filesystem is the single source of truth.
// Returns skillTools, skillInstructions, and secretEnv.
func (s *Server) buildSkillTools(userID string, skillNames []string) ([]agent.SkillTool, []string, map[string]string) {
	localStore := skills.NewLocalStore(s.config.HomeDir)
	systemSkills := skills.LoadAllSystemSkills()

	var skillTools []agent.SkillTool
	var skillInstructions []string
	secretEnv := make(map[string]string)

	for _, name := range skillNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		var st agent.SkillTool
		var requiredSecrets []string

		if sf, ok := systemSkills[name]; ok {
			// System skill — from embed FS
			st = agent.SkillTool{
				ID:           "system/" + name,
				Name:         sf.Manifest.Name,
				Description:  sf.Manifest.Description,
				Instructions: sf.Body,
				DirectTools:  sf.Manifest.Tools,
			}
			if len(sf.ScriptDirs) > 0 {
				// Scripts are copied to disk by InstallSystemSkills()
				// SkillDir = skill root (e.g., ~/.tofi/skills/web-search), NOT the scripts/ subdirectory.
				// DirectTool.Script paths are relative to this root (e.g., "scripts/search.py").
				localStore := skills.NewLocalStore(s.config.HomeDir)
				st.SkillDir = localStore.SkillDir(name)
			}
			requiredSecrets = sf.Manifest.RequiredSecrets
		} else {
			// User skill — from filesystem
			sf, err := localStore.LoadSkill(userID, name)
			if err != nil {
				log.Printf("[chat] skill %q not found: %v", name, err)
				continue
			}
			st = agent.SkillTool{
				ID:           "user/" + name,
				Name:         sf.Manifest.Name,
				Description:  sf.Manifest.Description,
				Instructions: sf.Body,
				DirectTools:  sf.Manifest.Tools,
			}
			if len(sf.ScriptDirs) > 0 {
				st.SkillDir = sf.Dir
			}
			requiredSecrets = sf.Manifest.RequiredSecrets
		}

		skillTools = append(skillTools, st)
		if st.Instructions != "" {
			skillInstructions = append(skillInstructions, st.Instructions)
		}

		// Resolve secrets: user DB first, then system env fallback
		for _, secretName := range requiredSecrets {
			if _, ok := secretEnv[secretName]; ok {
				continue
			}
			// 1. Try user's own key from encrypted DB
			secretRec, err := s.db.GetSecret(userID, secretName)
			if err == nil {
				val, err := crypto.Decrypt(secretRec.EncryptedValue)
				if err == nil && val != "" {
					secretEnv[secretName] = val
					continue
				}
			}
			// 2. Fallback to system environment variable
			if sysVal := os.Getenv(secretName); sysVal != "" {
				secretEnv[secretName] = sysVal
			}
			// 3. If neither exists, don't inject — let the script handle fallback
		}
	}

	return skillTools, skillInstructions, secretEnv
}

// buildCapabilitiesFromJSON parses capabilities JSON and returns MCP servers + extra tools.
func (s *Server) buildCapabilitiesFromJSON(userID, capsJSON string) ([]agent.MCPServerConfig, []agent.ExtraBuiltinTool) {
	caps, err := capability.Parse(capsJSON)
	if err != nil {
		log.Printf("⚠️ Invalid capabilities JSON: %v", err)
		return nil, nil
	}
	if caps == nil {
		return nil, nil
	}

	secretGetter := func(name string) (string, error) {
		rec, err := s.db.GetSecret(userID, name)
		if err != nil {
			return "", err
		}
		return crypto.Decrypt(rec.EncryptedValue)
	}

	if err := capability.ResolveSecrets(caps, secretGetter); err != nil {
		log.Printf("⚠️ Failed to resolve capability secrets: %v", err)
	}

	mcpServers := capability.BuildMCPServers(caps)
	extraTools := capability.BuildExtraTools(caps, secretGetter)

	return mcpServers, extraTools
}

// buildCapabilitiesFromMap marshals a capabilities map to JSON and delegates to buildCapabilitiesFromJSON.
func (s *Server) buildCapabilitiesFromMap(userID string, capsMap interface{}) ([]agent.MCPServerConfig, []agent.ExtraBuiltinTool) {
	capsJSON, err := json.Marshal(capsMap)
	if err != nil {
		log.Printf("⚠️ Failed to marshal capabilities: %v", err)
		return nil, nil
	}
	return s.buildCapabilitiesFromJSON(userID, string(capsJSON))
}
