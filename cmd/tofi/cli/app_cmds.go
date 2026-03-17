package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// --- tofi app run <name> ---

var appRunCmd = &cobra.Command{
	Use:   "run <name>",
	Short: "Trigger a manual run of an app",
	Args:  cobra.ExactArgs(1),
	RunE:  runAppRun,
}

func runAppRun(cmd *cobra.Command, args []string) error {
	name := args[0]
	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		return err
	}

	appID, err := resolveAppID(client, name)
	if err != nil {
		return err
	}

	var result struct {
		ID          string `json:"id"`
		Status      string `json:"status"`
		Trigger     string `json:"trigger_type"`
		ScheduledAt string `json:"scheduled_at"`
	}
	if err := client.post(fmt.Sprintf("/api/v1/apps/%s/run", appID), nil, &result); err != nil {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ ") + err.Error())
		fmt.Println()
		return err
	}

	fmt.Println()
	fmt.Println(successStyle.Render("  ✓ Run triggered for ") + accentStyle.Render(name))
	fmt.Println(subtitleStyle.Render("    Run ID: " + result.ID))
	fmt.Println()
	return nil
}

// --- tofi app runs <name> ---

var appRunsCmd = &cobra.Command{
	Use:   "runs <name>",
	Short: "List recent runs of an app",
	Args:  cobra.ExactArgs(1),
	RunE:  runAppRuns,
}

func runAppRuns(cmd *cobra.Command, args []string) error {
	name := args[0]
	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		return err
	}

	appID, err := resolveAppID(client, name)
	if err != nil {
		return err
	}

	var runs []struct {
		ID          string  `json:"id"`
		Status      string  `json:"status"`
		Trigger     string  `json:"trigger_type"`
		Result      string  `json:"result"`
		ScheduledAt string  `json:"scheduled_at"`
		StartedAt   *string `json:"started_at"`
		CompletedAt *string `json:"completed_at"`
	}
	if err := client.get(fmt.Sprintf("/api/v1/apps/%s/runs?limit=20", appID), &runs); err != nil {
		return fmt.Errorf("failed to fetch runs: %w", err)
	}

	fmt.Println()
	if len(runs) == 0 {
		fmt.Println(subtitleStyle.Render("  No runs found for ") + accentStyle.Render(name))
		fmt.Println()
		return nil
	}

	fmt.Printf("  %s %s\n\n",
		titleStyle.Render("Runs"),
		subtitleStyle.Render(fmt.Sprintf("— %s (%d)", name, len(runs))))

	// Header
	fmt.Printf("  %-14s %-10s %-10s %-20s %s\n",
		subtitleStyle.Render("ID"),
		subtitleStyle.Render("STATUS"),
		subtitleStyle.Render("TRIGGER"),
		subtitleStyle.Render("SCHEDULED"),
		subtitleStyle.Render("RESULT"))
	fmt.Println(subtitleStyle.Render("  " + strings.Repeat("─", 70)))

	for _, run := range runs {
		id := run.ID
		if len(id) > 12 {
			id = id[:12]
		}

		statusStyle := subtitleStyle
		switch run.Status {
		case "done":
			statusStyle = successStyle
		case "failed":
			statusStyle = errorStyle
		case "running":
			statusStyle = accentStyle
		}

		scheduled := formatTimeShort(run.ScheduledAt)
		result := run.Result
		if len(result) > 30 {
			result = result[:27] + "..."
		}
		if result == "" {
			result = "—"
		}

		fmt.Printf("  %-14s %-10s %-10s %-20s %s\n",
			subtitleStyle.Render(id),
			statusStyle.Render(run.Status),
			subtitleStyle.Render(run.Trigger),
			subtitleStyle.Render(scheduled),
			subtitleStyle.Render(result))
	}
	fmt.Println()
	return nil
}

// --- tofi app activate <name> ---

var appActivateCmd = &cobra.Command{
	Use:   "activate <name>",
	Short: "Activate app scheduling",
	Args:  cobra.ExactArgs(1),
	RunE:  runAppActivate,
}

func runAppActivate(cmd *cobra.Command, args []string) error {
	name := args[0]
	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		return err
	}

	appID, err := resolveAppID(client, name)
	if err != nil {
		return err
	}

	if err := client.post(fmt.Sprintf("/api/v1/apps/%s/activate", appID), nil, nil); err != nil {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ ") + err.Error())
		fmt.Println()
		return err
	}

	fmt.Println()
	fmt.Println(successStyle.Render("  ✓ ") + accentStyle.Render(name) + successStyle.Render(" activated"))
	fmt.Println()
	return nil
}

// --- tofi app deactivate <name> ---

var appDeactivateCmd = &cobra.Command{
	Use:   "deactivate <name>",
	Short: "Deactivate app scheduling",
	Args:  cobra.ExactArgs(1),
	RunE:  runAppDeactivate,
}

func runAppDeactivate(cmd *cobra.Command, args []string) error {
	name := args[0]
	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		return err
	}

	appID, err := resolveAppID(client, name)
	if err != nil {
		return err
	}

	var result struct {
		Cancelled int `json:"cancelled"`
	}
	if err := client.post(fmt.Sprintf("/api/v1/apps/%s/deactivate", appID), nil, &result); err != nil {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ ") + err.Error())
		fmt.Println()
		return err
	}

	fmt.Println()
	fmt.Println(successStyle.Render("  ✓ ") + accentStyle.Render(name) + successStyle.Render(" deactivated"))
	if result.Cancelled > 0 {
		fmt.Println(subtitleStyle.Render(fmt.Sprintf("    %d pending runs cancelled", result.Cancelled)))
	}
	fmt.Println()
	return nil
}

// --- tofi app delete <name> ---

var appDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete an app",
	Args:  cobra.ExactArgs(1),
	RunE:  runAppDelete,
}

func runAppDelete(cmd *cobra.Command, args []string) error {
	name := args[0]
	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		return err
	}

	appID, err := resolveAppID(client, name)
	if err != nil {
		return err
	}

	if err := client.delete(fmt.Sprintf("/api/v1/apps/%s", appID)); err != nil {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ ") + err.Error())
		fmt.Println()
		return err
	}

	fmt.Println()
	fmt.Println(successStyle.Render("  ✓ ") + accentStyle.Render(name) + successStyle.Render(" deleted"))
	fmt.Println()
	return nil
}

// --- tofi app create ---

var appCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new app (interactive)",
	Long: `Create a new app interactively.

Examples:
  tofi app create
  tofi app create --name "DailyNews" --prompt "Fetch and summarize tech news"
  tofi app create --name "StockTracker" --model gpt-4o --skills web-search`,
	RunE: runAppCreate,
}

var (
	createName   string
	createPrompt string
	createModel  string
	createSkills string
)

func init() {
	appCmd.AddCommand(appRunCmd)
	appCmd.AddCommand(appRunsCmd)
	appCmd.AddCommand(appActivateCmd)
	appCmd.AddCommand(appDeactivateCmd)
	appCmd.AddCommand(appDeleteCmd)
	appCmd.AddCommand(appCreateCmd)

	appCreateCmd.Flags().StringVar(&createName, "name", "", "App name")
	appCreateCmd.Flags().StringVar(&createPrompt, "prompt", "", "App instructions")
	appCreateCmd.Flags().StringVar(&createModel, "model", "", "LLM model")
	appCreateCmd.Flags().StringVar(&createSkills, "skills", "", "Comma-separated skills")
}

func runAppCreate(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ " + err.Error()))
		fmt.Println()
		return err
	}

	if createName == "" {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ --name is required"))
		fmt.Println(subtitleStyle.Render("  Usage: tofi app create --name \"MyApp\" --prompt \"...\""))
		fmt.Println()
		return fmt.Errorf("--name is required")
	}

	if createPrompt == "" {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ --prompt is required"))
		fmt.Println(subtitleStyle.Render("  Usage: tofi app create --name \"MyApp\" --prompt \"...\""))
		fmt.Println()
		return fmt.Errorf("--prompt is required")
	}

	var skillsList []string
	if createSkills != "" {
		for _, s := range strings.Split(createSkills, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				skillsList = append(skillsList, s)
			}
		}
	}

	body := map[string]any{
		"name":   createName,
		"prompt": createPrompt,
	}
	if createModel != "" {
		body["model"] = createModel
	}
	if len(skillsList) > 0 {
		body["skills"] = skillsList
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	var result struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := client.post("/api/v1/apps", bytes.NewReader(jsonBody), &result); err != nil {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ ") + err.Error())
		fmt.Println()
		return err
	}

	fmt.Println()
	fmt.Println(successStyle.Render("  ✓ App created: ") + accentStyle.Render(result.Name))
	fmt.Println(subtitleStyle.Render("    ID: " + result.ID))
	fmt.Println()
	fmt.Println(subtitleStyle.Render("  Next steps:"))
	fmt.Println(accentStyle.Render("    tofi app show " + result.Name))
	fmt.Println(accentStyle.Render("    tofi app run " + result.Name))
	fmt.Println(accentStyle.Render("    tofi app activate " + result.Name))
	fmt.Println()
	return nil
}

// --- Helpers ---

// resolveAppID finds an app by name and returns its ID.
func resolveAppID(client *apiClient, name string) (string, error) {
	var apps []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := client.get("/api/v1/apps", &apps); err != nil {
		return "", fmt.Errorf("failed to fetch apps: %w", err)
	}

	for _, a := range apps {
		if a.Name == name {
			return a.ID, nil
		}
	}

	// Also try as ID directly
	for _, a := range apps {
		if a.ID == name {
			return a.ID, nil
		}
	}

	return "", fmt.Errorf("app %q not found", name)
}

// formatTimeShort formats an ISO datetime string to a short display format.
func formatTimeShort(isoTime string) string {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02 15:04:05"} {
		t, err := time.Parse(layout, isoTime)
		if err == nil {
			if t.Year() == time.Now().Year() {
				return t.Format("Jan 02 15:04")
			}
			return t.Format("2006-01-02")
		}
	}
	if len(isoTime) > 16 {
		return isoTime[:16]
	}
	return isoTime
}
