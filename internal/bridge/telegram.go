package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"tofi-core/internal/storage"
)

// TelegramPollingBridge 通过 long polling 接收 Telegram 消息
type TelegramPollingBridge struct {
	connectorID string
	botToken    string
	botName     string
	onMessage   MessageHandler
	sender      *TelegramSender
	cancel      context.CancelFunc
	offset      int64
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

func (b *TelegramPollingBridge) ConnectorID() string         { return b.connectorID }
func (b *TelegramPollingBridge) Type() storage.ConnectorType { return storage.ConnectorTelegram }

func (b *TelegramPollingBridge) SendMessage(chatID, text string) error {
	return b.sender.SendMessage(chatID, text)
}

func (b *TelegramPollingBridge) SendTyping(chatID string) error {
	return b.sender.SendTyping(chatID)
}

// Start 启动 long polling 循环（阻塞直到 ctx 取消）
func (b *TelegramPollingBridge) Start(ctx context.Context) error {
	ctx, b.cancel = context.WithCancel(ctx)
	log.Printf("[Bridge:Telegram:%s] Starting polling for bot @%s", b.connectorID[:8], b.botName)

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

			if u.Message == nil || u.Message.Text == "" {
				continue
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

func (b *TelegramPollingBridge) getUpdates(ctx context.Context) ([]telegramUpdate, error) {
	url := fmt.Sprintf("%s%s/getUpdates?offset=%d&timeout=30&allowed_updates=[\"message\"]",
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
	UpdateID int64            `json:"update_id"`
	Message  *telegramMessage `json:"message,omitempty"`
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
