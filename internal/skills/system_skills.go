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

	"tofi-core/internal/models"
	"tofi-core/internal/storage"
)

//go:embed system-skills/*
var systemSkillsFS embed.FS

// GetSystemSkillsFS returns the embedded filesystem for external verification (e.g., doctor checks).
func GetSystemSkillsFS() embed.FS {
	return systemSkillsFS
}

// InstallSystemSkills scans the embedded system-skills directory and installs/updates
// each skill into the database and local store. Idempotent — only updates when needed.
// Also cleans up system skills that no longer exist in the embedded FS.
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

	// Track which skill names we install (for cleanup)
	installedNames := make(map[string]bool)

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()

		// Install the top-level skill if it has a SKILL.md
		skillMDPath := filepath.Join("system-skills", name, "SKILL.md")
		if data, err := systemSkillsFS.ReadFile(skillMDPath); err == nil {
			if err := installOneSystemSkill(db, store, name, ""); err != nil {
				log.Printf("[system-skills] failed to install %q: %v", name, err)
			} else {
				// Extract the actual name from SKILL.md frontmatter
				if sf, err := Parse(data); err == nil {
					installedNames[sf.Manifest.Name] = true
				}
				log.Printf("[system-skills] ✓ installed/updated %q", name)
			}
		}

		// Scan subdirectories for sub-skills (skill pack pattern)
		subEntries, err := systemSkillsFS.ReadDir(filepath.Join("system-skills", name))
		if err != nil {
			continue
		}
		packSourceURL := "system://" + name
		for _, sub := range subEntries {
			if !sub.IsDir() {
				continue
			}
			subSkillMD := filepath.Join("system-skills", name, sub.Name(), "SKILL.md")
			subData, err := systemSkillsFS.ReadFile(subSkillMD)
			if err != nil {
				continue
			}
			subPath := filepath.Join(name, sub.Name())
			if err := installOneSystemSkill(db, store, subPath, packSourceURL); err != nil {
				log.Printf("[system-skills] failed to install %q: %v", sub.Name(), err)
			} else {
				if sf, err := Parse(subData); err == nil {
					installedNames[sf.Manifest.Name] = true
				}
				log.Printf("[system-skills] ✓ installed/updated %q (pack: %s)", sub.Name(), name)
			}
		}
	}

	// Cleanup: remove system skills from DB that no longer exist in embedded FS
	existing, err := db.ListSystemSkills()
	if err != nil {
		log.Printf("[system-skills] failed to list existing: %v", err)
		return
	}
	for _, rec := range existing {
		if !installedNames[rec.Name] {
			if err := db.DeleteSkill(rec.ID, "system"); err != nil {
				log.Printf("[system-skills] failed to remove stale %q: %v", rec.Name, err)
			} else {
				log.Printf("[system-skills] 🗑 removed stale %q (no longer in embedded FS)", rec.Name)
			}
		}
	}
}

// installOneSystemSkill installs a single system skill from an embedded directory path.
// dirPath is relative to "system-skills/" (e.g., "web-fetch" or "app-manager/app-create").
// sourceURL groups sub-skills under a pack (e.g., "system://app-manager"); empty for top-level skills.
func installOneSystemSkill(db *storage.DB, store *LocalStore, dirPath string, sourceURL string) error {
	// 1. Read SKILL.md from embedded FS
	skillMDPath := filepath.Join("system-skills", dirPath, "SKILL.md")
	data, err := systemSkillsFS.ReadFile(skillMDPath)
	if err != nil {
		return fmt.Errorf("read SKILL.md: %w", err)
	}

	// 2. Parse the skill
	skillFile, err := Parse(data)
	if err != nil {
		return fmt.Errorf("parse SKILL.md: %w", err)
	}

	// Use the name from SKILL.md frontmatter (not directory name)
	skillName := skillFile.Manifest.Name

	// 3. Copy only scripts to local store (SKILL.md stays in embed FS, not written to disk)
	embeddedScriptsDir := filepath.Join("system-skills", dirPath, "scripts")
	if scriptEntries, err := systemSkillsFS.ReadDir(embeddedScriptsDir); err == nil && len(scriptEntries) > 0 {
		skillDir := store.SkillDir(skillName)
		scriptsDir := filepath.Join(skillDir, "scripts")
		os.MkdirAll(scriptsDir, 0755)
		if err := copyEmbeddedDir(systemSkillsFS, embeddedScriptsDir, scriptsDir); err != nil {
			log.Printf("[system-skills] warning: copy scripts for %q: %v", skillName, err)
		}
	}

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
		ID:              "system/" + skillName,
		Name:            skillName,
		Description:     skillFile.Manifest.Description,
		Version:         version,
		Scope:           "system",
		Source:          "builtin",
		SourceURL:       sourceURL,
		ManifestJSON:    manifestJSON,
		Instructions:    skillFile.Body,
		HasScripts:      hasScripts,
		RequiredSecrets: requiredSecrets,
		UserID:          "system",
	}

	return db.SaveSkill(record)
}

// --- Filesystem-first loading (no DB) ---

// ListSystemSkillNames returns all system skill names by scanning the embedded FS.
func ListSystemSkillNames() []string {
	result := loadAllSystemSkillsInternal()
	names := make([]string, 0, len(result))
	for name := range result {
		names = append(names, name)
	}
	return names
}

// LoadAllSystemSkills returns all system skills parsed from the embedded FS.
// Keys are manifest names (from SKILL.md frontmatter).
func LoadAllSystemSkills() map[string]*models.SkillFile {
	return loadAllSystemSkillsInternal()
}

func loadAllSystemSkillsInternal() map[string]*models.SkillFile {
	result := make(map[string]*models.SkillFile)

	entries, err := systemSkillsFS.ReadDir("system-skills")
	if err != nil {
		return result
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirName := entry.Name()

		// Top-level skill
		skillMDPath := filepath.Join("system-skills", dirName, "SKILL.md")
		if data, err := systemSkillsFS.ReadFile(skillMDPath); err == nil {
			if sf, err := Parse(data); err == nil {
				// Set Dir for scripts resolution
				sf.Dir = filepath.Join("system-skills", dirName)
				// Check for scripts in embed FS
				scriptsDir := filepath.Join("system-skills", dirName, "scripts")
				if scriptEntries, err := systemSkillsFS.ReadDir(scriptsDir); err == nil {
					for _, se := range scriptEntries {
						if !se.IsDir() {
							sf.ScriptDirs = append(sf.ScriptDirs, se.Name())
						}
					}
				}
				result[sf.Manifest.Name] = sf
			}
		}

		// Sub-skills (skill pack)
		subEntries, err := systemSkillsFS.ReadDir(filepath.Join("system-skills", dirName))
		if err != nil {
			continue
		}
		for _, sub := range subEntries {
			if !sub.IsDir() {
				continue
			}
			subSkillMD := filepath.Join("system-skills", dirName, sub.Name(), "SKILL.md")
			if data, err := systemSkillsFS.ReadFile(subSkillMD); err == nil {
				if sf, err := Parse(data); err == nil {
					sf.Dir = filepath.Join("system-skills", dirName, sub.Name())
					scriptsDir := filepath.Join(sf.Dir, "scripts")
					if scriptEntries, err := systemSkillsFS.ReadDir(scriptsDir); err == nil {
						for _, se := range scriptEntries {
							if !se.IsDir() {
								sf.ScriptDirs = append(sf.ScriptDirs, se.Name())
							}
						}
					}
					result[sf.Manifest.Name] = sf
				}
			}
		}
	}

	return result
}

// SystemSkillScriptsDir returns the on-disk path where system skill scripts are copied.
// Scripts need to be on disk for shell execution.
func SystemSkillScriptsDir(homeDir, skillName string) string {
	store := NewLocalStore(homeDir)
	return filepath.Join(store.SkillDir(skillName), "scripts")
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
