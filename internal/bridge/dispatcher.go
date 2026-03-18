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

// ChatBridgeDispatcher routes incoming messages to Chat Sessions and executes agents.
type ChatBridgeDispatcher struct {
	db           *storage.DB
	chatStore    *chat.Store
	execFn       ExecuteFn
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
			buf.Flush()
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

	case "help":
		_ = b.SendMessage(msg.ChatID, FormatHelp())
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
