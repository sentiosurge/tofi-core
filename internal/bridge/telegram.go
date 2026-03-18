package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"tofi-core/internal/storage"
)

// verifyResult 验证成功后返回的用户信息
type verifyResult struct {
	ChatID     string
	SenderName string
	SenderID   string
}

// CallbackHandler 处理 inline keyboard 回调
type CallbackHandler func(connectorID, chatID, senderID, data string)

// TelegramPollingBridge 通过 long polling 接收 Telegram 消息
type TelegramPollingBridge struct {
	connectorID   string
	botToken      string
	botName       string
	onMessage     MessageHandler
	onCallback    CallbackHandler
	sender        *TelegramSender
	cancel        context.CancelFunc
	offset        int64
	verifyMu      sync.Mutex
	verifyWaiters map[string]chan verifyResult // code → channel
}

func NewTelegramPollingBridge(connectorID, botToken, botName string, handler MessageHandler) *TelegramPollingBridge {
	return &TelegramPollingBridge{
		connectorID: connectorID,
		botToken:    botToken,
		botName:     botName,
		onMessage:   handler,
		sender:      &TelegramSender{BotToken: botToken},
	}
}

// SetCallbackHandler 设置 inline keyboard 回调处理器
func (b *TelegramPollingBridge) SetCallbackHandler(handler CallbackHandler) {
	b.onCallback = handler
}

func (b *TelegramPollingBridge) ConnectorID() string         { return b.connectorID }
func (b *TelegramPollingBridge) Type() storage.ConnectorType { return storage.ConnectorTelegram }

func (b *TelegramPollingBridge) SendMessage(chatID, text string) error {
	return b.sender.SendMessage(chatID, text)
}

func (b *TelegramPollingBridge) SendMessageWithButtons(chatID, text string, buttons [][]InlineButton) error {
	return b.sender.SendMessageWithButtons(chatID, text, buttons)
}

func (b *TelegramPollingBridge) SendTyping(chatID string) error {
	return b.sender.SendTyping(chatID)
}

// registerCommands 向 Telegram 注册 slash 命令菜单
func (b *TelegramPollingBridge) registerCommands() {
	commands := []map[string]string{
		{"command": "new", "description": "开始新对话"},
		{"command": "resume", "description": "继续历史会话"},
		{"command": "stop", "description": "停止当前任务"},
		{"command": "status", "description": "查看当前状态"},
		{"command": "restart", "description": "重启服务"},
		{"command": "help", "description": "查看帮助"},
	}
	body, _ := json.Marshal(map[string]any{"commands": commands})
	resp, err := http.Post(
		telegramAPIBase+b.botToken+"/setMyCommands",
		"application/json",
		strings.NewReader(string(body)),
	)
	if err != nil {
		log.Printf("[Bridge:Telegram:%s] Failed to register commands: %v", b.connectorID[:8], err)
		return
	}
	resp.Body.Close()
	log.Printf("[Bridge:Telegram:%s] Registered slash commands menu", b.connectorID[:8])
}

// Start 启动 long polling 循环（阻塞直到 ctx 取消）
func (b *TelegramPollingBridge) Start(ctx context.Context) error {
	ctx, b.cancel = context.WithCancel(ctx)
	log.Printf("[Bridge:Telegram:%s] Starting polling for bot @%s", b.connectorID[:8], b.botName)

	// 注册命令菜单（用户输入 / 时显示）
	b.registerCommands()

	backoff := time.Second
	maxBackoff := 60 * time.Second

	for {
		select {
		case <-ctx.Done():
			log.Printf("[Bridge:Telegram:%s] Stopped", b.connectorID[:8])
			return nil
		default:
		}

		updates, err := b.getUpdates(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("[Bridge:Telegram:%s] Poll error: %v, retry in %v", b.connectorID[:8], err, backoff)
			time.Sleep(backoff)
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		backoff = time.Second

		for _, u := range updates {
			if u.UpdateID >= b.offset {
				b.offset = u.UpdateID + 1
			}

			// Handle callback_query (inline keyboard button clicks)
			if u.CallbackQuery != nil && b.onCallback != nil {
				cq := u.CallbackQuery
				chatID := fmt.Sprintf("%d", cq.Message.Chat.ID)
				senderID := fmt.Sprintf("%d", cq.From.ID)
				go b.onCallback(b.connectorID, chatID, senderID, cq.Data)
				// Answer callback to remove loading state on button
				b.answerCallbackQuery(cq.ID)
				continue
			}

			if u.Message == nil || u.Message.Text == "" {
				continue
			}

			// 检查 /start {code} 验证码消息，优先路由到等待中的 verify waiter
			if strings.HasPrefix(u.Message.Text, "/start ") {
				code := strings.TrimSpace(strings.TrimPrefix(u.Message.Text, "/start "))
				if code != "" {
					b.verifyMu.Lock()
					ch, exists := b.verifyWaiters[code]
					b.verifyMu.Unlock()
					if exists {
						ch <- verifyResult{
							ChatID:     fmt.Sprintf("%d", u.Message.Chat.ID),
							SenderName: u.Message.From.DisplayName(),
							SenderID:   fmt.Sprintf("%d", u.Message.From.ID),
						}
						continue // 不再作为普通消息处理
					}
				}
			}

			msg := IncomingMessage{
				ConnectorID: b.connectorID,
				ChatID:      fmt.Sprintf("%d", u.Message.Chat.ID),
				SenderName:  u.Message.From.DisplayName(),
				SenderID:    fmt.Sprintf("%d", u.Message.From.ID),
				Text:        u.Message.Text,
				Timestamp:   time.Unix(int64(u.Message.Date), 0),
			}

			go b.onMessage(msg)
		}
	}
}

func (b *TelegramPollingBridge) Stop() {
	if b.cancel != nil {
		b.cancel()
	}
}

// answerCallbackQuery 应答 callback_query，移除按钮上的加载动画
func (b *TelegramPollingBridge) answerCallbackQuery(callbackQueryID string) {
	apiURL := fmt.Sprintf("%s%s/answerCallbackQuery?callback_query_id=%s",
		telegramAPIBase, b.botToken, callbackQueryID)
	resp, err := http.Get(apiURL)
	if err != nil {
		return
	}
	resp.Body.Close()
}

// WaitForVerifyCode 注册一个验证码并等待用户发送 /start {code}。
// 当用户发送后返回 chat 信息；超时返回错误。
func (b *TelegramPollingBridge) WaitForVerifyCode(code string, timeout time.Duration) (*verifyResult, error) {
	ch := make(chan verifyResult, 1)

	b.verifyMu.Lock()
	if b.verifyWaiters == nil {
		b.verifyWaiters = make(map[string]chan verifyResult)
	}
	b.verifyWaiters[code] = ch
	b.verifyMu.Unlock()

	defer func() {
		b.verifyMu.Lock()
		delete(b.verifyWaiters, code)
		b.verifyMu.Unlock()
	}()

	select {
	case result := <-ch:
		return &result, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("verification timed out")
	}
}

func (b *TelegramPollingBridge) getUpdates(ctx context.Context) ([]telegramUpdate, error) {
	url := fmt.Sprintf("%s%s/getUpdates?offset=%d&timeout=30&allowed_updates=[\"message\",\"callback_query\"]",
		telegramAPIBase, b.botToken, b.offset)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 35 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		OK     bool             `json:"ok"`
		Result []telegramUpdate `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse getUpdates: %w", err)
	}
	if !result.OK {
		return nil, fmt.Errorf("getUpdates not ok: %s", string(body))
	}

	return result.Result, nil
}

// --- Telegram API types ---

type telegramUpdate struct {
	UpdateID      int64                  `json:"update_id"`
	Message       *telegramMessage       `json:"message,omitempty"`
	CallbackQuery *telegramCallbackQuery `json:"callback_query,omitempty"`
}

type telegramCallbackQuery struct {
	ID      string           `json:"id"`
	From    telegramUser     `json:"from"`
	Message *telegramMessage `json:"message,omitempty"`
	Data    string           `json:"data"`
}

type telegramMessage struct {
	MessageID int64        `json:"message_id"`
	From      telegramUser `json:"from"`
	Chat      telegramChat `json:"chat"`
	Date      int          `json:"date"`
	Text      string       `json:"text"`
}

type telegramUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

func (u telegramUser) DisplayName() string {
	name := u.FirstName
	if u.LastName != "" {
		name += " " + u.LastName
	}
	return name
}

type telegramChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}
