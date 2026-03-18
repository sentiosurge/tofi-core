package bridge

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"tofi-core/internal/storage"
)

// ChatBridgeManager manages all active ChatBridge instances.
type ChatBridgeManager struct {
	bridges    map[string]ChatBridge // connectorID → bridge
	mu         sync.Mutex
	db         *storage.DB
	dispatcher *ChatBridgeDispatcher
	ctx        context.Context
	cancel     context.CancelFunc
}

func NewManager(db *storage.DB, dispatcher *ChatBridgeDispatcher) *ChatBridgeManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &ChatBridgeManager{
		bridges:    make(map[string]ChatBridge),
		db:         db,
		dispatcher: dispatcher,
		ctx:        ctx,
		cancel:     cancel,
	}
}

// StartAll scans all enabled CanReceive connectors and starts bridges.
func (m *ChatBridgeManager) StartAll() {
	connectors, err := m.db.ListAllConnectorsByType(storage.ConnectorTelegram)
	if err != nil {
		log.Printf("[BridgeManager] Failed to list connectors: %v", err)
		return
	}
	for _, c := range connectors {
		if err := m.StartBridge(c); err != nil {
			log.Printf("[BridgeManager] Failed to start bridge for %s: %v", c.ID[:8], err)
		}
	}
	log.Printf("[BridgeManager] Started %d bridges", len(m.bridges))
}

func (m *ChatBridgeManager) StartBridge(connector *storage.Connector) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.bridges[connector.ID]; exists {
		return nil
	}
	if !connector.Enabled || !connector.Type.CanReceive() {
		return nil
	}

	b, err := m.createBridge(connector)
	if err != nil {
		return err
	}

	m.bridges[connector.ID] = b
	m.dispatcher.RegisterBridge(b)

	go func() {
		if err := b.Start(m.ctx); err != nil {
			log.Printf("[BridgeManager] Bridge %s exited: %v", connector.ID[:8], err)
		}
		m.mu.Lock()
		delete(m.bridges, connector.ID)
		m.mu.Unlock()
		m.dispatcher.UnregisterBridge(connector.ID)
	}()

	return nil
}

func (m *ChatBridgeManager) StopBridge(connectorID string) {
	m.mu.Lock()
	b, exists := m.bridges[connectorID]
	if exists {
		delete(m.bridges, connectorID)
	}
	m.mu.Unlock()

	if exists {
		b.Stop()
		m.dispatcher.UnregisterBridge(connectorID)
		log.Printf("[BridgeManager] Stopped bridge for %s", connectorID[:8])
	}
}

func (m *ChatBridgeManager) RestartBridge(connector *storage.Connector) error {
	m.StopBridge(connector.ID)
	return m.StartBridge(connector)
}

func (m *ChatBridgeManager) StopAll() {
	m.cancel()
	m.mu.Lock()
	for id := range m.bridges {
		delete(m.bridges, id)
	}
	m.mu.Unlock()
	log.Printf("[BridgeManager] All bridges stopped")
}

func (m *ChatBridgeManager) IsRunning(connectorID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, exists := m.bridges[connectorID]
	return exists
}

// WaitForVerifyCode 通过已运行的 bridge 等待验证码。
// 如果该 connector 没有运行中的 bridge，返回 nil 错误（表示不可用）。
func (m *ChatBridgeManager) WaitForVerifyCode(connectorID, code string, timeout time.Duration) (*verifyResult, error) {
	m.mu.Lock()
	b, exists := m.bridges[connectorID]
	m.mu.Unlock()

	if !exists {
		return nil, fmt.Errorf("no active bridge")
	}

	tb, ok := b.(*TelegramPollingBridge)
	if !ok {
		return nil, fmt.Errorf("bridge is not Telegram")
	}

	return tb.WaitForVerifyCode(code, timeout)
}

func (m *ChatBridgeManager) createBridge(connector *storage.Connector) (ChatBridge, error) {
	switch connector.Type {
	case storage.ConnectorTelegram:
		cfg, err := connector.TelegramConfig()
		if err != nil {
			return nil, err
		}
		tb := NewTelegramPollingBridge(
			connector.ID, cfg.BotToken, cfg.BotName, m.dispatcher.HandleMessage,
		)
		tb.SetCallbackHandler(m.dispatcher.HandleCallback)
		return tb, nil
	default:
		return nil, fmt.Errorf("unsupported bridge type: %s", connector.Type)
	}
}
