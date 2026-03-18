package bridge

import (
	"context"
	"time"

	"tofi-core/internal/storage"
)

// InlineButton 表示一个 inline keyboard 按钮
type InlineButton struct {
	Label        string // 按钮文字
	CallbackData string // 点击后回调的数据
}

// ChatBridge — 通用双向通信桥接接口。
// Telegram/Discord/Slack/Email 各自实现。
type ChatBridge interface {
	Start(ctx context.Context) error
	Stop()
	SendMessage(chatID, text string) error
	SendMessageWithButtons(chatID, text string, buttons [][]InlineButton) error
	SendTyping(chatID string) error
	ConnectorID() string
	Type() storage.ConnectorType
}

// IncomingMessage 统一入站消息格式
type IncomingMessage struct {
	ConnectorID string
	ChatID      string
	SenderName  string
	SenderID    string
	Text        string
	Timestamp   time.Time
}

// MessageHandler 处理入站消息的回调
type MessageHandler func(msg IncomingMessage)

// ExecuteOptions 允许调用方自定义 agent 回调行为。
// nil 表示使用默认的 SSE/onEvent 回调。
type ExecuteOptions struct {
	OnStreamChunk    func(sessionID, delta string)
	OnToolCall       func(toolName, input, output string, durationMs int64)
	OnContextCompact func(summary string, originalTokens, compactedTokens int)
	OnStepStart      func(toolName, args string)
	OnStepDone       func(toolName, result string, durationMs int64)
}
