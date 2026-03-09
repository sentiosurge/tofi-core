package skills

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"tofi-core/internal/storage"
)

//go:embed system-skills/*
var systemSkillsFS embed.FS

// InstallSystemSkills scans the embedded system-skills directory and installs/updates
// each skill into the database and local store. Idempotent — only updates when needed.
func InstallSystemSkills(db *storage.DB, homeDir string) {
	entries, err := systemSkillsFS.ReadDir("system-skills")
	if err != nil {
		log.Printf("[system-skills] failed to read embedded directory: %v", err)
		return
	}

	store := NewLocalStore(homeDir)
	if err := store.EnsureDir(); err != nil {
		log.Printf("[system-skills] failed to create skills directory: %v", err)
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if err := installOneSystemSkill(db, store, name); err != nil {
			log.Printf("[system-skills] failed to install %q: %v", name, err)
		} else {
			log.Printf("[system-skills] ✓ installed/updated %q", name)
		}
	}
}

func installOneSystemSkill(db *storage.DB, store *LocalStore, name string) error {
	// 1. Read SKILL.md from embedded FS
	skillMDPath := filepath.Join("system-skills", name, "SKILL.md")
	data, err := systemSkillsFS.ReadFile(skillMDPath)
	if err != nil {
		return fmt.Errorf("read SKILL.md: %w", err)
	}

	// 2. Parse the skill
	skillFile, err := Parse(data)
	if err != nil {
		return fmt.Errorf("parse SKILL.md: %w", err)
	}

	// 3. Copy scripts to local store
	skillDir := store.SkillDir(name)
	scriptsDir := filepath.Join(skillDir, "scripts")
	os.MkdirAll(scriptsDir, 0755)

	embeddedScriptsDir := filepath.Join("system-skills", name, "scripts")
	if err := copyEmbeddedDir(systemSkillsFS, embeddedScriptsDir, scriptsDir); err != nil {
		log.Printf("[system-skills] warning: copy scripts for %q: %v", name, err)
	}

	// Also write SKILL.md to local store
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), data, 0644)

	// 4. Check if scripts exist
	hasScripts := false
	if scriptEntries, err := systemSkillsFS.ReadDir(embeddedScriptsDir); err == nil && len(scriptEntries) > 0 {
		hasScripts = true
	}

	// 5. Build required_secrets JSON
	requiredSecrets := "[]"
	if secrets := skillFile.Manifest.RequiredEnvVars(); len(secrets) > 0 {
		if b, err := json.Marshal(secrets); err == nil {
			requiredSecrets = string(b)
		}
	}

	// 6. Build manifest JSON
	manifestJSON := "{}"
	if b, err := json.Marshal(skillFile.Manifest); err == nil {
		manifestJSON = string(b)
	}

	// 7. Save to database
	version := skillFile.Manifest.Version
	if version == "" {
		version = "1.0"
	}

	record := &storage.SkillRecord{
		ID:              "system/" + name,
		Name:            name,
		Description:     skillFile.Manifest.Description,
		Version:         version,
		Scope:           "system",
		Source:          "builtin",
		ManifestJSON:    manifestJSON,
		Instructions:    skillFile.Body,
		HasScripts:      hasScripts,
		RequiredSecrets: requiredSecrets,
		UserID:          "system",
	}

	return db.SaveSkill(record)
}

// copyEmbeddedDir copies files from an embedded FS directory to a real directory.
func copyEmbeddedDir(embedFS embed.FS, srcDir, dstDir string) error {
	return fs.WalkDir(embedFS, srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Compute relative path
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		dstPath := filepath.Join(dstDir, rel)

		if d.IsDir() {
			return os.MkdirAll(dstPath, 0755)
		}

		data, err := embedFS.ReadFile(path)
		if err != nil {
			return err
		}

		// Make scripts executable
		perm := os.FileMode(0644)
		if strings.HasSuffix(path, ".py") || strings.HasSuffix(path, ".sh") {
			perm = 0755
		}

		return os.WriteFile(dstPath, data, perm)
	})
}
