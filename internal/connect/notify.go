package connect

import (
	"fmt"
	"log"
	"strings"

	"tofi-core/internal/mcp"
	"tofi-core/internal/provider"
	"tofi-core/internal/storage"
)

// NotifyDeps 依赖注入：tofi_notify 工具需要的 DB 操作
type NotifyDeps struct {
	ListConnectorsForApp   func(userID, appID string) ([]*storage.Connector, error)
	ListConnectors         func(userID string) ([]*storage.Connector, error)
	ListConnectorReceivers func(connectorID string) ([]*storage.ConnectorReceiver, error)
}

// BuildNotifyTool 基于 connector 系统构建 tofi_notify 工具
func BuildNotifyTool(connectors []*storage.Connector, deps NotifyDeps) mcp.ExtraBuiltinTool {
	// 构建可用渠道描述
	var channelDescs []string
	for _, c := range connectors {
		if !c.Enabled {
			continue
		}
		label := string(c.Type)
		if c.Name != "" {
			label += " (" + c.Name + ")"
		}
		channelDescs = append(channelDescs, label)
	}

	if len(channelDescs) == 0 {
		channelDescs = []string{"(no connectors configured)"}
	}

	return mcp.ExtraBuiltinTool{
		Schema: provider.Tool{
			Name: "tofi_notify",
			Description: fmt.Sprintf(
				"Send a notification to specified receivers through connected channels. Available connectors: %s. "+
					"Use this to notify users of results, completions, or alerts.",
				strings.Join(channelDescs, ", "),
			),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"to": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "List of receiver display names to notify. Use \"all\" to send to all receivers of the connector.",
					},
					"message": map[string]any{
						"type":        "string",
						"description": "The notification message to send (supports Markdown for Telegram)",
					},
					"channel": map[string]any{
						"type":        "string",
						"description": "Optional: connector type to use (telegram, slack_webhook, etc.). If omitted, sends through all available connectors.",
					},
				},
				"required": []string{"message"},
			},
		},
		Handler: func(args map[string]any) (string, error) {
			message, _ := args["message"].(string)
			if message == "" {
				return "Error: message is required", nil
			}

			channel, _ := args["channel"].(string)

			// Parse "to" list
			var toNames []string
			if toRaw, ok := args["to"]; ok {
				if toArr, ok := toRaw.([]any); ok {
					for _, v := range toArr {
						if s, ok := v.(string); ok {
							toNames = append(toNames, s)
						}
					}
				}
			}
			sendAll := len(toNames) == 0 || (len(toNames) == 1 && strings.ToLower(toNames[0]) == "all")

			var results []string
			sent := 0

			for _, c := range connectors {
				if !c.Enabled {
					continue
				}
				// 如果指定了 channel type，过滤
				if channel != "" && string(c.Type) != channel {
					continue
				}

				switch c.Type {
				case storage.ConnectorTelegram:
					r, n := sendViaTelegram(c, deps, toNames, sendAll, message)
					results = append(results, r...)
					sent += n
				case storage.ConnectorDiscordWebhook:
					r, n := sendViaWebhook(c, "Discord", SendDiscordWebhook, message)
					results = append(results, r...)
					sent += n
				case storage.ConnectorSlackWebhook:
					r, n := sendViaWebhook(c, "Slack", SendSlackWebhook, message)
					results = append(results, r...)
					sent += n
				default:
					results = append(results, fmt.Sprintf("Channel %s not yet supported", c.Type))
				}
			}

			if sent == 0 {
				return "No messages sent. Check that connectors are configured and receivers are verified.", nil
			}

			return fmt.Sprintf("Sent %d notification(s).\n%s", sent, strings.Join(results, "\n")), nil
		},
	}
}

func sendViaTelegram(c *storage.Connector, deps NotifyDeps, toNames []string, sendAll bool, message string) ([]string, int) {
	tgCfg, err := c.TelegramConfig()
	if err != nil || tgCfg.BotToken == "" {
		return []string{"Telegram: invalid config"}, 0
	}

	receivers, err := deps.ListConnectorReceivers(c.ID)
	if err != nil {
		return []string{"Telegram: failed to list receivers"}, 0
	}

	var results []string
	sent := 0

	for _, rv := range receivers {
		if !sendAll {
			matched := false
			for _, name := range toNames {
				if strings.EqualFold(rv.DisplayName, name) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}

		meta, err := rv.TelegramMeta()
		if err != nil || meta.ChatID == "" {
			continue
		}

		if err := SendMessage(tgCfg.BotToken, meta.ChatID, message); err != nil {
			log.Printf("[tofi_notify] telegram send failed to %s: %v", rv.DisplayName, err)
			results = append(results, fmt.Sprintf("Failed to notify %s: %v", rv.DisplayName, err))
		} else {
			sent++
			results = append(results, fmt.Sprintf("Notified %s via Telegram", rv.DisplayName))
		}
	}

	// Report unmatched names
	if !sendAll {
		for _, name := range toNames {
			found := false
			for _, rv := range receivers {
				if strings.EqualFold(rv.DisplayName, name) {
					found = true
					break
				}
			}
			if !found {
				results = append(results, fmt.Sprintf("Receiver '%s' not found in connector %s", name, c.ID[:8]))
			}
		}
	}

	return results, sent
}

// sendViaWebhook Webhook 类型通用发送（Discord Webhook / Slack Webhook）
// Webhook connector 没有 receivers 概念，直接发到 webhook URL
func sendViaWebhook(c *storage.Connector, channelName string, sendFn func(string, string) error, message string) ([]string, int) {
	cfg, err := c.WebhookConfig()
	if err != nil || cfg.WebhookURL == "" {
		return []string{fmt.Sprintf("%s: invalid config", channelName)}, 0
	}

	if err := sendFn(cfg.WebhookURL, message); err != nil {
		log.Printf("[tofi_notify] %s webhook send failed: %v", channelName, err)
		return []string{fmt.Sprintf("Failed to notify via %s: %v", channelName, err)}, 0
	}

	return []string{fmt.Sprintf("Notified via %s webhook", channelName)}, 1
}

// SendNotification sends a message through all configured connectors for an app.
// This is called by the runtime (e.g., app scheduler) after an app run completes,
// NOT by the AI. The AI only produces the content; delivery is handled by the platform.
func SendNotification(userID, appID, message string, deps NotifyDeps) (int, error) {
	if message == "" {
		return 0, nil
	}

	var connectors []*storage.Connector
	var err error

	if appID != "" {
		// Scope-based: returns both global:* and app:{appID} connectors
		connectors, err = deps.ListConnectorsForApp(userID, appID)
	} else {
		connectors, err = deps.ListConnectors(userID)
	}

	if err != nil || len(connectors) == 0 {
		return 0, nil // no connectors configured — not an error
	}

	sent := 0
	for _, c := range connectors {
		if !c.Enabled {
			continue
		}
		switch c.Type {
		case storage.ConnectorTelegram:
			_, n := sendViaTelegram(c, deps, nil, true, message)
			sent += n
		case storage.ConnectorDiscordWebhook:
			_, n := sendViaWebhook(c, "Discord", SendDiscordWebhook, message)
			sent += n
		case storage.ConnectorSlackWebhook:
			_, n := sendViaWebhook(c, "Slack", SendSlackWebhook, message)
			sent += n
		}
	}

	if sent > 0 {
		log.Printf("[notify] sent %d notification(s) for app %s", sent, appID)
	}
	return sent, nil
}

// InjectNotifyTool 向 extraTools 注入 tofi_notify（如果有可用 connectors）
func InjectNotifyTool(
	extraTools []mcp.ExtraBuiltinTool,
	userID, appID string,
	deps NotifyDeps,
) []mcp.ExtraBuiltinTool {
	var connectors []*storage.Connector
	var err error

	if appID != "" {
		connectors, err = deps.ListConnectorsForApp(userID, appID)
	} else {
		connectors, err = deps.ListConnectors(userID)
	}

	if err != nil || len(connectors) == 0 {
		return extraTools
	}

	// 过滤掉 disabled
	var enabled []*storage.Connector
	for _, c := range connectors {
		if c.Enabled {
			enabled = append(enabled, c)
		}
	}

	if len(enabled) == 0 {
		return extraTools
	}

	// 移除旧的 send_notification 工具（如果存在）
	var filtered []mcp.ExtraBuiltinTool
	for _, t := range extraTools {
		if t.Schema.Name != "send_notification" {
			filtered = append(filtered, t)
		}
	}

	return append(filtered, BuildNotifyTool(enabled, deps))
}
