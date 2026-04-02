package doctor

import (
	"fmt"
	"os"
	"path/filepath"
)

const catDirectories = "Directories"

// CheckDirectories verifies the TOFI_HOME directory structure.
func CheckDirectories(homeDir string) []CheckResult {
	var results []CheckResult

	// TOFI_HOME must exist (created by tofi init)
	if _, err := os.Stat(homeDir); os.IsNotExist(err) {
		results = append(results, newFail(catDirectories, "Home directory", homeDir+" (not found — run tofi init)"))
		return results // nothing else to check
	}
	results = append(results, newOK(catDirectories, "Home directory", homeDir))

	// Required subdirectories — auto-fixable
	requiredDirs := []struct {
		name string
		path string
	}{
		{"users/", filepath.Join(homeDir, "users")},
		{"skills/", filepath.Join(homeDir, "skills")},
		{"packages/", filepath.Join(homeDir, "packages")},
		{"logs/", filepath.Join(homeDir, "logs")},
	}

	for _, d := range requiredDirs {
		if _, err := os.Stat(d.path); os.IsNotExist(err) {
			p := d.path // capture for closure
			results = append(results, newFixable(
				catDirectories, d.name, "missing",
				fmt.Sprintf("mkdir -p %s", p),
				SeverityWarn,
				func() error { return os.MkdirAll(p, 0755) },
			))
		} else {
			results = append(results, newOK(catDirectories, d.name, ""))
		}
	}

	return results
}
