package doctor

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

const catEnvironment = "Environment"

// CheckEnvironment verifies external runtime dependencies.
func CheckEnvironment() []CheckResult {
	var results []CheckResult

	// Python3 — required for system skills
	results = append(results, checkPython3())

	// pip3 or uv — needed to install Python deps
	results = append(results, checkPipOrUV()...)

	// Chrome/Chromium — needed for web-fetch
	results = append(results, checkChrome())

	// Git — needed for skill install from git
	results = append(results, checkCommandVersion("Git", "git", "--version", SeverityWarn))

	// Node/npm — optional, for future skills
	results = append(results, checkCommandVersion("Node.js", "node", "--version", SeverityInfo))
	results = append(results, checkCommandVersion("npm", "npm", "--version", SeverityInfo))

	return results
}

func checkPython3() CheckResult {
	path, err := exec.LookPath("python3")
	if err != nil {
		hint := "install python3"
		switch runtime.GOOS {
		case "darwin":
			hint = "brew install python3"
		case "linux":
			hint = "apt install python3  (or dnf install python3)"
		}
		return newFail(catEnvironment, "Python3", "not found — "+hint)
	}

	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return newFail(catEnvironment, "Python3", "found but --version failed")
	}
	version := strings.TrimSpace(strings.TrimPrefix(string(out), "Python "))
	return newOK(catEnvironment, "Python3", version)
}

func checkPipOrUV() []CheckResult {
	var results []CheckResult

	pipPath, pipErr := exec.LookPath("pip3")
	uvPath, uvErr := exec.LookPath("uv")

	if pipErr != nil && uvErr != nil {
		hint := "install pip3 or uv for Python dependency management"
		switch runtime.GOOS {
		case "darwin":
			hint = "brew install uv  (or pip3 comes with python3)"
		case "linux":
			hint = "apt install python3-pip  (or install uv)"
		}
		results = append(results, newWarn(catEnvironment, "pip3/uv", "not found — "+hint))
		return results
	}

	if pipErr == nil {
		out, _ := exec.Command(pipPath, "--version").Output()
		version := strings.TrimSpace(string(out))
		// pip 24.0 from /path/to/pip (python 3.12) → keep first part
		if idx := strings.Index(version, " from "); idx > 0 {
			version = version[:idx]
		}
		results = append(results, newOK(catEnvironment, "pip3", version))
	}
	if uvErr == nil {
		out, _ := exec.Command(uvPath, "--version").Output()
		results = append(results, newOK(catEnvironment, "uv", strings.TrimSpace(string(out))))
	}

	return results
}

func checkChrome() CheckResult {
	var candidates []string

	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	case "linux":
		candidates = []string{
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
			"/usr/bin/chromium-browser",
			"/usr/bin/chromium",
			"/snap/bin/chromium",
		}
	default:
		// Windows or unsupported — skip
		return newInfo(catEnvironment, "Chrome", "check skipped on "+runtime.GOOS)
	}

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return newOK(catEnvironment, "Chrome", p)
		}
	}

	return newWarn(catEnvironment, "Chrome", "not found — needed for web-fetch (headless browsing)")
}

func checkCommandVersion(label, name, arg string, missingSeverity Severity) CheckResult {
	path, err := exec.LookPath(name)
	if err != nil {
		return CheckResult{
			Category: catEnvironment,
			Label:    label,
			Severity: missingSeverity,
			Detail:   "not found",
		}
	}

	out, err := exec.Command(path, arg).Output()
	if err != nil {
		return CheckResult{
			Category: catEnvironment,
			Label:    label,
			Severity: missingSeverity,
			Detail:   "found but " + arg + " failed",
		}
	}

	version := strings.TrimSpace(string(out))
	// Clean verbose prefixes
	version = strings.TrimPrefix(version, "git version ")
	return newOK(catEnvironment, label, version)
}
