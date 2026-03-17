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
			Foreground(lipgloss.Color("#d2a8ff"))

	subtitleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8b949e"))

	successStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#3fb950"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#f85149"))

	accentStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#58a6ff"))
)

var logo = `
  ╺╋╸┏━┓┏━╸╻
   ┃ ┃ ┃┣╸ ┃
   ╹ ┗━┛╹  ╹`

var rootCmd = &cobra.Command{
	Use:   "tofi",
	Short: "Tofi — AI App Engine",
	Long: lipgloss.NewStyle().Foreground(lipgloss.Color("#ff7b72")).Render(logo) + "\n\n" +
		titleStyle.Render("Tofi — AI App Engine") + "\n" +
		subtitleStyle.Render("Create, manage, and run AI agents with natural language,\nskills, scheduling, and memory."),
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
