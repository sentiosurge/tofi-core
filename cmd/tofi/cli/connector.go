package cli

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var connectorCmd = &cobra.Command{
	Use:     "connector",
	Aliases: []string{"conn"},
	Short:   "Manage notification connectors (Telegram, Slack, Discord, Email)",
	RunE:    runConnectorHelp,
}

func init() {
	rootCmd.AddCommand(connectorCmd)
}

func runConnectorHelp(cmd *cobra.Command, args []string) error {
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff7b72"))
	cmdStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#79c0ff"))
	descStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#8b949e"))
	highlightStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7ee787"))

	fmt.Println()
	fmt.Println(headerStyle.Render("  Connectors") + descStyle.Render(" — Telegram, Slack, Discord, Email"))
	fmt.Println()

	// Highlight configure
	fmt.Println(highlightStyle.Render("  Get started:"))
	fmt.Println("    " + cmdStyle.Render("tofi connector configure") + descStyle.Render("   Interactive setup wizard"))
	fmt.Println()

	// Other commands
	fmt.Println(descStyle.Render("  Commands:"))

	type cmdEntry struct {
		name string
		desc string
	}
	cmds := []cmdEntry{
		{"list", "List all connectors"},
		{"add <type>", "Add via CLI flags (for scripting/AI)"},
		{"remove <id>", "Remove a connector"},
		{"verify <id>", "Add a receiver (verification code)"},
		{"receivers <id>", "List receivers"},
		{"test <id>", "Send a test message"},
		{"link <id> --app <name>", "Link connector to an app"},
		{"unlink <id> --app <name>", "Unlink from an app"},
	}

	for _, c := range cmds {
		fmt.Printf("    %s  %s\n", cmdStyle.Render(fmt.Sprintf("%-28s", c.name)), descStyle.Render(c.desc))
	}

	fmt.Println()
	fmt.Println(descStyle.Render("  Use ") + cmdStyle.Render("tofi connector <command> --help") + descStyle.Render(" for details"))
	fmt.Println()
	return nil
}
