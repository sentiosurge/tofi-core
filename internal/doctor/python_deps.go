package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"tofi-core/internal/paths"
)

const catPythonDeps = "Python Deps"

// requiredPythonPkg describes a Python package needed by system skills.
type requiredPythonPkg struct {
	ImportName string // python import name
	PipName    string // pip install name
	UsedBy     string // which system skill uses it
}

var requiredPythonPkgs = []requiredPythonPkg{
	{"ddgs", "ddgs", "web-search"},
	{"trafilatura", "trafilatura", "web-fetch"},
}

// CheckPythonDeps verifies Python packages in the isolated venv.
func CheckPythonDeps(homeDir string) []CheckResult {
	var results []CheckResult

	// Can't check if python3 isn't available
	if _, err := exec.LookPath("python3"); err != nil {
		results = append(results, newFail(catPythonDeps, "Python3", "not installed — cannot check packages"))
		return results
	}

	venvDir := paths.PythonVenvDir()
	venvPython := paths.PythonVenvBin()

	// Check venv exists
	if _, err := os.Stat(venvPython); os.IsNotExist(err) {
		// venv missing — all packages are unfixable until venv is created
		results = append(results, newFixable(
			catPythonDeps, "Python venv", "not found at "+venvDir,
			"python3 -m venv "+venvDir,
			SeverityWarn,
			func() error { return createVenv(venvDir) },
		))

		// Each package is also fixable (fix will create venv first then install)
		for _, pkg := range requiredPythonPkgs {
			p := pkg // capture
			results = append(results, newFixable(
				catPythonDeps, p.PipName, "venv missing (used by "+p.UsedBy+")",
				"pip install "+p.PipName,
				SeverityWarn,
				func() error { return installPackage(venvDir, p) },
			))
		}
		return results
	}

	// Verify venv python is executable
	if err := exec.Command(venvPython, "--version").Run(); err != nil {
		results = append(results, newFixable(
			catPythonDeps, "Python venv", "corrupted — python3 not executable",
			"recreate venv at "+venvDir,
			SeverityWarn,
			func() error {
				os.RemoveAll(venvDir)
				return createVenv(venvDir)
			},
		))
		return results
	}
	results = append(results, newOK(catPythonDeps, "Python venv", venvDir))

	// Check each required package
	for _, pkg := range requiredPythonPkgs {
		p := pkg // capture
		if checkPythonImport(venvPython, p.ImportName) {
			results = append(results, newOK(catPythonDeps, p.PipName, "installed (used by "+p.UsedBy+")"))
		} else {
			results = append(results, newFixable(
				catPythonDeps, p.PipName, "missing (used by "+p.UsedBy+")",
				"pip install "+p.PipName,
				SeverityWarn,
				func() error { return installPackage(venvDir, p) },
			))
		}
	}

	return results
}

// checkPythonImport tests if a package can be imported.
func checkPythonImport(pythonBin, importName string) bool {
	cmd := exec.Command(pythonBin, "-c", fmt.Sprintf("import %s", importName))
	return cmd.Run() == nil
}

// createVenv creates an isolated Python venv.
func createVenv(venvDir string) error {
	if err := os.MkdirAll(filepath.Dir(venvDir), 0755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	cmd := exec.Command("python3", "-m", "venv", venvDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("python3 -m venv: %w", err)
	}
	return nil
}

// installPackage installs a single Python package into the venv.
// Creates the venv first if it doesn't exist.
func installPackage(venvDir string, pkg requiredPythonPkg) error {
	venvPython := filepath.Join(venvDir, "bin", "python3")

	// Ensure venv exists
	if _, err := os.Stat(venvPython); os.IsNotExist(err) {
		if err := createVenv(venvDir); err != nil {
			return fmt.Errorf("create venv: %w", err)
		}
	}

	pipBin := filepath.Join(venvDir, "bin", "pip")
	cmd := exec.Command(pipBin, "install", pkg.PipName)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pip install %s: %w", pkg.PipName, err)
	}
	return nil
}
