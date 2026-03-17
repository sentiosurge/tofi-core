package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"tofi-core/internal/daemon"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show engine status",
	Long:  "Display the current state of the Tofi engine including uptime, agents, and schedule.",
	RunE:  runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

// appsCount fetches the number of apps from the API.
func appsCount(port int) (total int, active int) {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/api/v1/apps", port))
	if err != nil {
		return 0, 0
	}
	defer resp.Body.Close()

	var result []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, 0
	}

	total = len(result)
	for _, app := range result {
		if s, ok := app["status"].(string); ok && s == "active" {
			active++
		}
	}
	return total, active
}

func runStatus(cmd *cobra.Command, args []string) error {
	fmt.Println()

	status := daemon.GetStatus(homeDir, startPort)

	// Logo
	logoStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff7b72"))
	fmt.Println(logoStyle.Render(logo))
	fmt.Println()

	if !status.Running {
		badge := lipgloss.NewStyle().
			Background(lipgloss.Color("#30363d")).
			Foreground(lipgloss.Color("#8b949e")).
			Padding(0, 1).
			Render(" Engine Stopped ")

		fmt.Printf("  %s\n\n", badge)
		fmt.Printf("  %s\n\n", subtitleStyle.Render("Run ")+accentStyle.Render("tofi start")+subtitleStyle.Render(" to launch the engine"))
		return nil
	}

	// Running badge
	badge := lipgloss.NewStyle().
		Background(lipgloss.Color("#238636")).
		Foreground(lipgloss.Color("#ffffff")).
		Padding(0, 1).
		Render(" Engine Running ")

	uptime := status.Uptime
	if uptime == "" {
		uptime = "unknown"
	}

	fmt.Printf("  %s  %s  %s\n", badge, subtitleStyle.Render("uptime "+uptime), subtitleStyle.Render(fmt.Sprintf("pid %d", status.PID)))
	fmt.Println(subtitleStyle.Render("  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"))


	// Agents
	total, active := appsCount(startPort)
	inactive := total - active
	fmt.Printf("  %s        %s  %s\n",
		titleStyle.Render("Agents"),
		successStyle.Render(fmt.Sprintf("%d active", active)),
		subtitleStyle.Render(fmt.Sprintf("%d inactive  %d total", inactive, total)))

	// Listening
	fmt.Printf("  %s          %s\n",
		titleStyle.Render("Port"),
		accentStyle.Render(fmt.Sprintf("localhost:%d", startPort)))

	fmt.Println()
	return nil
}
