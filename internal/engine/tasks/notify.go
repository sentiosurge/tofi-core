package tasks

import (
	"encoding/json"
	"fmt"
	"strings"
	"tofi-core/internal/executor"
	"tofi-core/internal/models"
)

type Notify struct{}

func (n *Notify) Execute(config map[string]interface{}, ctx *models.ExecutionContext) (string, error) {
	channel := strings.ToLower(fmt.Sprint(config["channel"]))
	if channel == "" {
		channel = "webhook"
	}

	message := fmt.Sprint(config["message"])

	switch channel {
	case "webhook":
		return n.sendWebhook(config, message)
	case "slack":
		return n.sendSlack(config, message)
	case "discord":
		return n.sendDiscord(config, message)
	default:
		return "", fmt.Errorf("unsupported notification channel: %s", channel)
	}
}

func (n *Notify) Validate(node *models.Node) error {
	channel := fmt.Sprint(node.Config["channel"])
	if channel == "" {
		channel = "webhook"
	}

	switch channel {
	case "webhook", "slack", "discord":
		if _, ok := node.Config["url"]; !ok {
			return fmt.Errorf("config.url is required for %s notifications", channel)
		}
	default:
		return fmt.Errorf("unsupported notification channel: %s", channel)
	}
	return nil
}

// sendWebhook sends a generic webhook POST with JSON body
func (n *Notify) sendWebhook(config map[string]interface{}, message string) (string, error) {
	url := fmt.Sprint(config["url"])
	if url == "" {
		return "", fmt.Errorf("url is required for webhook notification")
	}

	// Build payload
	payload := map[string]interface{}{
		"message":   message,
		"timestamp": "{{_timestamp}}",
	}

	// Allow custom payload via "payload" config
	if customPayload, ok := config["payload"]; ok {
		if m, ok := customPayload.(map[string]interface{}); ok {
			for k, v := range m {
				payload[k] = v
			}
		}
	}

	body, _ := json.Marshal(payload)

	headers := map[string]string{
		"Content-Type": "application/json",
	}

	resp, err := executor.ExecuteHTTP("POST", url, headers, nil, string(body), 30)
	if err != nil {
		return "", fmt.Errorf("webhook notification failed: %v", err)
	}

	return fmt.Sprintf("Webhook sent to %s: %s", url, resp), nil
}

// sendSlack sends a message via Slack Incoming Webhook
func (n *Notify) sendSlack(config map[string]interface{}, message string) (string, error) {
	url := fmt.Sprint(config["url"])
	if url == "" {
		return "", fmt.Errorf("url (Slack webhook URL) is required")
	}

	// Slack expects {"text": "message"}
	payload := map[string]string{"text": message}
	body, _ := json.Marshal(payload)

	headers := map[string]string{
		"Content-Type": "application/json",
	}

	resp, err := executor.ExecuteHTTP("POST", url, headers, nil, string(body), 30)
	if err != nil {
		return "", fmt.Errorf("slack notification failed: %v", err)
	}

	return fmt.Sprintf("Slack message sent: %s", resp), nil
}

// sendDiscord sends a message via Discord Webhook
func (n *Notify) sendDiscord(config map[string]interface{}, message string) (string, error) {
	url := fmt.Sprint(config["url"])
	if url == "" {
		return "", fmt.Errorf("url (Discord webhook URL) is required")
	}

	// Discord expects {"content": "message"}
	payload := map[string]string{"content": message}
	body, _ := json.Marshal(payload)

	headers := map[string]string{
		"Content-Type": "application/json",
	}

	resp, err := executor.ExecuteHTTP("POST", url, headers, nil, string(body), 30)
	if err != nil {
		return "", fmt.Errorf("discord notification failed: %v", err)
	}

	return fmt.Sprintf("Discord message sent: %s", resp), nil
}
