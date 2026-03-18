package bridge

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	telegramAPIBase   = "https://api.telegram.org/bot"
	telegramMaxMsgLen = 4096
	telegramTypingTTL = 4 * time.Second
)

// TelegramSender 封装 Telegram Bot API 消息发送
type TelegramSender struct {
	BotToken string
}

// SendMessage 发送消息（不返回 message_id）。超过 4096 字符自动分片。
func (ts *TelegramSender) SendMessage(chatID, text string) error {
	if text == "" {
		return nil
	}
	chunks := splitMessage(text, telegramMaxMsgLen)
	for _, chunk := range chunks {
		if err := ts.sendSingle(chatID, chunk); err != nil {
			return err
		}
	}
	return nil
}

// SendMessageReturnID 发送消息并返回 message_id（用于后续 editMessage）。
func (ts *TelegramSender) SendMessageReturnID(chatID, text string) (int64, error) {
	if text == "" {
		return 0, nil
	}
	msgID, err := ts.sendRawReturnID(chatID, text, "")
	if err != nil {
		return 0, err
	}
	return msgID, nil
}

// EditMessage 编辑已有消息的文本内容。
func (ts *TelegramSender) EditMessage(chatID string, messageID int64, text string) error {
	if text == "" {
		return nil
	}
	// 先尝试 Markdown
	err := ts.editRaw(chatID, messageID, text, "Markdown")
	if err != nil && strings.Contains(err.Error(), "400") {
		// Markdown 解析失败，fallback 到纯文本
		return ts.editRaw(chatID, messageID, text, "")
	}
	return err
}

// editRaw 底层 Telegram editMessageText API 调用
func (ts *TelegramSender) editRaw(chatID string, messageID int64, text, parseMode string) error {
	params := url.Values{
		"chat_id":    {chatID},
		"message_id": {fmt.Sprintf("%d", messageID)},
		"text":       {text},
	}
	if parseMode != "" {
		params.Set("parse_mode", parseMode)
	}
	apiURL := telegramAPIBase + ts.BotToken + "/editMessageText"
	resp, err := http.PostForm(apiURL, params)
	if err != nil {
		return fmt.Errorf("telegram editMessageText failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram editMessageText %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// sendSingle 发送单条消息，先 Markdown 后 fallback 纯文本，带重试
func (ts *TelegramSender) sendSingle(chatID, text string) error {
	err := ts.sendWithRetry(chatID, text, "Markdown")
	if err != nil && strings.Contains(err.Error(), "400") {
		return ts.sendWithRetry(chatID, text, "")
	}
	return err
}

// sendWithRetry 带重试的发送（3 次，间隔 1/2/4 秒）
func (ts *TelegramSender) sendWithRetry(chatID, text, parseMode string) error {
	var lastErr error
	for i := 0; i < 3; i++ {
		_, lastErr = ts.sendRawReturnID(chatID, text, parseMode)
		if lastErr == nil {
			return nil
		}
		if strings.Contains(lastErr.Error(), "400") {
			return lastErr
		}
		time.Sleep(time.Duration(1<<uint(i)) * time.Second)
	}
	return lastErr
}

// sendRawReturnID 底层 Telegram sendMessage API 调用，返回 message_id
func (ts *TelegramSender) sendRawReturnID(chatID, text, parseMode string) (int64, error) {
	params := url.Values{
		"chat_id": {chatID},
		"text":    {text},
	}
	if parseMode != "" {
		params.Set("parse_mode", parseMode)
	}
	apiURL := telegramAPIBase + ts.BotToken + "/sendMessage"
	resp, err := http.PostForm(apiURL, params)
	if err != nil {
		return 0, fmt.Errorf("telegram sendMessage failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("telegram sendMessage %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, nil // 消息发出去了，只是解析 ID 失败
	}
	return result.Result.MessageID, nil
}

// SendMessageWithButtons 发送带 inline keyboard 的消息
func (ts *TelegramSender) SendMessageWithButtons(chatID, text string, buttons [][]InlineButton) error {
	if text == "" {
		return nil
	}

	// Build inline_keyboard structure for Telegram API
	keyboard := make([][]map[string]string, len(buttons))
	for i, row := range buttons {
		keyboard[i] = make([]map[string]string, len(row))
		for j, btn := range row {
			keyboard[i][j] = map[string]string{
				"text":          btn.Label,
				"callback_data": btn.CallbackData,
			}
		}
	}

	payload := map[string]any{
		"chat_id":      chatID,
		"text":         text,
		"reply_markup": map[string]any{"inline_keyboard": keyboard},
	}
	body, _ := json.Marshal(payload)

	apiURL := telegramAPIBase + ts.BotToken + "/sendMessage"
	resp, err := http.Post(apiURL, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("telegram sendMessage with buttons failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram sendMessage with buttons %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// SendTyping 发送"正在输入"状态
func (ts *TelegramSender) SendTyping(chatID string) error {
	params := url.Values{
		"chat_id": {chatID},
		"action":  {"typing"},
	}
	apiURL := telegramAPIBase + ts.BotToken + "/sendChatAction"
	resp, err := http.PostForm(apiURL, params)
	if err != nil {
		return nil
	}
	resp.Body.Close()
	return nil
}

// splitMessage 将超长文本分片，尽量在换行符处断开
func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}
	var chunks []string
	for len(text) > 0 {
		if len(text) <= maxLen {
			chunks = append(chunks, text)
			break
		}
		cutAt := maxLen
		if idx := strings.LastIndex(text[:maxLen], "\n"); idx > maxLen/2 {
			cutAt = idx + 1
		}
		chunks = append(chunks, text[:cutAt])
		text = text[cutAt:]
	}
	return chunks
}
