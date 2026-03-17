package notify

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const telegramAPI = "https://api.telegram.org/bot"

// BotInfo Telegram Bot 基本信息
type BotInfo struct {
	Name     string `json:"name"`
	Username string `json:"username"`
	PhotoURL string `json:"photo_url"`
}

// GetBotInfo 调用 getMe 获取 bot 信息和头像
func GetBotInfo(botToken string) (*BotInfo, error) {
	// getMe
	resp, err := http.Get(telegramAPI + botToken + "/getMe")
	if err != nil {
		return nil, fmt.Errorf("telegram getMe failed: %w", err)
	}
	defer resp.Body.Close()

	var meResp struct {
		OK     bool `json:"ok"`
		Result struct {
			FirstName string `json:"first_name"`
			Username  string `json:"username"`
			ID        int64  `json:"id"`
		} `json:"result"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meResp); err != nil {
		return nil, fmt.Errorf("telegram getMe decode: %w", err)
	}
	if !meResp.OK {
		return nil, fmt.Errorf("telegram getMe: %s", meResp.Description)
	}

	info := &BotInfo{
		Name:     meResp.Result.FirstName,
		Username: meResp.Result.Username,
	}

	// 尝试获取头像
	photoResp, err := http.Get(telegramAPI + botToken + "/getUserProfilePhotos?user_id=" + fmt.Sprintf("%d", meResp.Result.ID) + "&limit=1")
	if err == nil {
		defer photoResp.Body.Close()
		var photoResult struct {
			OK     bool `json:"ok"`
			Result struct {
				Photos [][]struct {
					FileID string `json:"file_id"`
				} `json:"photos"`
			} `json:"result"`
		}
		if json.NewDecoder(photoResp.Body).Decode(&photoResult) == nil && photoResult.OK && len(photoResult.Result.Photos) > 0 && len(photoResult.Result.Photos[0]) > 0 {
			fileID := photoResult.Result.Photos[0][len(photoResult.Result.Photos[0])-1].FileID
			// getFile
			fileResp, err := http.Get(telegramAPI + botToken + "/getFile?file_id=" + fileID)
			if err == nil {
				defer fileResp.Body.Close()
				var fileResult struct {
					OK     bool `json:"ok"`
					Result struct {
						FilePath string `json:"file_path"`
					} `json:"result"`
				}
				if json.NewDecoder(fileResp.Body).Decode(&fileResult) == nil && fileResult.OK {
					info.PhotoURL = fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", botToken, fileResult.Result.FilePath)
				}
			}
		}
	}

	return info, nil
}

// SendMessage 发送 Telegram 消息（Markdown 格式）
func SendMessage(botToken, chatID, text string) error {
	data := url.Values{
		"chat_id":    {chatID},
		"text":       {text},
		"parse_mode": {"Markdown"},
	}

	resp, err := http.Post(
		telegramAPI+botToken+"/sendMessage",
		"application/x-www-form-urlencoded",
		strings.NewReader(data.Encode()),
	)
	if err != nil {
		return fmt.Errorf("telegram sendMessage: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if json.Unmarshal(body, &result) == nil && !result.OK {
		return fmt.Errorf("telegram sendMessage: %s", result.Description)
	}
	return nil
}

// GenerateVerifyCode 生成 4 位随机数字验证码
func GenerateVerifyCode() string {
	code := make([]byte, 4)
	for i := range code {
		n, _ := rand.Int(rand.Reader, big.NewInt(10))
		code[i] = byte('0' + n.Int64())
	}
	return string(code)
}

// VerifiedUser 验证成功后返回的用户信息
type VerifiedUser struct {
	ChatID      string `json:"chat_id"`
	DisplayName string `json:"display_name"`
	Username    string `json:"username"`
	AvatarURL   string `json:"avatar_url"`
}

// GetUserAvatar 获取 Telegram 用户头像并返回 base64 data URL
// 这样不会泄露 bot token，也不会因 Telegram file URL 过期而失效
func GetUserAvatar(botToken string, userID int64) string {
	photoResp, err := http.Get(fmt.Sprintf("%s%s/getUserProfilePhotos?user_id=%d&limit=1", telegramAPI, botToken, userID))
	if err != nil {
		log.Printf("[telegram] getUserProfilePhotos request failed for user %d: %v", userID, err)
		return ""
	}
	defer photoResp.Body.Close()

	photoBody, _ := io.ReadAll(photoResp.Body)
	var photoResult struct {
		OK     bool `json:"ok"`
		Result struct {
			TotalCount int `json:"total_count"`
			Photos     [][]struct {
				FileID string `json:"file_id"`
				Width  int    `json:"width"`
				Height int    `json:"height"`
			} `json:"photos"`
		} `json:"result"`
	}
	if err := json.Unmarshal(photoBody, &photoResult); err != nil {
		log.Printf("[telegram] getUserProfilePhotos decode failed for user %d: %v", userID, err)
		return ""
	}
	if !photoResult.OK {
		log.Printf("[telegram] getUserProfilePhotos not OK for user %d: %s", userID, string(photoBody))
		return ""
	}
	if photoResult.Result.TotalCount == 0 || len(photoResult.Result.Photos) == 0 || len(photoResult.Result.Photos[0]) == 0 {
		log.Printf("[telegram] user %d has no profile photos (total_count=%d)", userID, photoResult.Result.TotalCount)
		return ""
	}

	log.Printf("[telegram] user %d has %d profile photos, first photo has %d sizes", userID, photoResult.Result.TotalCount, len(photoResult.Result.Photos[0]))

	// 取最小尺寸（index 0）节省存储
	fileID := photoResult.Result.Photos[0][0].FileID
	fileResp, err := http.Get(telegramAPI + botToken + "/getFile?file_id=" + fileID)
	if err != nil {
		log.Printf("[telegram] getFile request failed: %v", err)
		return ""
	}
	defer fileResp.Body.Close()

	var fileResult struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.NewDecoder(fileResp.Body).Decode(&fileResult); err != nil || !fileResult.OK {
		log.Printf("[telegram] getFile failed for fileID %s", fileID)
		return ""
	}

	// 下载图片并转为 base64 data URL
	imgURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", botToken, fileResult.Result.FilePath)
	imgResp, err := http.Get(imgURL)
	if err != nil {
		log.Printf("[telegram] download avatar failed: %v", err)
		return ""
	}
	defer imgResp.Body.Close()

	imgBytes, err := io.ReadAll(imgResp.Body)
	if err != nil || len(imgBytes) == 0 {
		log.Printf("[telegram] read avatar bytes failed: err=%v len=%d", err, len(imgBytes))
		return ""
	}

	contentType := imgResp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}
	log.Printf("[telegram] avatar fetched for user %d: %d bytes, type=%s", userID, len(imgBytes), contentType)
	return fmt.Sprintf("data:%s;base64,%s", contentType, base64.StdEncoding.EncodeToString(imgBytes))
}

// PollForVerifyCode 通过 long polling 等待用户发送验证码
// 匹配成功返回用户完整信息，超时返回错误
func PollForVerifyCode(botToken, code string, timeout time.Duration) (*VerifiedUser, error) {
	deadline := time.Now().Add(timeout)
	offset := 0

	for time.Now().Before(deadline) {
		remaining := time.Until(deadline).Seconds()
		pollTimeout := 30
		if remaining < 30 {
			pollTimeout = int(remaining) + 1
		}

		reqURL := fmt.Sprintf("%s%s/getUpdates?offset=%d&timeout=%d&allowed_updates=[\"message\"]",
			telegramAPI, botToken, offset, pollTimeout)

		resp, err := http.Get(reqURL)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}

		var updateResp struct {
			OK     bool `json:"ok"`
			Result []struct {
				UpdateID int `json:"update_id"`
				Message  struct {
					Text string `json:"text"`
					From struct {
						ID        int64  `json:"id"`
						FirstName string `json:"first_name"`
						LastName  string `json:"last_name"`
						Username  string `json:"username"`
					} `json:"from"`
					Chat struct {
						ID int64 `json:"id"`
					} `json:"chat"`
				} `json:"message"`
			} `json:"result"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&updateResp); err != nil {
			resp.Body.Close()
			time.Sleep(2 * time.Second)
			continue
		}
		resp.Body.Close()

		if !updateResp.OK {
			time.Sleep(2 * time.Second)
			continue
		}

		for _, update := range updateResp.Result {
			offset = update.UpdateID + 1
			msgText := strings.TrimSpace(update.Message.Text)
			// 支持两种格式：纯验证码 "1234" 和 /start 深链 "/start 1234"
			if msgText == code || msgText == "/start "+code {
				// 确认 offset 避免重复处理
				http.Get(fmt.Sprintf("%s%s/getUpdates?offset=%d&timeout=1", telegramAPI, botToken, offset))

				from := update.Message.From
				displayName := from.FirstName
				if from.LastName != "" {
					displayName += " " + from.LastName
				}

				avatarURL := GetUserAvatar(botToken, from.ID)

				return &VerifiedUser{
					ChatID:      fmt.Sprintf("%d", update.Message.Chat.ID),
					DisplayName: displayName,
					Username:    from.Username,
					AvatarURL:   avatarURL,
				}, nil
			}
		}
	}

	return nil, fmt.Errorf("verification timed out")
}
