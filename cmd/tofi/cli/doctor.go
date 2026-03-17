package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"tofi-core/internal/daemon"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check system health and configuration",
	Long:  "Run diagnostics on your Tofi setup: workspace, API keys, runtime dependencies, and system info.",
	RunE:  runDoctor,
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}

type checkResult struct {
	label   string
	status  string // "ok", "warn", "fail"
	detail  string
}

func runDoctor(cmd *cobra.Command, args []string) error {
	fmt.Println()
	fmt.Println(titleStyle.Render("  Tofi Doctor"))
	fmt.Println(subtitleStyle.Render("  Checking your setup...\n"))

	var checks []checkResult

	// --- Workspace ---
	checks = append(checks, sectionHeader("Workspace"))
	checks = append(checks, checkWorkspace()...)

	// --- API Keys ---
	checks = append(checks, sectionHeader("API Configuration"))
	checks = append(checks, checkAPIKeys()...)

	// --- Engine ---
	checks = append(checks, sectionHeader("Engine"))
	checks = append(checks, checkEngine()...)

	// --- Runtime Dependencies ---
	checks = append(checks, sectionHeader("Runtime"))
	checks = append(checks, checkRuntime()...)

	// --- System Info ---
	checks = append(checks, sectionHeader("System"))
	checks = append(checks, checkSystem()...)

	// Render
	for _, c := range checks {
		if c.status == "section" {
			fmt.Printf("\n  %s\n", titleStyle.Render(c.label))
			continue
		}

		icon := successStyle.Render("✓")
		detailStyle := subtitleStyle
		switch c.status {
		case "warn":
			icon = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffa657")).Render("⚠")
			detailStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffa657"))
		case "fail":
			icon = errorStyle.Render("✗")
			detailStyle = errorStyle
		case "info":
			icon = accentStyle.Render("●")
		}

		if c.detail != "" {
			fmt.Printf("  %s %-22s %s\n", icon, c.label, detailStyle.Render(c.detail))
		} else {
			fmt.Printf("  %s %s\n", icon, c.label)
		}
	}

	fmt.Println()
	return nil
}

func sectionHeader(name string) checkResult {
	return checkResult{label: name, status: "section"}
}

// --- Workspace checks ---

func checkWorkspace() []checkResult {
	var results []checkResult

	// Home dir exists?
	if _, err := os.Stat(homeDir); os.IsNotExist(err) {
		results = append(results, checkResult{
			label:  "Home directory",
			status: "fail",
			detail: homeDir + " (not found — run tofi init)",
		})
		return results
	}
	results = append(results, checkResult{
		label:  "Home directory",
		status: "ok",
		detail: homeDir,
	})

	// config.yaml?
	configPath := filepath.Join(homeDir, "config.yaml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		results = append(results, checkResult{
			label:  "Config file",
			status: "fail",
			detail: "config.yaml not found",
		})
	} else {
		results = append(results, checkResult{
			label:  "Config file",
			status: "ok",
			detail: "config.yaml",
		})
	}

	// Subdirectories
	for _, dir := range []string{"users", "skills", "logs"} {
		p := filepath.Join(homeDir, dir)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			results = append(results, checkResult{
				label:  dir + "/",
				status: "warn",
				detail: "missing",
			})
		} else {
			results = append(results, checkResult{
				label:  dir + "/",
				status: "ok",
				detail: "",
			})
		}
	}

	return results
}

// --- API Key checks ---

type simpleConfig struct {
	Provider string `yaml:"provider"`
	APIKey   string `yaml:"api_key"`
}

func checkAPIKeys() []checkResult {
	var results []checkResult

	configPath := filepath.Join(homeDir, "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		results = append(results, checkResult{
			label:  "Config",
			status: "fail",
			detail: "cannot read config.yaml",
		})
		return results
	}

	var cfg simpleConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		results = append(results, checkResult{
			label:  "Config parse",
			status: "fail",
			detail: err.Error(),
		})
		return results
	}

	// Provider
	if cfg.Provider != "" {
		results = append(results, checkResult{
			label:  "Provider",
			status: "ok",
			detail: cfg.Provider,
		})
	} else {
		results = append(results, checkResult{
			label:  "Provider",
			status: "warn",
			detail: "not set",
		})
	}

	// API Key
	if cfg.APIKey != "" {
		masked := maskKey(cfg.APIKey)
		results = append(results, checkResult{
			label:  "API Key",
			status: "ok",
			detail: masked,
		})
	} else {
		// Check env vars as fallback
		envFound := false
		for _, env := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY"} {
			if v := os.Getenv(env); v != "" {
				results = append(results, checkResult{
					label:  "API Key",
					status: "ok",
					detail: env + " (from env) " + maskKey(v),
				})
				envFound = true
				break
			}
		}
		if !envFound {
			results = append(results, checkResult{
				label:  "API Key",
				status: "fail",
				detail: "not configured — run tofi init",
			})
		}
	}

	return results
}

// --- Engine checks ---

func checkEngine() []checkResult {
	var results []checkResult

	status := daemon.GetStatus(homeDir, startPort)
	if status.Running {
		results = append(results, checkResult{
			label:  "Engine",
			status: "ok",
			detail: fmt.Sprintf("running (pid %d, port %d)", status.PID, startPort),
		})
	} else {
		results = append(results, checkResult{
			label:  "Engine",
			status: "warn",
			detail: "not running",
		})
	}

	return results
}

// --- Runtime dependency checks ---

func checkRuntime() []checkResult {
	var results []checkResult

	// Python
	results = append(results, checkCommand("Python", "python3", "--version"))

	// Node.js
	results = append(results, checkCommand("Node.js", "node", "--version"))

	// npm
	results = append(results, checkCommand("npm", "npm", "--version"))

	// Go (optional, for development)
	results = append(results, checkCommand("Go", "go", "version"))

	// Git
	results = append(results, checkCommand("Git", "git", "--version"))

	return results
}

func checkCommand(label string, name string, args ...string) checkResult {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	if err != nil {
		return checkResult{
			label:  label,
			status: "warn",
			detail: "not found",
		}
	}

	version := strings.TrimSpace(string(out))
	// Clean up verbose output
	version = strings.TrimPrefix(version, "go version ")
	version = strings.TrimPrefix(version, "git version ")
	version = strings.TrimPrefix(version, "Python ")

	return checkResult{
		label:  label,
		status: "ok",
		detail: version,
	}
}

// --- System info ---

func checkSystem() []checkResult {
	var results []checkResult

	// OS
	results = append(results, checkResult{
		label:  "OS",
		status: "info",
		detail: runtime.GOOS + "/" + runtime.GOARCH,
	})

	// CPU cores
	results = append(results, checkResult{
		label:  "CPU Cores",
		status: "info",
		detail: fmt.Sprintf("%d", runtime.NumCPU()),
	})

	// Go version (runtime)
	results = append(results, checkResult{
		label:  "Go Runtime",
		status: "info",
		detail: runtime.Version(),
	})

	return results
}
