package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

// --- tofi connect list ---

var connListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all connectors",
	Args:  cobra.NoArgs,
	RunE:  runConnList,
}

func runConnList(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		return err
	}

	var connectors []struct {
		ID            string `json:"id"`
		Type          string `json:"type"`
		Name          string `json:"name"`
		Scope         string `json:"scope"`
		AppName       string `json:"app_name"`
		Enabled       bool   `json:"enabled"`
		ReceiverCount int    `json:"receiver_count"`
		CanReceive    bool   `json:"can_receive"`
		CreatedAt     string `json:"created_at"`
	}
	if err := client.get("/api/v1/connectors", &connectors); err != nil {
		return err
	}

	fmt.Println()
	if len(connectors) == 0 {
		fmt.Println(subtitleStyle.Render("  No connectors configured."))
		fmt.Println(subtitleStyle.Render("  Add one with: ") + accentStyle.Render("tofi connect add telegram --token <BOT_TOKEN>"))
		fmt.Println()
		return nil
	}

	fmt.Println(titleStyle.Render("  Connectors"))
	fmt.Println()

	for _, c := range connectors {
		icon := connectorIcon(c.Type)
		scope := subtitleStyle.Render("(global)")
		if c.Scope != "" && c.Scope != "global:*" {
			appLabel := c.Scope
			if c.AppName != "" {
				appLabel = "app: " + c.AppName
			}
			scope = titleStyle.Render("(" + appLabel + ")")
		}

		status := successStyle.Render("●")
		if !c.Enabled {
			status = subtitleStyle.Render("○")
		}

		mode := ""
		if c.CanReceive {
			mode = subtitleStyle.Render("  [interactive]")
		} else {
			mode = subtitleStyle.Render("  [notify-only]")
		}

		receivers := subtitleStyle.Render(fmt.Sprintf("  %d receivers", c.ReceiverCount))

		fmt.Printf("  %s %s %s %s%s%s\n", status, icon, accentStyle.Render(c.Type), scope, mode, receivers)
		fmt.Println(subtitleStyle.Render("    ID: " + c.ID))
	}
	fmt.Println()
	return nil
}

// --- tofi connect add <type> ---

var connAddCmd = &cobra.Command{
	Use:   "add <type>",
	Short: "Add a new connector (telegram, slack_webhook, slack_app, discord_webhook, discord_bot, email)",
	Args:  cobra.ExactArgs(1),
	RunE:  runConnAdd,
}

var (
	connAddToken   string
	connAddAppID   string
	connAddAppName string
	connAddName    string
	connAddWebhook string
)

func runConnAdd(cmd *cobra.Command, args []string) error {
	ctype := args[0]
	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		return err
	}

	// 构建 config
	config := map[string]string{}
	switch ctype {
	case "telegram":
		if connAddToken == "" {
			return fmt.Errorf("--token is required for telegram")
		}
		config["bot_token"] = connAddToken
	case "slack_webhook", "discord_webhook":
		if connAddWebhook == "" {
			return fmt.Errorf("--webhook is required for %s", ctype)
		}
		config["webhook_url"] = connAddWebhook
	case "slack_app":
		if connAddToken == "" {
			return fmt.Errorf("--token is required for slack_app")
		}
		config["bot_token"] = connAddToken
	case "discord_bot":
		if connAddToken == "" {
			return fmt.Errorf("--token is required for discord_bot")
		}
		config["bot_token"] = connAddToken
	case "email":
		// email config 后续完善
	default:
		return fmt.Errorf("unsupported type: %s\nSupported: telegram, slack_webhook, slack_app, discord_webhook, discord_bot, email", ctype)
	}

	// 解析 scope（可以传 app name 或 id → scope）
	scope := "global:*"
	if connAddAppID != "" {
		scope = "app:" + connAddAppID
	} else if connAddAppName != "" {
		resolvedID, err := resolveAppID(client, connAddAppName)
		if err != nil {
			return fmt.Errorf("app not found: %s", connAddAppName)
		}
		scope = "app:" + resolvedID
	}

	configJSON, _ := json.Marshal(config)

	body := map[string]any{
		"type":   ctype,
		"name":   connAddName,
		"scope":  scope,
		"config": json.RawMessage(configJSON),
	}
	bodyJSON, _ := json.Marshal(body)

	var result struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if err := client.post("/api/v1/connectors", bytes.NewReader(bodyJSON), &result); err != nil {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ ") + err.Error())
		fmt.Println()
		return err
	}

	fmt.Println()
	fmt.Println(successStyle.Render("  ✓ Connector created"))
	fmt.Println(subtitleStyle.Render("    ID: " + result.ID))
	fmt.Println(subtitleStyle.Render("    Type: " + ctype))
	fmt.Println(subtitleStyle.Render("    Scope: " + scope))
	fmt.Println()
	fmt.Println(subtitleStyle.Render("  Next: add receivers with ") + accentStyle.Render("tofi connect verify "+result.ID))
	fmt.Println()
	return nil
}

// --- tofi connect remove <id> ---

var connRemoveCmd = &cobra.Command{
	Use:   "remove <id>",
	Short: "Remove a connector",
	Args:  cobra.ExactArgs(1),
	RunE:  runConnRemove,
}

func runConnRemove(cmd *cobra.Command, args []string) error {
	connID := args[0]
	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		return err
	}

	if err := client.delete("/api/v1/connectors/" + connID); err != nil {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ ") + err.Error())
		fmt.Println()
		return err
	}

	fmt.Println()
	fmt.Println(successStyle.Render("  ✓ Connector removed"))
	fmt.Println()
	return nil
}

// --- tofi connect verify <id> ---

var connVerifyCmd = &cobra.Command{
	Use:   "verify <id>",
	Short: "Add a receiver by sending a verification code",
	Args:  cobra.ExactArgs(1),
	RunE:  runConnVerify,
}

func runConnVerify(cmd *cobra.Command, args []string) error {
	connID := args[0]
	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		return err
	}

	var result struct {
		Code        string `json:"code"`
		BotName     string `json:"bot_name"`
		BotUsername string `json:"bot_username"`
		ConnectorID string `json:"connector_id"`
	}
	if err := client.post(fmt.Sprintf("/api/v1/connectors/%s/verify", connID), nil, &result); err != nil {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ ") + err.Error())
		fmt.Println()
		return err
	}

	fmt.Println()
	fmt.Println(titleStyle.Render("  Verification Code"))
	fmt.Println()

	codeStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#ff7b72")).
		Background(lipgloss.Color("#21262d")).
		Padding(0, 2)

	fmt.Println("  " + codeStyle.Render(result.Code))
	fmt.Println()
	if result.BotUsername != "" {
		fmt.Println(subtitleStyle.Render("  Send this code to ") +
			accentStyle.Render("@"+result.BotUsername) +
			subtitleStyle.Render(" on Telegram"))
	} else {
		fmt.Println(subtitleStyle.Render("  Send this code to your bot on Telegram"))
	}
	fmt.Println(subtitleStyle.Render("  Waiting for verification (5 min timeout)..."))
	fmt.Println()

	// 轮询验证状态
	for i := 0; i < 60; i++ {
		time.Sleep(5 * time.Second)

		var status struct {
			Verifying bool `json:"verifying"`
		}
		if err := client.get(fmt.Sprintf("/api/v1/connectors/%s/verify-status", connID), &status); err != nil {
			continue
		}
		if !status.Verifying {
			// 验证完成（或超时），检查 receivers
			var receivers []struct {
				ID          int64  `json:"id"`
				DisplayName string `json:"display_name"`
			}
			if err := client.get(fmt.Sprintf("/api/v1/connectors/%s/receivers", connID), &receivers); err == nil && len(receivers) > 0 {
				last := receivers[len(receivers)-1]
				fmt.Println(successStyle.Render("  ✓ Verified: ") + accentStyle.Render(last.DisplayName))
				fmt.Println()
				return nil
			}
			fmt.Println(subtitleStyle.Render("  Verification expired or failed."))
			fmt.Println()
			return nil
		}
	}

	fmt.Println(subtitleStyle.Render("  Timed out waiting for verification."))
	fmt.Println()
	return nil
}

// --- tofi connect receivers <id> ---

var connReceiversCmd = &cobra.Command{
	Use:   "receivers <id>",
	Short: "List receivers of a connector",
	Args:  cobra.ExactArgs(1),
	RunE:  runConnReceivers,
}

func runConnReceivers(cmd *cobra.Command, args []string) error {
	connID := args[0]
	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		return err
	}

	var receivers []struct {
		ID          int64  `json:"id"`
		Identifier  string `json:"identifier"`
		DisplayName string `json:"display_name"`
		AvatarURL   string `json:"avatar_url"`
		VerifiedAt  string `json:"verified_at"`
	}
	if err := client.get(fmt.Sprintf("/api/v1/connectors/%s/receivers", connID), &receivers); err != nil {
		return err
	}

	fmt.Println()
	if len(receivers) == 0 {
		fmt.Println(subtitleStyle.Render("  No receivers."))
		fmt.Println(subtitleStyle.Render("  Add one with: ") + accentStyle.Render("tofi connect verify "+connID))
		fmt.Println()
		return nil
	}

	fmt.Println(titleStyle.Render("  Receivers"))
	fmt.Println()
	for _, r := range receivers {
		name := r.DisplayName
		if name == "" {
			name = r.Identifier
		}
		verified := ""
		if r.VerifiedAt != "" {
			verified = subtitleStyle.Render("  verified " + formatTimeShort(r.VerifiedAt))
		}
		fmt.Printf("  %s %s%s\n", accentStyle.Render("•"), name, verified)
		fmt.Println(subtitleStyle.Render(fmt.Sprintf("    ID: %d  Identifier: %s", r.ID, r.Identifier)))
	}
	fmt.Println()
	return nil
}

// --- tofi connect test <id> ---

var connTestCmd = &cobra.Command{
	Use:   "test <id>",
	Short: "Send a test message",
	Args:  cobra.ExactArgs(1),
	RunE:  runConnTest,
}

var connTestTo string

func runConnTest(cmd *cobra.Command, args []string) error {
	connID := args[0]
	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		return err
	}

	body := map[string]any{}
	bodyJSON, _ := json.Marshal(body)

	var result struct {
		OK bool `json:"ok"`
	}
	if err := client.post(fmt.Sprintf("/api/v1/connectors/%s/test", connID), bytes.NewReader(bodyJSON), &result); err != nil {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ ") + err.Error())
		fmt.Println()
		return err
	}

	fmt.Println()
	fmt.Println(successStyle.Render("  ✓ Test message sent"))
	fmt.Println()
	return nil
}

// --- tofi connect link <connector-id> --app <app-name> ---

var connLinkCmd = &cobra.Command{
	Use:   "link <connector-id>",
	Short: "Link a connector to an app",
	Args:  cobra.ExactArgs(1),
	RunE:  runConnLink,
}

var connLinkApp string

func runConnLink(cmd *cobra.Command, args []string) error {
	connID := args[0]
	if connLinkApp == "" {
		return fmt.Errorf("--app is required")
	}

	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		return err
	}

	appID, err := resolveAppID(client, connLinkApp)
	if err != nil {
		return fmt.Errorf("app not found: %s", connLinkApp)
	}

	body := map[string]string{"connector_id": connID}
	bodyJSON, _ := json.Marshal(body)

	var result struct {
		OK bool `json:"ok"`
	}
	if err := client.post(fmt.Sprintf("/api/v1/apps/%s/connectors", appID), bytes.NewReader(bodyJSON), &result); err != nil {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ ") + err.Error())
		fmt.Println()
		return err
	}

	fmt.Println()
	fmt.Println(successStyle.Render("  ✓ Connector linked to ") + accentStyle.Render(connLinkApp))
	fmt.Println()
	return nil
}

// --- tofi connect unlink <connector-id> --app <app-name> ---

var connUnlinkCmd = &cobra.Command{
	Use:   "unlink <connector-id>",
	Short: "Unlink a connector from an app",
	Args:  cobra.ExactArgs(1),
	RunE:  runConnUnlink,
}

var connUnlinkApp string

func runConnUnlink(cmd *cobra.Command, args []string) error {
	connID := args[0]
	if connUnlinkApp == "" {
		return fmt.Errorf("--app is required")
	}

	client := newAPIClient()
	if err := client.ensureRunning(); err != nil {
		return err
	}

	appID, err := resolveAppID(client, connUnlinkApp)
	if err != nil {
		return fmt.Errorf("app not found: %s", connUnlinkApp)
	}

	if err := client.delete(fmt.Sprintf("/api/v1/apps/%s/connectors/%s", appID, connID)); err != nil {
		fmt.Println()
		fmt.Println(errorStyle.Render("  ✗ ") + err.Error())
		fmt.Println()
		return err
	}

	fmt.Println()
	fmt.Println(successStyle.Render("  ✓ Connector unlinked from ") + accentStyle.Render(connUnlinkApp))
	fmt.Println()
	return nil
}

// --- helpers ---

func connectorIcon(ctype string) string {
	switch {
	case strings.HasPrefix(ctype, "telegram"):
		return "📨"
	case strings.HasPrefix(ctype, "slack"):
		return "💬"
	case strings.HasPrefix(ctype, "discord"):
		return "🎮"
	case ctype == "email":
		return "📧"
	default:
		return "🔗"
	}
}

// --- register subcommands ---

func init() {
	connAddCmd.Flags().StringVar(&connAddToken, "token", "", "bot token (telegram, slack_app, discord_bot)")
	connAddCmd.Flags().StringVar(&connAddWebhook, "webhook", "", "webhook URL (slack_webhook, discord_webhook)")
	connAddCmd.Flags().StringVar(&connAddAppID, "app-id", "", "scope to specific app (by ID)")
	connAddCmd.Flags().StringVar(&connAddAppName, "app", "", "scope to specific app (by name)")
	connAddCmd.Flags().StringVar(&connAddName, "name", "", "connector display name")

	connLinkCmd.Flags().StringVar(&connLinkApp, "app", "", "app name")
	connUnlinkCmd.Flags().StringVar(&connUnlinkApp, "app", "", "app name")

	connectCmd.AddCommand(connConfigureCmd) // interactive wizard first
	connectCmd.AddCommand(connListCmd)
	connectCmd.AddCommand(connAddCmd)
	connectCmd.AddCommand(connRemoveCmd)
	connectCmd.AddCommand(connVerifyCmd)
	connectCmd.AddCommand(connReceiversCmd)
	connectCmd.AddCommand(connTestCmd)
	connectCmd.AddCommand(connLinkCmd)
	connectCmd.AddCommand(connUnlinkCmd)
}
