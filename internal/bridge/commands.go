package bridge

import (
	"fmt"
	"strings"
)

// SlashCommand 表示解析后的 slash 命令
type SlashCommand struct {
	Command string
	Args    string
}

// ParseSlashCommand 解析 Telegram 消息中的 slash 命令。返回 nil 表示不是命令。
func ParseSlashCommand(text string) *SlashCommand {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return nil
	}
	parts := strings.SplitN(text[1:], " ", 2)
	cmd := strings.Split(parts[0], "@")[0]
	cmd = strings.ToLower(cmd)
	args := ""
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}
	switch cmd {
	case "start", "new", "stop", "help", "status", "restart", "resume":
		return &SlashCommand{Command: cmd, Args: args}
	default:
		return nil
	}
}

// FormatWelcome 生成欢迎消息
func FormatWelcome(botName string) string {
	return fmt.Sprintf(
		"👋 你好！我是 %s。\n直接发消息跟我聊天，或使用命令：\n/new — 开始新对话\n/stop — 停止当前任务\n/status — 查看状态\n/help — 查看帮助",
		botName,
	)
}

// FormatHelp 生成帮助消息
func FormatHelp() string {
	return "可用命令：\n" +
		"/new — 开始新对话\n" +
		"/resume — 继续历史会话\n" +
		"/stop — 停止当前任务\n" +
		"/status — 查看当前状态\n" +
		"/restart — 重启服务\n" +
		"/help — 查看帮助"
}
