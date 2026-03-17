package cli

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var appListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all agents",
	RunE:  runAppList,
}

func init() {
	appCmd.AddCommand(appListCmd)
}

// appSummary represents the minimal app info returned by the API.
type appSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Model       string `json:"model"`
	Source      string `json:"source"`
}

func runAppList(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ " + err.Error()))
		fmt.Println()
		return err
	}

	var apps []appSummary
	if err := client.get("/api/v1/apps", &apps); err != nil {
		return fmt.Errorf("failed to fetch apps: %w", err)
	}

	fmt.Println()

	if len(apps) == 0 {
		fmt.Println(subtitleStyle.Render("  No agents found."))
		fmt.Println(subtitleStyle.Render("  Create one with: ") + accentStyle.Render("tofi app create"))
		fmt.Println()
		return nil
	}

	// Count
	active := 0
	for _, a := range apps {
		if a.Status == "active" {
			active++
		}
	}
	fmt.Printf("  %s %s\n\n",
		titleStyle.Render("Your Agents"),
		subtitleStyle.Render(fmt.Sprintf("(%d total)", len(apps))))

	// Box style
	boxBorder := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#30363d"))

	topLine := boxBorder.Render("  ╭" + strings.Repeat("─", 52) + "╮")
	midLine := boxBorder.Render("  ├" + strings.Repeat("─", 52) + "┤")
	botLine := boxBorder.Render("  ╰" + strings.Repeat("─", 52) + "╯")

	fmt.Println(topLine)

	for i, app := range apps {
		// Status indicator
		statusDot := successStyle.Render("●")
		statusBadge := lipgloss.NewStyle().
			Background(lipgloss.Color("#238636")).
			Foreground(lipgloss.Color("#ffffff")).
			Padding(0, 1).
			Render("active")

		if app.Status != "active" {
			statusDot = subtitleStyle.Render("○")
			statusBadge = lipgloss.NewStyle().
				Background(lipgloss.Color("#30363d")).
				Foreground(lipgloss.Color("#8b949e")).
				Padding(0, 1).
				Render(app.Status)
		}

		// Name line
		name := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f0f6fc")).Render(app.Name)
		fmt.Printf("  %s %s %s  %s\n",
			boxBorder.Render("│"),
			statusDot,
			padRight(name, 30),
			statusBadge)

		// Description
		desc := app.Description
		if len(desc) > 46 {
			desc = desc[:43] + "..."
		}
		if desc != "" {
			fmt.Printf("  %s   %s\n",
				boxBorder.Render("│"),
				subtitleStyle.Render(desc))
		}

		// Model + source
		model := app.Model
		if model == "" {
			model = "default"
		}
		fmt.Printf("  %s   %s\n",
			boxBorder.Render("│"),
			accentStyle.Render(model)+subtitleStyle.Render(" · "+app.Source))

		if i < len(apps)-1 {
			fmt.Println(midLine)
		}
	}

	fmt.Println(botLine)

	// Summary
	fmt.Printf("\n  %s\n\n",
		subtitleStyle.Render(fmt.Sprintf("%d agents (%d active, %d inactive)", len(apps), active, len(apps)-active)))

	return nil
}

// padRight pads a string with spaces to reach the target visible width.
func padRight(s string, width int) string {
	// lipgloss.Width counts visible characters (not ANSI escape sequences)
	visibleLen := lipgloss.Width(s)
	if visibleLen >= width {
		return s
	}
	return s + strings.Repeat(" ", width-visibleLen)
}
