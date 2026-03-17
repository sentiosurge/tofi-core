package cli

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var connectCmd = &cobra.Command{
	Use:     "connect",
	Aliases: []string{"conn"},
	Short:   "Manage notification connectors (Telegram, Slack, Discord, Email)",
	RunE:    runConnConfigure,
}

var connectHelpCmd = &cobra.Command{
	Use:     "commands",
	Aliases: []string{"cmds"},
	Short:   "Show all connect subcommands",
	RunE:    runConnectHelp,
}

func init() {
	rootCmd.AddCommand(connectCmd)
	connectCmd.AddCommand(connectHelpCmd)
}

func runConnectHelp(cmd *cobra.Command, args []string) error {
	cmdStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f0f6fc"))

	content := "\n" +
		titleStyle.Render("Connect") + subtitleStyle.Render(" — Telegram, Slack, Discord, Email") + "\n\n" +
		titleStyle.Render("Get started") + "\n" +
		"  " + cmdStyle.Render("tofi connect") + subtitleStyle.Render("             Interactive setup wizard") + "\n\n" +
		subtitleStyle.Render("Commands") + "\n"

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
		content += fmt.Sprintf("  %s  %s\n", cmdStyle.Render(fmt.Sprintf("%-28s", c.name)), subtitleStyle.Render(c.desc))
	}

	content += "\n" + subtitleStyle.Render("Use ") + cmdStyle.Render("tofi connect <command> --help") + subtitleStyle.Render(" for details")

	fmt.Println("\n" + renderBox(content))
	fmt.Println()
	return nil
}
