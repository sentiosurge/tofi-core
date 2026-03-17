package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"
	"tofi-core/internal/notify"
	"tofi-core/internal/storage"
)

// ===================== Connector CRUD =====================

// handleListConnectors GET /api/v1/connectors
func (s *Server) handleListConnectors(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")

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
	userID := r.Header.Get("X-User-ID")

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

	// 对于 telegram，验证 bot token
	if ctype == storage.ConnectorTelegram {
		var tgCfg storage.TelegramConnectorConfig
		if err := json.Unmarshal(req.Config, &tgCfg); err != nil || tgCfg.BotToken == "" {
			http.Error(w, `{"error":"bot_token required for telegram"}`, http.StatusBadRequest)
			return
		}

		info, err := notify.GetBotInfo(tgCfg.BotToken)
		if err != nil {
			http.Error(w, `{"error":"invalid bot token: `+err.Error()+`"}`, http.StatusBadRequest)
			return
		}

		// 用验证后的信息填充 config
		tgCfg.BotName = info.Name
		tgCfg.BotUsername = info.Username
		tgCfg.BotPhoto = info.PhotoURL
		cfgJSON, _ := json.Marshal(tgCfg)
		req.Config = cfgJSON
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
	userID := r.Header.Get("X-User-ID")
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
	userID := r.Header.Get("X-User-ID")
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
					go notify.SendMessage(tgCfg.BotToken, meta.ChatID, "🔕 This Tofi connector has been removed.")
				}
			}
		}
	}

	// 取消 pending 验证
	cancelPendingVerify(connID)

	if err := s.db.DeleteConnector(connID, userID); err != nil {
		http.Error(w, `{"error":"failed to delete"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleToggleConnector PUT /api/v1/connectors/{id}/toggle
func (s *Server) handleToggleConnector(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// ===================== Receiver Management =====================

// handleConnectorVerify POST /api/v1/connectors/{id}/verify
// Telegram: 生成验证码，polling 等待
func (s *Server) handleConnectorVerify(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
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

	done := make(chan *notify.VerifiedUser, 1)

	// 生成唯一验证码
	pendingVerifiesMu.Lock()
	var code string
	for {
		code = notify.GenerateVerifyCode()
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

	// 后台 polling
	go func() {
		defer cancelPendingVerify(connID)

		verified, err := notify.PollForVerifyCode(tgCfg.BotToken, code, 5*time.Minute)
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

		select {
		case done <- verified:
		default:
		}
	}()

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
	userID := r.Header.Get("X-User-ID")
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
	userID := r.Header.Get("X-User-ID")
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
				go notify.SendMessage(tgCfg.BotToken, meta.ChatID, "🔕 You have been removed from Tofi notifications.")
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
	userID := r.Header.Get("X-User-ID")
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

	if conn.Type != storage.ConnectorTelegram {
		http.Error(w, `{"error":"test only supported for telegram currently"}`, http.StatusBadRequest)
		return
	}

	tgCfg, err := conn.TelegramConfig()
	if err != nil || tgCfg.BotToken == "" {
		http.Error(w, `{"error":"telegram config invalid"}`, http.StatusBadRequest)
		return
	}

	msg := "✅ *Tofi Connected!*\n\nYou'll receive task notifications here."

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
		if err := notify.SendMessage(tgCfg.BotToken, meta.ChatID, msg); err != nil {
			log.Printf("[connector] test message failed: %v", err)
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
				if err := notify.SendMessage(tgCfg.BotToken, meta.ChatID, msg); err != nil {
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
	userID := r.Header.Get("X-User-ID")
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
	_ = r.Header.Get("X-User-ID")
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
	userID := r.Header.Get("X-User-ID")
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
	Done        chan *notify.VerifiedUser
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
