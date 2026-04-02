package doctor

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"tofi-core/internal/skills"
)

const catSystemSkills = "System Skills"

// CheckSystemSkills verifies system skill scripts on disk match the embedded versions.
func CheckSystemSkills(homeDir string) []CheckResult {
	var results []CheckResult
	skillsDir := filepath.Join(homeDir, "skills")

	embedFS := skills.GetSystemSkillsFS()

	// Collect all expected script files from embedded FS
	type scriptEntry struct {
		embedPath string // path in embed FS: "system-skills/web-fetch/scripts/fetch.py"
		diskPath  string // expected on disk: ~/.tofi/skills/web-fetch/scripts/fetch.py
		skillName string // directory name: "web-fetch"
	}

	var expected []scriptEntry

	entries, err := embedFS.ReadDir("system-skills")
	if err != nil {
		results = append(results, newFail(catSystemSkills, "Embedded FS", "cannot read: "+err.Error()))
		return results
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirName := entry.Name()
		scriptsPath := filepath.Join("system-skills", dirName, "scripts")

		// Walk scripts recursively
		_ = fs.WalkDir(embedFS, scriptsPath, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			// Compute disk path: skillsDir/dirName/scripts/filename
			rel, _ := filepath.Rel(scriptsPath, path)
			diskPath := filepath.Join(skillsDir, dirName, "scripts", rel)

			expected = append(expected, scriptEntry{
				embedPath: path,
				diskPath:  diskPath,
				skillName: dirName,
			})
			return nil
		})

		// Also check sub-skills (skill pack pattern)
		subEntries, err := embedFS.ReadDir(filepath.Join("system-skills", dirName))
		if err != nil {
			continue
		}
		for _, sub := range subEntries {
			if !sub.IsDir() {
				continue
			}
			subScriptsPath := filepath.Join("system-skills", dirName, sub.Name(), "scripts")
			_ = fs.WalkDir(embedFS, subScriptsPath, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				rel, _ := filepath.Rel(subScriptsPath, path)
				// Sub-skill scripts go under the sub-skill name
				subSkillDir := filepath.Join(dirName, sub.Name())
				diskPath := filepath.Join(skillsDir, subSkillDir, "scripts", rel)

				expected = append(expected, scriptEntry{
					embedPath: path,
					diskPath:  diskPath,
					skillName: subSkillDir,
				})
				return nil
			})
		}
	}

	if len(expected) == 0 {
		results = append(results, newInfo(catSystemSkills, "Scripts", "no system skill scripts found in embedded FS"))
		return results
	}

	// Check each expected script
	for _, se := range expected {
		label := se.skillName + "/" + filepath.Base(se.diskPath)
		embedData, err := embedFS.ReadFile(se.embedPath)
		if err != nil {
			results = append(results, newWarn(catSystemSkills, label, "cannot read from embedded FS"))
			continue
		}

		diskData, err := os.ReadFile(se.diskPath)
		if os.IsNotExist(err) {
			// Missing on disk — fixable
			ep := se.embedPath
			dp := se.diskPath
			ed := embedData
			results = append(results, newFixable(
				catSystemSkills, label, "missing on disk",
				"copy from embedded FS",
				SeverityWarn,
				func() error { return writeScript(dp, ed, ep) },
			))
			continue
		}
		if err != nil {
			results = append(results, newWarn(catSystemSkills, label, "cannot read: "+err.Error()))
			continue
		}

		// Compare contents
		if !bytes.Equal(embedData, diskData) {
			ep := se.embedPath
			dp := se.diskPath
			ed := embedData
			results = append(results, newFixable(
				catSystemSkills, label, "outdated (differs from embedded version)",
				"overwrite with embedded version",
				SeverityWarn,
				func() error { return writeScript(dp, ed, ep) },
			))
			continue
		}

		// Check permissions for executable scripts
		info, _ := os.Stat(se.diskPath)
		if info != nil && isExecutableScript(se.diskPath) && info.Mode().Perm()&0111 == 0 {
			dp := se.diskPath
			results = append(results, newFixable(
				catSystemSkills, label, "not executable",
				"chmod 755 "+dp,
				SeverityWarn,
				func() error { return os.Chmod(dp, 0755) },
			))
			continue
		}

		results = append(results, newOK(catSystemSkills, label, ""))
	}

	// Check for orphaned system skill directories
	results = append(results, checkOrphanedSkills(skillsDir, embedFS)...)

	return results
}

// checkOrphanedSkills finds skill directories on disk that no longer exist in embedded FS.
func checkOrphanedSkills(skillsDir string, embedFS fs.FS) []CheckResult {
	var results []CheckResult

	// Build set of embedded system skill directory names
	embeddedNames := make(map[string]bool)
	entries, err := fs.ReadDir(embedFS, "system-skills")
	if err != nil {
		return results
	}
	for _, e := range entries {
		if e.IsDir() {
			embeddedNames[e.Name()] = true
		}
	}

	// Scan disk skills directory
	diskEntries, err := os.ReadDir(skillsDir)
	if err != nil {
		return results
	}

	// Get list of all known system skill names (from SKILL.md metadata)
	systemSkillNames := make(map[string]bool)
	for _, name := range skills.ListSystemSkillNames() {
		systemSkillNames[name] = true
	}

	for _, de := range diskEntries {
		if !de.IsDir() {
			continue
		}
		dirName := de.Name()

		// Skip if it's a current embedded system skill directory
		if embeddedNames[dirName] {
			continue
		}

		// Skip user-installed skills (those that have SKILL.md on disk but not in embedded FS)
		// We identify orphans as: directory exists on disk, looks like a system skill
		// (no user SKILL.md), and not in embedded FS
		diskSkillMD := filepath.Join(skillsDir, dirName, "SKILL.md")
		if _, err := os.Stat(diskSkillMD); err == nil {
			// Has SKILL.md on disk — likely user-installed, skip
			continue
		}

		// No SKILL.md on disk + not in embedded FS = orphaned system skill
		dirPath := filepath.Join(skillsDir, dirName)
		dp := dirPath
		results = append(results, newFixable(
			catSystemSkills, dirName, "orphaned (no longer in embedded FS)",
			fmt.Sprintf("rm -rf %s", dp),
			SeverityInfo,
			func() error { return os.RemoveAll(dp) },
		))
	}

	return results
}

// writeScript writes embedded data to disk with appropriate permissions.
func writeScript(diskPath string, data []byte, embedPath string) error {
	if err := os.MkdirAll(filepath.Dir(diskPath), 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	perm := os.FileMode(0644)
	if isExecutableScript(embedPath) {
		perm = 0755
	}
	return os.WriteFile(diskPath, data, perm)
}

func isExecutableScript(path string) bool {
	return strings.HasSuffix(path, ".py") || strings.HasSuffix(path, ".sh")
}
