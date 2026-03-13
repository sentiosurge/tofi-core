package capability

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"tofi-core/internal/mcp"
	"tofi-core/internal/provider"
)

// BuildNotifyTool creates an ExtraBuiltinTool for push notifications.
// Supports Discord webhooks and Telegram Bot API.
func BuildNotifyTool(channels []string, getter SecretGetter) mcp.ExtraBuiltinTool {
	channelList := strings.Join(channels, ", ")
	return mcp.ExtraBuiltinTool{
		Schema: provider.Tool{
			Name:        "send_notification",
			Description: fmt.Sprintf("Send a push notification to the user. Available channels: %s. Use this to notify the user of important results, completions, or alerts.", channelList),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"channel": map[string]any{
						"type":        "string",
						"description": fmt.Sprintf("Notification channel to use: %s", channelList),
						"enum":        channels,
					},
					"message": map[string]any{
						"type":        "string",
						"description": "The notification message to send",
					},
				},
				"required": []string{"channel", "message"},
			},
		},
		Handler: func(args map[string]any) (string, error) {
			channel, _ := args["channel"].(string)
			message, _ := args["message"].(string)
			if channel == "" || message == "" {
				return "Error: channel and message are required", nil
			}

			switch channel {
			case "discord":
				return sendDiscordNotification(message, getter)
			case "telegram":
				return sendTelegramNotification(message, getter)
			default:
				return fmt.Sprintf("Unknown channel: %s", channel), nil
			}
		},
	}
}

// sendDiscordNotification posts a message via Discord webhook.
func sendDiscordNotification(message string, getter SecretGetter) (string, error) {
	webhookURL, err := getter("DISCORD_WEBHOOK_URL")
	if err != nil || webhookURL == "" {
		return "Error: DISCORD_WEBHOOK_URL secret not configured. Please add it in Settings > Secrets.", nil
	}

	payload, _ := json.Marshal(map[string]string{"content": message})
	req, err := http.NewRequest("POST", webhookURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Sprintf("Failed to create request: %v", err), nil
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Sprintf("Discord notification failed: %v", err), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return "Discord notification sent successfully.", nil
	}
	return fmt.Sprintf("Discord returned HTTP %d", resp.StatusCode), nil
}

// sendTelegramNotification posts a message via Telegram Bot API.
func sendTelegramNotification(message string, getter SecretGetter) (string, error) {
	botToken, err := getter("TELEGRAM_BOT_TOKEN")
	if err != nil || botToken == "" {
		return "Error: TELEGRAM_BOT_TOKEN secret not configured. Please add it in Settings > Secrets.", nil
	}
	chatID, err := getter("TELEGRAM_CHAT_ID")
	if err != nil || chatID == "" {
		return "Error: TELEGRAM_CHAT_ID secret not configured. Please add it in Settings > Secrets.", nil
	}

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)
	payload, _ := json.Marshal(map[string]string{
		"chat_id": chatID,
		"text":    message,
	})

	req, err := http.NewRequest("POST", apiURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Sprintf("Failed to create request: %v", err), nil
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Sprintf("Telegram notification failed: %v", err), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return "Telegram notification sent successfully.", nil
	}
	return fmt.Sprintf("Telegram returned HTTP %d", resp.StatusCode), nil
}
