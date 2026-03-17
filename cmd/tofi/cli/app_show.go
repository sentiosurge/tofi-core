package cli

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var appShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show agent details",
	Args:  cobra.ExactArgs(1),
	RunE:  runAppShow,
}

func init() {
	appCmd.AddCommand(appShowCmd)
}

// appDetail is the full app record from the API.
type appDetail struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Status       string `json:"status"`
	Model        string `json:"model"`
	Prompt       string `json:"prompt"`
	SystemPrompt string `json:"system_prompt"`
	Source       string `json:"source"`
	Skills       string `json:"skills"`
	Schedule     string `json:"schedule"`
	UserID       string `json:"user_id"`
}

func runAppShow(cmd *cobra.Command, args []string) error {
	name := args[0]

	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ " + err.Error()))
		fmt.Println()
		return err
	}

	// Fetch all apps and find by name
	var apps []appDetail
	if err := client.get("/api/v1/apps", &apps); err != nil {
		return fmt.Errorf("failed to fetch apps: %w", err)
	}

	var app *appDetail
	for i := range apps {
		if apps[i].Name == name {
			app = &apps[i]
			break
		}
	}

	if app == nil {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ Agent not found: ") + accentStyle.Render(name))
		fmt.Println()
		return fmt.Errorf("agent %q not found", name)
	}

	fmt.Println()
	renderAppDetail(app)
	fmt.Println()

	return nil
}

func renderAppDetail(app *appDetail) {
	border := lipgloss.NewStyle().Foreground(lipgloss.Color("#30363d"))

	// Title bar
	statusDot := successStyle.Render("●")
	if app.Status != "active" {
		statusDot = subtitleStyle.Render("○")
	}

	topLine := border.Render("╭─ ") + titleStyle.Render(app.Name) + border.Render(" " + strings.Repeat("─", max(0, 46-lipgloss.Width(app.Name))) + "╮")
	fmt.Println("  " + topLine)

	// Fields
	rows := []struct {
		label string
		value string
	}{
		{"Status", statusDot + " " + capitalize(app.Status)},
		{"Model", app.Model},
		{"Source", app.Source},
	}

	if app.Description != "" {
		rows = append([]struct {
			label string
			value string
		}{{"Desc", app.Description}}, rows...)
	}

	if app.Skills != "" && app.Skills != "[]" && app.Skills != "null" {
		rows = append(rows, struct {
			label string
			value string
		}{"Skills", app.Skills})
	}

	if app.Schedule != "" && app.Schedule != "null" {
		rows = append(rows, struct {
			label string
			value string
		}{"Schedule", app.Schedule})
	}

	for _, r := range rows {
		val := r.value
		if val == "" {
			val = subtitleStyle.Render("—")
		} else {
			val = accentStyle.Render(val)
		}
		fmt.Printf("  %s  %-10s %s%s\n",
			border.Render("│"),
			subtitleStyle.Render(r.label),
			val,
			strings.Repeat(" ", max(0, 38-lipgloss.Width(val)))+border.Render("│"))
	}

	// Prompt preview
	if app.Prompt != "" {
		fmt.Printf("  %s%s\n", border.Render("│"), strings.Repeat(" ", 51)+border.Render("│"))
		fmt.Printf("  %s  %s%s\n",
			border.Render("│"),
			subtitleStyle.Render("Instructions"),
			strings.Repeat(" ", 39)+border.Render("│"))

		// Show first 3 lines of prompt
		lines := strings.Split(app.Prompt, "\n")
		shown := 0
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if len(line) > 46 {
				line = line[:43] + "..."
			}
			fmt.Printf("  %s  %s%s\n",
				border.Render("│"),
				subtitleStyle.Render(line),
				strings.Repeat(" ", max(0, 49-lipgloss.Width(line)))+border.Render("│"))
			shown++
			if shown >= 3 {
				if len(lines) > 3 {
					fmt.Printf("  %s  %s%s\n",
						border.Render("│"),
						subtitleStyle.Render("..."),
						strings.Repeat(" ", 48)+border.Render("│"))
				}
				break
			}
		}
	}

	// Bottom
	botLine := border.Render("╰" + strings.Repeat("─", 52) + "╯")
	fmt.Println("  " + botLine)

	// Quick actions
	fmt.Println()
	fmt.Printf("  %s run  %s edit  %s configure  %s delete\n",
		accentStyle.Render("tofi app"),
		accentStyle.Render("tofi app"),
		accentStyle.Render("tofi app"),
		accentStyle.Render("tofi app"))
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
