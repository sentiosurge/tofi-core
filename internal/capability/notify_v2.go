package capability

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"tofi-core/internal/mcp"
	"tofi-core/internal/notify"
	"tofi-core/internal/provider"
	"tofi-core/internal/storage"
)

// ConnectorNotifyDeps 依赖注入：tofi_notify 工具需要的 DB 操作
type ConnectorNotifyDeps struct {
	ListConnectorsByApp  func(userID, appID string) ([]*storage.Connector, error)
	ListConnectors       func(userID string) ([]*storage.Connector, error)
	ListConnectorReceivers func(connectorID string) ([]*storage.ConnectorReceiver, error)
}

// BuildConnectorNotifyTool 基于 v2 connector 系统构建 tofi_notify 工具
// connectors: 该 App（或全局）可用的所有 connectors
func BuildConnectorNotifyTool(connectors []*storage.Connector, deps ConnectorNotifyDeps) mcp.ExtraBuiltinTool {
	// 构建可用渠道描述
	var channelDescs []string
	var channelNames []string
	for _, c := range connectors {
		if !c.Enabled {
			continue
		}
		label := string(c.Type)
		if c.Name != "" {
			label += " (" + c.Name + ")"
		}
		channelDescs = append(channelDescs, label)
		channelNames = append(channelNames, c.ID)
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
				// TODO: 其他渠道实现
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

func sendViaTelegram(c *storage.Connector, deps ConnectorNotifyDeps, toNames []string, sendAll bool, message string) ([]string, int) {
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

		if err := notify.SendMessage(tgCfg.BotToken, meta.ChatID, message); err != nil {
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

// InjectConnectorNotifyTool 向 extraTools 注入 tofi_notify（如果有可用 connectors）
func InjectConnectorNotifyTool(
	extraTools []mcp.ExtraBuiltinTool,
	userID, appID string,
	deps ConnectorNotifyDeps,
) []mcp.ExtraBuiltinTool {
	var connectors []*storage.Connector
	var err error

	if appID != "" {
		connectors, err = deps.ListConnectorsByApp(userID, appID)
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

	return append(filtered, BuildConnectorNotifyTool(enabled, deps))
}

// marshalJSON is a helper (unused import guard)
func init() {
	_ = json.Marshal
}
