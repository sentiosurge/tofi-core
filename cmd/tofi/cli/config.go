package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:     "config",
	Aliases: []string{"cfg"},
	Short:   "Manage configuration (interactive wizard)",
	RunE:    runConfigWizard,
}

var configHelpCmd = &cobra.Command{
	Use:     "commands",
	Aliases: []string{"cmds"},
	Short:   "Show all config subcommands",
	RunE:    runConfigHelp,
}

func init() {
	rootCmd.AddCommand(configCmd)

	configCmd.AddCommand(configHelpCmd)
	configCmd.AddCommand(configWizardCmd)
	configCmd.AddCommand(configKeysCmd)
	configCmd.AddCommand(configSetKeyCmd)
	configCmd.AddCommand(configDeleteKeyCmd)
	configCmd.AddCommand(configModelCmd)
}

func runConfigHelp(cmd *cobra.Command, args []string) error {
	content := titleStyle.Render("Config Commands") + "\n\n"

	commands := []struct {
		cmd  string
		desc string
	}{
		{"tofi config", "Interactive configuration wizard"},
		{"tofi config keys", "List configured AI provider keys"},
		{"tofi config set-key <provider> <key>", "Set an AI provider key"},
		{"tofi config delete-key <provider>", "Delete an AI provider key"},
		{"tofi config model [name]", "View or set preferred model"},
	}

	cmdStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f0f6fc"))
	for _, c := range commands {
		content += cmdStyle.Render(c.cmd) + "\n"
		content += subtitleStyle.Render("  "+c.desc) + "\n\n"
	}

	fmt.Println(renderBox(content))
	return nil
}

// --- tofi config keys ---

var configKeysCmd = &cobra.Command{
	Use:   "keys",
	Short: "List configured AI provider keys",
	RunE:  runConfigKeys,
}

func runConfigKeys(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		return err
	}

	var result struct {
		System []map[string]string `json:"system"`
		User   []map[string]string `json:"user"`
		Env    []map[string]string `json:"env"`
	}
	if err := client.get("/api/v1/settings/ai-keys", &result); err != nil {
		return fmt.Errorf("failed to fetch keys: %w", err)
	}

	fmt.Println()
	border := lipgloss.NewStyle().Foreground(lipgloss.Color("#30363d"))
	hasKeys := false

	if len(result.System) > 0 {
		hasKeys = true
		fmt.Println(titleStyle.Render("  System Keys") + subtitleStyle.Render(" (database)"))
		fmt.Println()
		for _, k := range result.System {
			fmt.Printf("  %s  %-12s %s\n",
				border.Render("│"),
				accentStyle.Render(k["provider"]),
				subtitleStyle.Render(k["masked_key"]))
		}
		fmt.Println()
	}

	if len(result.User) > 0 {
		hasKeys = true
		fmt.Println(titleStyle.Render("  User Keys") + subtitleStyle.Render(" (database)"))
		fmt.Println()
		for _, k := range result.User {
			fmt.Printf("  %s  %-12s %s\n",
				border.Render("│"),
				accentStyle.Render(k["provider"]),
				subtitleStyle.Render(k["masked_key"]))
		}
		fmt.Println()
	}

	if len(result.Env) > 0 {
		hasKeys = true
		fmt.Println(titleStyle.Render("  Environment Keys") + subtitleStyle.Render(" (env vars)"))
		fmt.Println()
		for _, k := range result.Env {
			fmt.Printf("  %s  %-12s %s  %s\n",
				border.Render("│"),
				accentStyle.Render(k["provider"]),
				subtitleStyle.Render(k["masked_key"]),
				subtitleStyle.Render("("+k["source"]+")"))
		}
		fmt.Println()
	}

	hasDBKeys := len(result.System) > 0 || len(result.User) > 0

	if !hasDBKeys && len(result.Env) > 0 {
		fmt.Println(subtitleStyle.Render("  No keys saved to database."))
		fmt.Println(subtitleStyle.Render("  Detected from environment variables — these work but won't appear in the UI."))
		fmt.Println(subtitleStyle.Render("  To persist, run: ") + accentStyle.Render("tofi config set-key <provider> <key>"))
		fmt.Println()
	}

	if !hasKeys {
		fmt.Println(subtitleStyle.Render("  No API keys configured."))
		fmt.Println(subtitleStyle.Render("  Set one with: ") + accentStyle.Render("tofi config set-key anthropic sk-ant-..."))
		fmt.Println()
	}

	return nil
}

// --- tofi config set-key <provider> <key> ---

var configSetKeyCmd = &cobra.Command{
	Use:   "set-key <provider> <key>",
	Short: "Set an AI provider key",
	Long: `Set an API key for a provider.

Providers: anthropic, openai, gemini

Examples:
  tofi config set-key anthropic sk-ant-api03-...
  tofi config set-key openai sk-...`,
	Args: cobra.ExactArgs(2),
	RunE: runConfigSetKey,
}

func runConfigSetKey(cmd *cobra.Command, args []string) error {
	provider := strings.ToLower(args[0])
	apiKey := args[1]

	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		return err
	}

	body := map[string]string{
		"provider": provider,
		"api_key":  apiKey,
		"scope":    "system",
	}
	jsonBody, _ := json.Marshal(body)

	if err := client.post("/api/v1/settings/ai-keys", bytes.NewReader(jsonBody), nil); err != nil {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ ") + err.Error())
		fmt.Println()
		return err
	}

	fmt.Println()
	fmt.Println(successStyle.Render("  ✓ ") + accentStyle.Render(provider) + successStyle.Render(" key saved"))
	fmt.Println()
	return nil
}

// --- tofi config delete-key <provider> ---

var configDeleteKeyCmd = &cobra.Command{
	Use:   "delete-key <provider>",
	Short: "Delete an AI provider key",
	Args:  cobra.ExactArgs(1),
	RunE:  runConfigDeleteKey,
}

func runConfigDeleteKey(cmd *cobra.Command, args []string) error {
	provider := strings.ToLower(args[0])

	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		return err
	}

	if err := client.delete(fmt.Sprintf("/api/v1/settings/ai-keys/%s", provider)); err != nil {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ ") + err.Error())
		fmt.Println()
		return err
	}

	fmt.Println()
	fmt.Println(successStyle.Render("  ✓ ") + accentStyle.Render(provider) + successStyle.Render(" key deleted"))
	fmt.Println()
	return nil
}

// --- tofi config model [name] ---

var configModelCmd = &cobra.Command{
	Use:   "model [name]",
	Short: "View or set preferred model",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runConfigModel,
}

func runConfigModel(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		return err
	}

	if len(args) == 0 {
		// Show current model
		var result struct {
			Model string `json:"model"`
		}
		if err := client.get("/api/v1/settings/preferred-model", &result); err != nil {
			return err
		}
		fmt.Println()
		if result.Model == "" {
			fmt.Println(subtitleStyle.Render("  No preferred model set (using default)"))
		} else {
			fmt.Println(subtitleStyle.Render("  Preferred model: ") + accentStyle.Render(result.Model))
		}
		fmt.Println()
		return nil
	}

	// Set model
	modelName := args[0]
	body := map[string]string{"model": modelName}
	jsonBody, _ := json.Marshal(body)

	if err := client.post("/api/v1/settings/preferred-model", bytes.NewReader(jsonBody), nil); err != nil {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ ") + err.Error())
		fmt.Println()
		return err
	}

	fmt.Println()
	fmt.Println(successStyle.Render("  ✓ Preferred model: ") + accentStyle.Render(modelName))
	fmt.Println()
	return nil
}

// maskKey is defined in init.go
