package bridge

import (
	"fmt"
	"log"
	"sync"
	"time"

	"tofi-core/internal/chat"
	"tofi-core/internal/storage"
)

// ExecuteFn is the function signature for executeChatSession (decouples from Server).
type ExecuteFn func(userID, scope string, session *chat.Session, message string, opts *ExecuteOptions) error

// RestartFn is called when a user issues /restart via Telegram.
// botToken and chatID identify who requested it, so the new process can notify them.
type RestartFn func(botToken, chatID string)

// ChatBridgeDispatcher routes incoming messages to Chat Sessions and executes agents.
type ChatBridgeDispatcher struct {
	db           *storage.DB
	chatStore    *chat.Store
	execFn       ExecuteFn
	restartFn    RestartFn
	sessionLocks sync.Map // sessionID → *sync.Mutex
	pendingMsgs  sync.Map // sessionID → *pendingQueue
	bridges      sync.Map // connectorID → ChatBridge
}

// NewDispatcher creates a new dispatcher.
func NewDispatcher(db *storage.DB, chatStore *chat.Store, execFn ExecuteFn) *ChatBridgeDispatcher {
	return &ChatBridgeDispatcher{
		db:        db,
		chatStore: chatStore,
		execFn:    execFn,
	}
}

// SetRestartFn sets the callback for the /restart command.
func (d *ChatBridgeDispatcher) SetRestartFn(fn RestartFn) {
	d.restartFn = fn
}

// RegisterBridge registers a bridge for sending replies.
func (d *ChatBridgeDispatcher) RegisterBridge(b ChatBridge) {
	d.bridges.Store(b.ConnectorID(), b)
}

// UnregisterBridge removes a bridge.
func (d *ChatBridgeDispatcher) UnregisterBridge(connectorID string) {
	d.bridges.Delete(connectorID)
}

// HandleMessage processes an incoming message (called by Bridge polling loop).
func (d *ChatBridgeDispatcher) HandleMessage(msg IncomingMessage) {
	// 1. Find connector
	connector, err := d.db.GetConnectorByID(msg.ConnectorID)
	if err != nil || connector == nil {
		log.Printf("[Dispatcher] Connector %s not found", msg.ConnectorID[:8])
		return
	}
	userID := connector.UserID

	// 2. Verify sender
	receiver, err := d.db.GetReceiverByIdentifier(msg.ConnectorID, msg.ChatID)
	if err != nil {
		log.Printf("[Dispatcher] Error checking receiver: %v", err)
		return
	}

	// 3. Get bridge for replies
	bridgeVal, ok := d.bridges.Load(msg.ConnectorID)
	if !ok {
		log.Printf("[Dispatcher] No bridge for connector %s", msg.ConnectorID[:8])
		return
	}
	b := bridgeVal.(ChatBridge)

	// 4. Parse slash commands
	cmd := ParseSlashCommand(msg.Text)
	if cmd != nil {
		d.handleCommand(cmd, msg, connector, receiver, b, userID)
		return
	}

	// 5. Unverified users cannot chat
	if receiver == nil {
		_ = b.SendMessage(msg.ChatID, "请先在 Tofi 中完成验证后才能对话。")
		return
	}

	// 6. Determine scope
	scope := chat.ScopeUser // ""
	if connector.AppID != "" {
		appPrefix := connector.AppID
		if len(appPrefix) > 8 {
			appPrefix = appPrefix[:8]
		}
		scope = chat.AgentScope("app-" + appPrefix)
	}

	// 7. Find or create session
	sessionID, err := d.db.GetBridgeSession(msg.ConnectorID, msg.ChatID)
	if err != nil {
		log.Printf("[Dispatcher] Error getting bridge session: %v", err)
		_ = b.SendMessage(msg.ChatID, "❌ 内部错误，请稍后重试")
		return
	}

	var session *chat.Session
	if sessionID != "" {
		session, err = d.chatStore.Load(userID, scope, sessionID)
		if err != nil {
			sessionID = "" // session file lost, recreate
		}
	}

	if sessionID == "" {
		newID := chat.NewSessionID()
		session = chat.NewSession(newID, "", "")
		if err := d.chatStore.Save(userID, scope, session); err != nil {
			log.Printf("[Dispatcher] Error creating session: %v", err)
			_ = b.SendMessage(msg.ChatID, "❌ 创建会话失败")
			return
		}
		if err := d.db.SetBridgeSession(msg.ConnectorID, msg.ChatID, userID, newID); err != nil {
			log.Printf("[Dispatcher] Error saving bridge session: %v", err)
		}
		sessionID = newID
	}

	// 8. Concurrency control
	lockVal, _ := d.sessionLocks.LoadOrStore(sessionID, &sync.Mutex{})
	mu := lockVal.(*sync.Mutex)

	if !mu.TryLock() {
		_ = b.SendMessage(msg.ChatID, "⏳ 正在处理上一条消息，收到后会继续处理")
		d.enqueueMessage(sessionID, msg)
		return
	}

	d.executeAndDrain(b, mu, sessionID, userID, scope, session, msg)
}

// executeAndDrain executes current message, then drains pending queue.
func (d *ChatBridgeDispatcher) executeAndDrain(
	b ChatBridge, mu *sync.Mutex, sessionID string,
	userID, scope string, session *chat.Session, msg IncomingMessage,
) {
	defer mu.Unlock()

	d.executeMessage(b, userID, scope, session, msg)

	for {
		next := d.dequeueMessage(sessionID)
		if next == nil {
			break
		}
		reloaded, err := d.chatStore.Load(userID, scope, session.ID)
		if err == nil {
			session = reloaded
		}
		d.executeMessage(b, userID, scope, session, *next)
	}
}

// executeMessage executes a single message through the agent loop.
func (d *ChatBridgeDispatcher) executeMessage(
	b ChatBridge, userID, scope string, session *chat.Session, msg IncomingMessage,
) {
	// Get sender from bridge
	var sender *TelegramSender
	if tb, ok := b.(*TelegramPollingBridge); ok {
		sender = tb.sender
	} else {
		// Fallback: create a dummy sender (for non-Telegram bridges in the future)
		sender = &TelegramSender{}
	}

	_ = b.SendTyping(msg.ChatID)

	buf := NewStreamBuffer(sender, msg.ChatID)
	defer buf.Close()

	// Typing keepalive
	typingDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(telegramTypingTTL)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = b.SendTyping(msg.ChatID)
			case <-typingDone:
				return
			}
		}
	}()

	opts := &ExecuteOptions{
		OnStreamChunk: func(_, delta string) {
			buf.Write(delta)
		},
		OnStepStart: func(toolName, args string) {
			buf.FinalizeAndReset() // 工具调用前结束当前消息，后续内容从新消息开始
			_ = b.SendTyping(msg.ChatID)
		},
	}

	err := d.execFn(userID, scope, session, msg.Text, opts)

	close(typingDone)
	buf.Close()

	if err != nil {
		errMsg := fmt.Sprintf("❌ 处理失败: %s", truncateStr(err.Error(), 200))
		_ = b.SendMessage(msg.ChatID, errMsg)
		log.Printf("[Dispatcher] Agent error for session %s: %v", session.ID, err)
	}
}

// handleCommand processes slash commands.
func (d *ChatBridgeDispatcher) handleCommand(
	cmd *SlashCommand, msg IncomingMessage,
	connector *storage.Connector, receiver *storage.ConnectorReceiver,
	b ChatBridge, userID string,
) {
	switch cmd.Command {
	case "start":
		if cmd.Args != "" && receiver == nil {
			return // verification code, handled elsewhere
		}
		botName := "Tofi AI"
		if cfg, err := connector.TelegramConfig(); err == nil && cfg.BotName != "" {
			botName = cfg.BotName
		}
		_ = b.SendMessage(msg.ChatID, FormatWelcome(botName))

	case "new":
		if receiver == nil {
			_ = b.SendMessage(msg.ChatID, "请先完成验证。")
			return
		}
		_ = d.db.DeleteBridgeSession(msg.ConnectorID, msg.ChatID)
		_ = b.SendMessage(msg.ChatID, "✅ 新对话已开始，发消息吧！")

	case "stop":
		sessionID, _ := d.db.GetBridgeSession(msg.ConnectorID, msg.ChatID)
		if sessionID != "" {
			_ = b.SendMessage(msg.ChatID, "⏹ 已停止当前任务")
		} else {
			_ = b.SendMessage(msg.ChatID, "当前没有正在执行的任务")
		}

	case "status":
		if receiver == nil {
			_ = b.SendMessage(msg.ChatID, "请先完成验证。")
			return
		}
		sessionID, _ := d.db.GetBridgeSession(msg.ConnectorID, msg.ChatID)
		if sessionID == "" {
			_ = b.SendMessage(msg.ChatID, "当前没有活跃对话。发消息即可开始。")
			return
		}
		scope := chat.ScopeUser
		if connector.AppID != "" {
			appPrefix := connector.AppID
			if len(appPrefix) > 8 {
				appPrefix = appPrefix[:8]
			}
			scope = chat.AgentScope("app-" + appPrefix)
		}
		session, err := d.chatStore.Load(userID, scope, sessionID)
		if err != nil {
			_ = b.SendMessage(msg.ChatID, fmt.Sprintf("Session: %s\n状态: 未知", sessionID))
			return
		}
		status := "空闲"
		if session.Status == "running" {
			status = "运行中"
		} else if session.Status == "hold" {
			status = "等待中"
		}
		model := session.Model
		if model == "" {
			model = "默认"
		}
		statusMsg := fmt.Sprintf(
			"Session: %s\n状态: %s\n模型: %s\n消息数: %d\nTokens: %d in / %d out\n费用: $%.4f",
			session.ID, status, model,
			len(session.Messages),
			session.Usage.InputTokens, session.Usage.OutputTokens,
			session.Usage.Cost,
		)
		_ = b.SendMessage(msg.ChatID, statusMsg)

	case "restart":
		if d.restartFn == nil {
			_ = b.SendMessage(msg.ChatID, "重启功能未配置")
			return
		}
		_ = b.SendMessage(msg.ChatID, "🔄 正在重启 Tofi 服务...")
		// Get bot token for post-restart notification
		botToken := ""
		if cfg, cfgErr := connector.TelegramConfig(); cfgErr == nil {
			botToken = cfg.BotToken
		}
		chatID := msg.ChatID
		go func() {
			time.Sleep(500 * time.Millisecond)
			d.restartFn(botToken, chatID)
		}()

	case "resume":
		if receiver == nil {
			_ = b.SendMessage(msg.ChatID, "请先完成验证。")
			return
		}
		d.sendSessionHistory(connector, b, msg.ChatID, userID)

	case "help":
		_ = b.SendMessage(msg.ChatID, FormatHelp())
	}
}

// sendSessionHistory 列出用户最近的 sessions，以 inline keyboard 形式展示
func (d *ChatBridgeDispatcher) sendSessionHistory(
	connector *storage.Connector, b ChatBridge, chatID, userID string,
) {
	scope := chat.ScopeUser
	if connector.AppID != "" {
		appPrefix := connector.AppID
		if len(appPrefix) > 8 {
			appPrefix = appPrefix[:8]
		}
		scope = chat.AgentScope("app-" + appPrefix)
	}

	sessions, err := d.db.ListChatSessions(userID, scope, 10)
	if err != nil {
		_ = b.SendMessage(chatID, "❌ 获取历史会话失败")
		return
	}
	if len(sessions) == 0 {
		_ = b.SendMessage(chatID, "没有历史会话。发消息即可开始新对话。")
		return
	}

	// 当前 session
	currentSessionID, _ := d.db.GetBridgeSession(connector.ID, chatID)

	// Build inline keyboard — each session as one row
	var buttons [][]InlineButton
	for _, s := range sessions {
		title := s.Title
		if title == "" {
			title = "(未命名)"
		}
		titleRunes := []rune(title)
		if len(titleRunes) > 25 {
			title = string(titleRunes[:22]) + "..."
		}

		// Parse time
		timeStr := ""
		if t, parseErr := time.Parse("2006-01-02 15:04:05", s.UpdatedAt); parseErr == nil {
			timeStr = t.Format("01/02 15:04")
		} else if t, parseErr := time.Parse("2006-01-02T15:04:05Z", s.UpdatedAt); parseErr == nil {
			timeStr = t.Format("01/02 15:04")
		}

		label := fmt.Sprintf("%s · %d条 · %s", title, s.MessageCount, timeStr)
		if s.ID == currentSessionID {
			label = "✅ " + label
		}

		buttons = append(buttons, []InlineButton{
			{Label: label, CallbackData: "switch:" + s.ID},
		})
	}

	_ = b.SendMessageWithButtons(chatID, "📋 最近会话（点击切换）：", buttons)
}

// HandleCallback 处理 inline keyboard 按钮点击
func (d *ChatBridgeDispatcher) HandleCallback(connectorID, chatID, senderID, data string) {
	// Get bridge
	bridgeVal, ok := d.bridges.Load(connectorID)
	if !ok {
		return
	}
	b := bridgeVal.(ChatBridge)

	// Parse callback data
	if len(data) > 7 && data[:7] == "switch:" {
		sessionID := data[7:]
		// Update bridge session mapping
		connector, err := d.db.GetConnectorByID(connectorID)
		if err != nil {
			_ = b.SendMessage(chatID, "❌ 内部错误")
			return
		}
		if err := d.db.SetBridgeSession(connectorID, chatID, connector.UserID, sessionID); err != nil {
			_ = b.SendMessage(chatID, "❌ 切换失败")
			return
		}

		// Load session to show title
		scope := chat.ScopeUser
		if connector.AppID != "" {
			appPrefix := connector.AppID
			if len(appPrefix) > 8 {
				appPrefix = appPrefix[:8]
			}
			scope = chat.AgentScope("app-" + appPrefix)
		}
		session, loadErr := d.chatStore.Load(connector.UserID, scope, sessionID)
		title := sessionID[:10]
		if loadErr == nil && session.Title != "" {
			titleRunes := []rune(session.Title)
			if len(titleRunes) > 30 {
				title = string(titleRunes[:27]) + "..."
			} else {
				title = session.Title
			}
		}
		_ = b.SendMessage(chatID, fmt.Sprintf("✅ 已切换到: %s\n继续发消息即可。", title))
	}
}

func (d *ChatBridgeDispatcher) enqueueMessage(sessionID string, msg IncomingMessage) {
	val, _ := d.pendingMsgs.LoadOrStore(sessionID, &pendingQueue{})
	q := val.(*pendingQueue)
	q.mu.Lock()
	defer q.mu.Unlock()
	q.msgs = append(q.msgs, msg)
}

func (d *ChatBridgeDispatcher) dequeueMessage(sessionID string) *IncomingMessage {
	val, ok := d.pendingMsgs.Load(sessionID)
	if !ok {
		return nil
	}
	q := val.(*pendingQueue)
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.msgs) == 0 {
		return nil
	}
	msg := q.msgs[0]
	q.msgs = q.msgs[1:]
	return &msg
}

type pendingQueue struct {
	mu   sync.Mutex
	msgs []IncomingMessage
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
