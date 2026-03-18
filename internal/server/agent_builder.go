package server

import (
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"tofi-core/internal/capability"
	"tofi-core/internal/crypto"
	"tofi-core/internal/mcp"
	"tofi-core/internal/skills"
	"tofi-core/internal/storage"
)

// buildSkillToolsFromRecords builds SkillTool list from skill records and resolves secrets.
// Returns: skillTools, secretEnv, missingSecrets
func (s *Server) buildSkillToolsFromRecords(userID string, skillRecords []*storage.SkillRecord) ([]mcp.SkillTool, map[string]string, []string) {
	localStore := skills.NewLocalStore(s.config.HomeDir)
	var skillTools []mcp.SkillTool
	secretEnv := make(map[string]string)
	var missingSecrets []string

	for _, skill := range skillRecords {
		st := mcp.SkillTool{
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
func (s *Server) buildSkillToolsFromNames(userID string, skillNames []string) ([]mcp.SkillTool, []string, map[string]string) {
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

// buildCapabilitiesFromJSON parses capabilities JSON and returns MCP servers + extra tools.
func (s *Server) buildCapabilitiesFromJSON(userID, capsJSON string) ([]mcp.MCPServerConfig, []mcp.ExtraBuiltinTool) {
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
func (s *Server) buildCapabilitiesFromMap(userID string, capsMap interface{}) ([]mcp.MCPServerConfig, []mcp.ExtraBuiltinTool) {
	capsJSON, err := json.Marshal(capsMap)
	if err != nil {
		log.Printf("⚠️ Failed to marshal capabilities: %v", err)
		return nil, nil
	}
	return s.buildCapabilitiesFromJSON(userID, string(capsJSON))
}
