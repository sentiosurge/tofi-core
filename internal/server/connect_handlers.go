package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
	"tofi-core/internal/connect"
	"tofi-core/internal/storage"
)

// pendingVerifiesMu 保护所有 pending verify maps
var pendingVerifiesMu sync.Mutex

// ===================== Connector CRUD =====================

// handleListConnectors GET /api/v1/connectors
func (s *Server) handleListConnectors(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	connectors, err := s.db.ListConnectors(userID)
	if err != nil {
		http.Error(w, `{"error":"failed to list connectors"}`, http.StatusInternalServerError)
		return
	}

	// 为每个 connector 附带 receiver 数量
	type connectorWithCount struct {
		*storage.Connector
		ReceiverCount int    `json:"receiver_count"`
		CanReceive    bool   `json:"can_receive"`
		AppName       string `json:"app_name,omitempty"`
	}

	var results []connectorWithCount
	for _, c := range connectors {
		receivers, _ := s.db.ListConnectorReceivers(c.ID)
		item := connectorWithCount{
			Connector:     c,
			ReceiverCount: len(receivers),
			CanReceive:    c.Type.CanReceive(),
		}
		// 查 app name
		if c.AppID != "" {
			app, _ := s.db.GetApp(c.AppID)
			if app != nil {
				item.AppName = app.Name
			}
		}
		results = append(results, item)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

// handleCreateConnector POST /api/v1/connectors
func (s *Server) handleCreateConnector(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	var req struct {
		Type   string `json:"type"`
		Name   string `json:"name"`
		AppID  string `json:"app_id"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	ctype := storage.ConnectorType(req.Type)
	switch ctype {
	case storage.ConnectorTelegram, storage.ConnectorSlackWebhook, storage.ConnectorSlackApp,
		storage.ConnectorDiscordWebhook, storage.ConnectorDiscordBot, storage.ConnectorEmail:
		// valid
	default:
		http.Error(w, `{"error":"unsupported connector type"}`, http.StatusBadRequest)
		return
	}

	// 验证 connector config
	switch ctype {
	case storage.ConnectorTelegram:
		var tgCfg storage.TelegramConnectorConfig
		if err := json.Unmarshal(req.Config, &tgCfg); err != nil || tgCfg.BotToken == "" {
			http.Error(w, `{"error":"bot_token required for telegram"}`, http.StatusBadRequest)
			return
		}
		info, err := connect.GetBotInfo(tgCfg.BotToken)
		if err != nil {
			http.Error(w, `{"error":"invalid bot token: `+err.Error()+`"}`, http.StatusBadRequest)
			return
		}
		tgCfg.BotName = info.Name
		tgCfg.BotUsername = info.Username
		tgCfg.BotPhoto = info.PhotoURL
		cfgJSON, _ := json.Marshal(tgCfg)
		req.Config = cfgJSON

	case storage.ConnectorDiscordWebhook:
		var whCfg storage.WebhookConnectorConfig
		if err := json.Unmarshal(req.Config, &whCfg); err != nil || whCfg.WebhookURL == "" {
			http.Error(w, `{"error":"webhook_url required for discord_webhook"}`, http.StatusBadRequest)
			return
		}
		if err := connect.ValidateDiscordWebhook(whCfg.WebhookURL); err != nil {
			http.Error(w, `{"error":"invalid discord webhook: `+err.Error()+`"}`, http.StatusBadRequest)
			return
		}

	case storage.ConnectorSlackWebhook:
		var whCfg storage.WebhookConnectorConfig
		if err := json.Unmarshal(req.Config, &whCfg); err != nil || whCfg.WebhookURL == "" {
			http.Error(w, `{"error":"webhook_url required for slack_webhook"}`, http.StatusBadRequest)
			return
		}
		if err := connect.ValidateSlackWebhook(whCfg.WebhookURL); err != nil {
			http.Error(w, `{"error":"invalid slack webhook: `+err.Error()+`"}`, http.StatusBadRequest)
			return
		}
	}

	configStr := "{}"
	if req.Config != nil {
		configStr = string(req.Config)
	}

	connector, err := s.db.CreateConnector(userID, req.AppID, ctype, req.Name, configStr)
	if err != nil {
		http.Error(w, `{"error":"failed to create connector"}`, http.StatusInternalServerError)
		return
	}

	// 如果指定了 app_id，自动创建 app_connector 关联
	if req.AppID != "" {
		s.db.LinkAppConnector(req.AppID, connector.ID)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(connector)
}

// handleGetConnector GET /api/v1/connectors/{id}
func (s *Server) handleGetConnector(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	connID := r.PathValue("id")

	conn, err := s.db.GetConnector(connID, userID)
	if err != nil {
		http.Error(w, `{"error":"connector not found"}`, http.StatusNotFound)
		return
	}

	receivers, _ := s.db.ListConnectorReceivers(connID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"connector":  conn,
		"receivers":  receivers,
		"can_receive": conn.Type.CanReceive(),
	})
}

// handleDeleteConnector DELETE /api/v1/connectors/{id}
func (s *Server) handleDeleteConnector(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	connID := r.PathValue("id")

	conn, err := s.db.GetConnector(connID, userID)
	if err != nil {
		http.Error(w, `{"error":"connector not found"}`, http.StatusNotFound)
		return
	}

	// 对于 telegram，给所有 receiver 发通知
	if conn.Type == storage.ConnectorTelegram {
		tgCfg, _ := conn.TelegramConfig()
		if tgCfg != nil {
			receivers, _ := s.db.ListConnectorReceivers(connID)
			for _, rv := range receivers {
				meta, _ := rv.TelegramMeta()
				if meta != nil {
					go connect.SendMessage(tgCfg.BotToken, meta.ChatID, "🔕 This Tofi connector has been removed.")
				}
			}
		}
	}

	// 取消 pending 验证
	cancelPendingVerify(connID)

	if s.bridgeManager != nil {
		s.bridgeManager.StopBridge(connID)
	}
	s.db.DeleteBridgeSessionsByConnector(connID)

	if err := s.db.DeleteConnector(connID, userID); err != nil {
		http.Error(w, `{"error":"failed to delete"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleToggleConnector PUT /api/v1/connectors/{id}/toggle
func (s *Server) handleToggleConnector(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	connID := r.PathValue("id")

	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}

	if err := s.db.SetConnectorEnabled(connID, userID, req.Enabled); err != nil {
		http.Error(w, `{"error":"failed to toggle"}`, http.StatusInternalServerError)
		return
	}

	// Notify Bridge Manager
	if s.bridgeManager != nil {
		if req.Enabled {
			connector, _ := s.db.GetConnector(connID, userID)
			if connector != nil {
				s.bridgeManager.StartBridge(connector)
			}
		} else {
			s.bridgeManager.StopBridge(connID)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// ===================== Receiver Management =====================

// handleConnectorVerify POST /api/v1/connectors/{id}/verify
// Telegram: 生成验证码，polling 等待
func (s *Server) handleConnectorVerify(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	connID := r.PathValue("id")

	conn, err := s.db.GetConnector(connID, userID)
	if err != nil {
		http.Error(w, `{"error":"connector not found"}`, http.StatusNotFound)
		return
	}

	if conn.Type != storage.ConnectorTelegram {
		http.Error(w, `{"error":"verify only supported for telegram"}`, http.StatusBadRequest)
		return
	}

	tgCfg, err := conn.TelegramConfig()
	if err != nil || tgCfg.BotToken == "" {
		http.Error(w, `{"error":"telegram config invalid"}`, http.StatusBadRequest)
		return
	}

	// 取消之前的 pending 验证
	cancelPendingVerify(connID)

	done := make(chan *connect.VerifiedUser, 1)

	// 生成唯一验证码
	pendingVerifiesMu.Lock()
	var code string
	for {
		code = connect.GenerateVerifyCode()
		unique := true
		for _, p := range pendingConnectorVerifies {
			if p.BotToken == tgCfg.BotToken && p.Code == code {
				unique = false
				break
			}
		}
		if unique {
			break
		}
	}
	pendingConnectorVerifies[connID] = &PendingConnectorVerify{
		Code:        code,
		BotToken:    tgCfg.BotToken,
		ConnectorID: connID,
		Done:        done,
	}
	pendingVerifiesMu.Unlock()

	// 后台 polling：如果 bridge 已在运行，使用 bridge-based 验证避免 offset 冲突
	if s.bridgeManager != nil && s.bridgeManager.IsRunning(connID) {
		go func() {
			defer cancelPendingVerify(connID)

			result, err := s.bridgeManager.WaitForVerifyCode(connID, code, 5*time.Minute)
			if err != nil {
				log.Printf("[connector] bridge verify failed for connector %s: %v", connID, err)
				return
			}

			// 获取用户头像（需要 int64 userID）
			var senderIDInt int64
			fmt.Sscanf(result.SenderID, "%d", &senderIDInt)
			avatarURL := connect.GetUserAvatar(tgCfg.BotToken, senderIDInt)

			// 保存 receiver（bridge 验证不返回 username，留空）
			meta := storage.TelegramReceiverMeta{ChatID: result.ChatID, Username: ""}
			metaJSON, _ := json.Marshal(meta)
			_, err = s.db.AddConnectorReceiver(
				connID, result.ChatID, result.SenderName, avatarURL, string(metaJSON),
			)
			if err != nil {
				log.Printf("[connector] failed to save receiver for connector %s: %v", connID, err)
				return
			}

			log.Printf("[connector] connector %s verified receiver via bridge: %s", connID, result.SenderName)

			verified := &connect.VerifiedUser{
				ChatID:      result.ChatID,
				DisplayName: result.SenderName,
				AvatarURL:   avatarURL,
			}
			select {
			case done <- verified:
			default:
			}
		}()
	} else {
		go func() {
			defer cancelPendingVerify(connID)

			verified, err := connect.PollForVerifyCode(tgCfg.BotToken, code, 5*time.Minute)
			if err != nil {
				log.Printf("[connector] verify polling failed for connector %s: %v", connID, err)
				return
			}

			// 保存 receiver
			meta := storage.TelegramReceiverMeta{ChatID: verified.ChatID, Username: verified.Username}
			metaJSON, _ := json.Marshal(meta)
			_, err = s.db.AddConnectorReceiver(
				connID, verified.ChatID, verified.DisplayName, verified.AvatarURL, string(metaJSON),
			)
			if err != nil {
				log.Printf("[connector] failed to save receiver for connector %s: %v", connID, err)
				return
			}

			log.Printf("[connector] connector %s verified receiver: %s (@%s)", connID, verified.DisplayName, verified.Username)

			if s.bridgeManager != nil {
				connector, _ := s.db.GetConnector(connID, userID)
				if connector != nil {
					s.bridgeManager.StartBridge(connector)
				}
			}

			select {
			case done <- verified:
			default:
			}
		}()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"code":         code,
		"bot_name":     tgCfg.BotName,
		"bot_username": tgCfg.BotUsername,
		"connector_id": connID,
	})
}

// handleConnectorReceivers GET /api/v1/connectors/{id}/receivers
func (s *Server) handleConnectorReceivers(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	connID := r.PathValue("id")

	// 验证 connector 归属
	if _, err := s.db.GetConnector(connID, userID); err != nil {
		http.Error(w, `{"error":"connector not found"}`, http.StatusNotFound)
		return
	}

	receivers, err := s.db.ListConnectorReceivers(connID)
	if err != nil {
		http.Error(w, `{"error":"failed to list receivers"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(receivers)
}

// handleDeleteConnectorReceiver DELETE /api/v1/connectors/{id}/receivers/{rid}
func (s *Server) handleDeleteConnectorReceiver(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	connID := r.PathValue("id")
	ridStr := r.PathValue("rid")
	rid, err := strconv.ParseInt(ridStr, 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid receiver id"}`, http.StatusBadRequest)
		return
	}

	conn, err := s.db.GetConnector(connID, userID)
	if err != nil {
		http.Error(w, `{"error":"connector not found"}`, http.StatusNotFound)
		return
	}

	// Telegram: 发删除通知
	if conn.Type == storage.ConnectorTelegram {
		receiver, _ := s.db.GetConnectorReceiver(rid)
		if receiver != nil {
			tgCfg, _ := conn.TelegramConfig()
			meta, _ := receiver.TelegramMeta()
			if tgCfg != nil && meta != nil {
				go connect.SendMessage(tgCfg.BotToken, meta.ChatID, "🔕 You have been removed from Tofi notifications.")
			}
		}
	}

	if err := s.db.DeleteConnectorReceiver(rid); err != nil {
		http.Error(w, `{"error":"failed to delete"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleConnectorTest POST /api/v1/connectors/{id}/test
func (s *Server) handleConnectorTest(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	connID := r.PathValue("id")

	conn, err := s.db.GetConnector(connID, userID)
	if err != nil {
		http.Error(w, `{"error":"connector not found"}`, http.StatusNotFound)
		return
	}

	var req struct {
		ReceiverID *int64 `json:"receiver_id"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	msg := "✅ Tofi Connected! You'll receive task notifications here."

	switch conn.Type {
	case storage.ConnectorDiscordWebhook, storage.ConnectorSlackWebhook:
		whCfg, err := conn.WebhookConfig()
		if err != nil || whCfg.WebhookURL == "" {
			http.Error(w, `{"error":"webhook config invalid"}`, http.StatusBadRequest)
			return
		}
		var sendErr error
		if conn.Type == storage.ConnectorDiscordWebhook {
			sendErr = connect.SendDiscordWebhook(whCfg.WebhookURL, msg)
		} else {
			sendErr = connect.SendSlackWebhook(whCfg.WebhookURL, msg)
		}
		if sendErr != nil {
			log.Printf("[connect] webhook test failed: %v", sendErr)
			http.Error(w, `{"error":"test failed: `+sendErr.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return

	case storage.ConnectorTelegram:
		// Telegram: send to specific receiver or all receivers
	default:
		http.Error(w, `{"error":"test not supported for this connector type"}`, http.StatusBadRequest)
		return
	}

	tgCfg, err := conn.TelegramConfig()
	if err != nil || tgCfg.BotToken == "" {
		http.Error(w, `{"error":"telegram config invalid"}`, http.StatusBadRequest)
		return
	}

	tgMsg := "✅ *Tofi Connected!*\n\nYou'll receive task notifications here."

	if req.ReceiverID != nil {
		receiver, err := s.db.GetConnectorReceiver(*req.ReceiverID)
		if err != nil {
			http.Error(w, `{"error":"receiver not found"}`, http.StatusNotFound)
			return
		}
		meta, _ := receiver.TelegramMeta()
		if meta == nil {
			http.Error(w, `{"error":"invalid receiver metadata"}`, http.StatusInternalServerError)
			return
		}
		if err := connect.SendMessage(tgCfg.BotToken, meta.ChatID, tgMsg); err != nil {
			log.Printf("[connect] test message failed: %v", err)
		}
	} else {
		receivers, _ := s.db.ListConnectorReceivers(connID)
		if len(receivers) == 0 {
			http.Error(w, `{"error":"no receivers connected"}`, http.StatusBadRequest)
			return
		}
		for _, rv := range receivers {
			meta, _ := rv.TelegramMeta()
			if meta != nil {
				if err := connect.SendMessage(tgCfg.BotToken, meta.ChatID, msg); err != nil {
					log.Printf("[connector] test message failed for %s: %v", meta.ChatID, err)
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// ===================== App-Connector Linking =====================

// handleLinkAppConnector POST /api/v1/apps/{id}/connectors
func (s *Server) handleLinkAppConnector(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	appID := r.PathValue("id")

	// 验证 app 存在
	if _, err := s.db.GetApp(appID); err != nil {
		http.Error(w, `{"error":"app not found"}`, http.StatusNotFound)
		return
	}

	var req struct {
		ConnectorID string `json:"connector_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ConnectorID == "" {
		http.Error(w, `{"error":"connector_id required"}`, http.StatusBadRequest)
		return
	}

	// 验证 connector 存在且属于该用户
	if _, err := s.db.GetConnector(req.ConnectorID, userID); err != nil {
		http.Error(w, `{"error":"connector not found"}`, http.StatusNotFound)
		return
	}

	if err := s.db.LinkAppConnector(appID, req.ConnectorID); err != nil {
		http.Error(w, `{"error":"failed to link"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleUnlinkAppConnector DELETE /api/v1/apps/{id}/connectors/{cid}
func (s *Server) handleUnlinkAppConnector(w http.ResponseWriter, r *http.Request) {
	_ = r.Context().Value(UserContextKey).(string)
	appID := r.PathValue("id")
	connID := r.PathValue("cid")

	if _, err := s.db.GetApp(appID); err != nil {
		http.Error(w, `{"error":"app not found"}`, http.StatusNotFound)
		return
	}

	if err := s.db.UnlinkAppConnector(appID, connID); err != nil {
		http.Error(w, `{"error":"failed to unlink"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleListAppConnectors GET /api/v1/apps/{id}/connectors
func (s *Server) handleListAppConnectors(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	appID := r.PathValue("id")

	connectors, err := s.db.ListConnectorsByApp(userID, appID)
	if err != nil {
		http.Error(w, `{"error":"failed to list"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(connectors)
}

// ===================== Pending Verify (v2) =====================

// PendingConnectorVerify v2 版验证，按 connector ID 索引
type PendingConnectorVerify struct {
	Code        string
	BotToken    string
	ConnectorID string
	Done        chan *connect.VerifiedUser
}

var pendingConnectorVerifies = make(map[string]*PendingConnectorVerify) // connectorID → pending

func cancelPendingVerify(connectorID string) {
	pendingVerifiesMu.Lock()
	defer pendingVerifiesMu.Unlock()
	if old, ok := pendingConnectorVerifies[connectorID]; ok {
		close(old.Done)
		delete(pendingConnectorVerifies, connectorID)
	}
}

// handleConnectorVerifyStatus GET /api/v1/connectors/{id}/verify-status
func (s *Server) handleConnectorVerifyStatus(w http.ResponseWriter, r *http.Request) {
	connID := r.PathValue("id")

	pendingVerifiesMu.Lock()
	pending, exists := pendingConnectorVerifies[connID]
	pendingVerifiesMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if !exists {
		json.NewEncoder(w).Encode(map[string]any{"verifying": false})
		return
	}

	json.NewEncoder(w).Encode(map[string]any{
		"verifying": true,
		"code":      pending.Code,
	})
}
