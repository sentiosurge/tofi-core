package cli

import (
	"fmt"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var (
	homeDir string
	verbose bool
)

// Styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#ff7b72"))

	subtitleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8b949e"))

	successStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#3fb950"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#f85149"))

	accentStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#58a6ff"))
)

var logoText = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff7b72")).Render("/") +
	lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f0f6fc")).Render("tofi")

// logo is the standalone banner (used by other files for backward compat)
var logo = logoText

// boxStyle is the shared frame for all CLI output panels
var boxStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("#30363d")).
	Padding(1, 2).
	Width(52)

// renderBox wraps content in a branded box with /tofi header
func renderBox(content string) string {
	header := logoText + "  " + subtitleStyle.Render("AI App Engine")
	return boxStyle.Render(header + "\n" + content)
}

// tuiBoxStyle is a wider box for interactive TUI wizards
var tuiBoxStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("#30363d")).
	Padding(1, 2).
	Width(68)

// renderTUIBox wraps TUI content in a branded box with a section title
func renderTUIBox(section string, content string) string {
	header := logoText + "  " + titleStyle.Render(section)
	return tuiBoxStyle.Render(header + "\n\n" + content)
}

// tuiSelectedRow is the shared highlight style for TUI list selection
// Usage: tuiSelectedRow.Render("► " + label)
var tuiSelectedRow = lipgloss.NewStyle().
	Background(lipgloss.Color("#ff7b72")).
	Foreground(lipgloss.Color("#0d1117")).
	Bold(true)

var rootCmd = &cobra.Command{
	Use:   "tofi",
	Short: "Tofi — AI App Engine",
	RunE: func(cmd *cobra.Command, args []string) error {
		// ./tofi with no args → branded quick-reference
		cmdStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f0f6fc"))
		content := "\n" +
			subtitleStyle.Render("Create, manage, and run AI agents") + "\n" +
			subtitleStyle.Render("with skills, scheduling, and memory.") + "\n\n" +
			titleStyle.Render("Quick start") + "\n" +
			"  " + cmdStyle.Render("tofi chat") + subtitleStyle.Render("          Start chatting with AI") + "\n" +
			"  " + cmdStyle.Render("tofi chat --agent") + subtitleStyle.Render("   Chat with a specific agent") + "\n\n" +
			titleStyle.Render("Engine") + "\n" +
			"  " + cmdStyle.Render("tofi start") + subtitleStyle.Render("        Launch the engine") + "\n" +
			"  " + cmdStyle.Render("tofi stop") + subtitleStyle.Render("         Stop the engine") + "\n" +
			"  " + cmdStyle.Render("tofi restart") + subtitleStyle.Render("      Restart the engine") + "\n" +
			"  " + cmdStyle.Render("tofi status") + subtitleStyle.Render("       Show engine status") + "\n\n" +
			titleStyle.Render("More") + "\n" +
			"  " + cmdStyle.Render("tofi app") + subtitleStyle.Render("          Manage AI agents") + "\n" +
			"  " + cmdStyle.Render("tofi connect") + subtitleStyle.Render("      Set up notifications") + "\n" +
			"  " + cmdStyle.Render("tofi help") + subtitleStyle.Render("         All commands")
		fmt.Println("\n" + renderBox(content))
		fmt.Println()
		return nil
	},
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	defaultHome := os.Getenv("TOFI_HOME")
	if defaultHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			defaultHome = ".tofi"
		} else {
			defaultHome = home + "/.tofi"
		}
	}

	rootCmd.PersistentFlags().StringVar(&homeDir, "home", defaultHome, "tofi home directory")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")
}

// Execute runs the root command.
func Execute() error {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: "+err.Error()))
		return err
	}
	return nil
}
