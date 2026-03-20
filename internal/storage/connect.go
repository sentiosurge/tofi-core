package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ConnectorType 渠道类型
type ConnectorType string

const (
	ConnectorTelegram       ConnectorType = "telegram"
	ConnectorSlackWebhook   ConnectorType = "slack_webhook"
	ConnectorSlackApp       ConnectorType = "slack_app"
	ConnectorDiscordWebhook ConnectorType = "discord_webhook"
	ConnectorDiscordBot     ConnectorType = "discord_bot"
	ConnectorEmail          ConnectorType = "email"
)

// ConnectorCapability 渠道能力
func (t ConnectorType) CanSend() bool { return true } // 所有类型都能发
func (t ConnectorType) CanReceive() bool {
	switch t {
	case ConnectorTelegram, ConnectorSlackApp, ConnectorDiscordBot, ConnectorEmail:
		return true
	default:
		return false
	}
}

// Connector Scope constants
const (
	ScopeGlobal = "global:*" // available to all apps
)

// ScopeApp returns the scope string for an app-specific connector.
func ScopeApp(appID string) string { return "app:" + appID }

// Connector 一个渠道实例
type Connector struct {
	ID        string        `json:"id"`
	UserID    string        `json:"user_id"`
	Scope     string        `json:"scope"`          // "global:*" or "app:{appID}"
	Type      ConnectorType `json:"type"`
	Name      string        `json:"name,omitempty"` // app 专属时有意义
	Config    string        `json:"config"`         // JSON，按 type 不同结构
	Enabled   bool          `json:"enabled"`
	CreatedAt string        `json:"created_at"`
}

// IsGlobal returns true if the connector is available to all apps.
func (c *Connector) IsGlobal() bool {
	return c.Scope == ScopeGlobal || c.Scope == ""
}

// ScopeAppID extracts the app ID from an app-scoped connector. Returns "" for global.
func (c *Connector) ScopeAppID() string {
	if strings.HasPrefix(c.Scope, "app:") {
		return c.Scope[4:]
	}
	return ""
}

// ConnectorReceiver 渠道下的接收者
type ConnectorReceiver struct {
	ID          int64  `json:"id"`
	ConnectorID string `json:"connector_id"`
	Identifier  string `json:"identifier"`   // chat_id / email / slack_uid
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
	Metadata    string `json:"metadata"` // JSON，渠道特有信息
	VerifiedAt  string `json:"verified_at"`
}

// AppConnector App 和 Connector 的多对多关联
type AppConnector struct {
	AppID       string `json:"app_id"`
	ConnectorID string `json:"connector_id"`
	CreatedAt   string `json:"created_at"`
}

// AppNotifyTarget links an App to specific receivers for push notifications.
// Unlike AppConnector (which grants interaction scope), this controls who gets notified
// when the App completes a run — regardless of their interaction scope.
type AppNotifyTarget struct {
	AppID      string `json:"app_id"`
	ReceiverID int64  `json:"receiver_id"`
	CreatedAt  string `json:"created_at"`
}

// --- Telegram Config helpers ---

type TelegramConnectorConfig struct {
	BotToken    string `json:"bot_token"`
	BotName     string `json:"bot_name"`
	BotUsername string `json:"bot_username"`
	BotPhoto    string `json:"bot_photo"`
}

func (c *Connector) TelegramConfig() (*TelegramConnectorConfig, error) {
	if c.Type != ConnectorTelegram {
		return nil, fmt.Errorf("connector %s is not telegram", c.ID)
	}
	var cfg TelegramConnectorConfig
	if err := json.Unmarshal([]byte(c.Config), &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// --- Webhook Config helpers (Discord Webhook / Slack Webhook) ---

type WebhookConnectorConfig struct {
	WebhookURL string `json:"webhook_url"`
	Name       string `json:"name,omitempty"`
}

func (c *Connector) WebhookConfig() (*WebhookConnectorConfig, error) {
	if c.Type != ConnectorDiscordWebhook && c.Type != ConnectorSlackWebhook {
		return nil, fmt.Errorf("connector %s is not a webhook type", c.ID)
	}
	var cfg WebhookConnectorConfig
	if err := json.Unmarshal([]byte(c.Config), &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// --- Telegram Receiver metadata helpers ---

type TelegramReceiverMeta struct {
	ChatID   string `json:"chat_id"`
	Username string `json:"username"`
}

func (r *ConnectorReceiver) TelegramMeta() (*TelegramReceiverMeta, error) {
	var meta TelegramReceiverMeta
	if err := json.Unmarshal([]byte(r.Metadata), &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

// ===================== Table Init =====================

func (db *DB) initConnectorsTable() error {
	_, err := db.conn.Exec(`
	CREATE TABLE IF NOT EXISTS connectors (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		app_id TEXT NOT NULL DEFAULT '',
		scope TEXT NOT NULL DEFAULT 'global:*',
		type TEXT NOT NULL,
		name TEXT NOT NULL DEFAULT '',
		config TEXT NOT NULL DEFAULT '{}',
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_connectors_user ON connectors(user_id);
	`)
	if err != nil {
		return err
	}

	// Migration: add scope column if missing (must run BEFORE scope index)
	db.migrateConnectorScope()

	// Now safe to create scope index
	db.conn.Exec(`CREATE INDEX IF NOT EXISTS idx_connectors_scope ON connectors(scope)`)
	return nil
}

// migrateConnectorScope adds scope column and populates it from legacy app_id.
func (db *DB) migrateConnectorScope() {
	// Check if scope column exists
	var count int
	err := db.conn.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('connectors') WHERE name='scope'`).Scan(&count)
	if err != nil || count == 0 {
		// Column doesn't exist — add it
		db.conn.Exec(`ALTER TABLE connectors ADD COLUMN scope TEXT NOT NULL DEFAULT 'global:*'`)
	}

	// Migrate: app_id != '' → scope = 'app:' + app_id
	db.conn.Exec(`UPDATE connectors SET scope = 'app:' || app_id WHERE app_id != '' AND (scope = '' OR scope = 'global:*')`)
	// Ensure empty scope gets default
	db.conn.Exec(`UPDATE connectors SET scope = 'global:*' WHERE scope = ''`)
}

func (db *DB) initConnectorReceiversTable() error {
	_, err := db.conn.Exec(`
	CREATE TABLE IF NOT EXISTS connector_receivers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		connector_id TEXT NOT NULL,
		identifier TEXT NOT NULL,
		display_name TEXT NOT NULL DEFAULT '',
		avatar_url TEXT NOT NULL DEFAULT '',
		metadata TEXT NOT NULL DEFAULT '{}',
		verified_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(connector_id, identifier),
		FOREIGN KEY(connector_id) REFERENCES connectors(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_connector_receivers_connector ON connector_receivers(connector_id);
	`)
	return err
}

func (db *DB) initAppConnectorsTable() error {
	_, err := db.conn.Exec(`
	CREATE TABLE IF NOT EXISTS app_connectors (
		app_id TEXT NOT NULL,
		connector_id TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY(app_id, connector_id),
		FOREIGN KEY(app_id) REFERENCES apps(id) ON DELETE CASCADE,
		FOREIGN KEY(connector_id) REFERENCES connectors(id) ON DELETE CASCADE
	);
	`)
	return err
}

func (db *DB) initAppNotifyTargetsTable() error {
	_, err := db.conn.Exec(`
	CREATE TABLE IF NOT EXISTS app_notify_targets (
		app_id TEXT NOT NULL,
		receiver_id INTEGER NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY(app_id, receiver_id),
		FOREIGN KEY(app_id) REFERENCES apps(id) ON DELETE CASCADE,
		FOREIGN KEY(receiver_id) REFERENCES connector_receivers(id) ON DELETE CASCADE
	);
	`)
	return err
}

// ── App Notify Targets CRUD ──

// SetAppNotifyTargets replaces all notify targets for an App.
func (db *DB) SetAppNotifyTargets(appID string, receiverIDs []int64) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM app_notify_targets WHERE app_id = ?`, appID); err != nil {
		return err
	}

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO app_notify_targets (app_id, receiver_id) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, rid := range receiverIDs {
		if _, err := stmt.Exec(appID, rid); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// ListAppNotifyTargets returns all notify target receivers for an App.
func (db *DB) ListAppNotifyTargets(appID string) ([]*ConnectorReceiver, error) {
	rows, err := db.conn.Query(`
		SELECT cr.id, cr.connector_id, cr.identifier, cr.display_name, cr.avatar_url, cr.metadata, cr.verified_at
		FROM connector_receivers cr
		JOIN app_notify_targets ant ON ant.receiver_id = cr.id
		WHERE ant.app_id = ?
		ORDER BY cr.display_name
	`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*ConnectorReceiver
	for rows.Next() {
		r := &ConnectorReceiver{}
		if err := rows.Scan(&r.ID, &r.ConnectorID, &r.Identifier, &r.DisplayName, &r.AvatarURL, &r.Metadata, &r.VerifiedAt); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, nil
}

// AddAppNotifyTarget adds a single receiver as a notify target for an App.
func (db *DB) AddAppNotifyTarget(appID string, receiverID int64) error {
	_, err := db.conn.Exec(
		`INSERT OR IGNORE INTO app_notify_targets (app_id, receiver_id) VALUES (?, ?)`,
		appID, receiverID)
	return err
}

// RemoveAppNotifyTarget removes a single receiver from an App's notify targets.
func (db *DB) RemoveAppNotifyTarget(appID string, receiverID int64) error {
	_, err := db.conn.Exec(
		`DELETE FROM app_notify_targets WHERE app_id = ? AND receiver_id = ?`,
		appID, receiverID)
	return err
}

// ===================== Migration =====================

// migrateOldTelegramToConnectors 从 settings 表迁移旧的 Telegram 配置
func (db *DB) migrateOldTelegramToConnectors() {
	// 检查是否有旧数据
	rows, err := db.conn.Query(
		`SELECT DISTINCT scope FROM settings WHERE key = ?`,
		keyTelegramBotToken,
	)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			continue
		}

		// 检查是否已迁移
		var count int
		db.conn.QueryRow(
			`SELECT COUNT(*) FROM connectors WHERE user_id = ? AND type = 'telegram'`,
			userID,
		).Scan(&count)
		if count > 0 {
			continue
		}

		// 读取旧配置
		oldCfg, err := db.GetTelegramConfig(userID)
		if err != nil {
			continue
		}

		// 创建新 connector
		cfg := TelegramConnectorConfig{
			BotToken:    oldCfg.BotToken,
			BotName:     oldCfg.BotName,
			BotUsername:  oldCfg.BotUsername,
			BotPhoto:    oldCfg.BotPhoto,
		}
		cfgJSON, _ := json.Marshal(cfg)

		connID := uuid.New().String()
		_, err = db.conn.Exec(
			`INSERT INTO connectors (id, user_id, scope, type, config, enabled) VALUES (?, ?, 'global:*', 'telegram', ?, ?)`,
			connID, userID, string(cfgJSON), oldCfg.Enabled,
		)
		if err != nil {
			continue
		}

		// 迁移 receivers
		oldReceivers, _ := db.ListTelegramReceivers(userID)
		for _, r := range oldReceivers {
			meta := TelegramReceiverMeta{ChatID: r.ChatID, Username: r.Username}
			metaJSON, _ := json.Marshal(meta)
			db.conn.Exec(
				`INSERT OR IGNORE INTO connector_receivers (connector_id, identifier, display_name, avatar_url, metadata, verified_at)
				 VALUES (?, ?, ?, ?, ?, ?)`,
				connID, r.ChatID, r.DisplayName, r.AvatarURL, string(metaJSON), r.ConnectedAt,
			)
		}
	}
}

// ===================== Connector CRUD =====================

// CreateConnector 创建新 connector
func (db *DB) CreateConnector(userID string, scope string, ctype ConnectorType, name string, config string) (*Connector, error) {
	id := uuid.New().String()
	now := time.Now().Format("2006-01-02 15:04:05")
	if scope == "" {
		scope = ScopeGlobal
	}

	_, err := db.conn.Exec(
		`INSERT INTO connectors (id, user_id, scope, type, name, config, enabled, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, 1, ?)`,
		id, userID, scope, string(ctype), name, config, now,
	)
	if err != nil {
		return nil, err
	}

	return &Connector{
		ID:        id,
		UserID:    userID,
		Scope:     scope,
		Type:      ctype,
		Name:      name,
		Config:    config,
		Enabled:   true,
		CreatedAt: now,
	}, nil
}

// GetConnector 获取单个 connector
func (db *DB) GetConnector(connectorID, userID string) (*Connector, error) {
	c := &Connector{}
	err := db.conn.QueryRow(
		`SELECT id, user_id, scope, type, name, config, enabled, created_at
		 FROM connectors WHERE id = ? AND user_id = ?`,
		connectorID, userID,
	).Scan(&c.ID, &c.UserID, &c.Scope, &c.Type, &c.Name, &c.Config, &c.Enabled, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// ListConnectors 列出用户的所有 connectors
func (db *DB) ListConnectors(userID string) ([]*Connector, error) {
	rows, err := db.conn.Query(
		`SELECT id, user_id, scope, type, name, config, enabled, created_at
		 FROM connectors WHERE user_id = ? ORDER BY created_at`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var connectors []*Connector
	for rows.Next() {
		c := &Connector{}
		if err := rows.Scan(&c.ID, &c.UserID, &c.Scope, &c.Type, &c.Name, &c.Config, &c.Enabled, &c.CreatedAt); err != nil {
			continue
		}
		connectors = append(connectors, c)
	}
	return connectors, nil
}

// ListConnectorsForApp 列出 app 可用的 connectors：global + app 专属
func (db *DB) ListConnectorsForApp(userID, appID string) ([]*Connector, error) {
	rows, err := db.conn.Query(
		`SELECT id, user_id, scope, type, name, config, enabled, created_at
		 FROM connectors
		 WHERE user_id = ? AND (scope = 'global:*' OR scope = ?)
		 ORDER BY created_at`,
		userID, ScopeApp(appID),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var connectors []*Connector
	for rows.Next() {
		c := &Connector{}
		if err := rows.Scan(&c.ID, &c.UserID, &c.Scope, &c.Type, &c.Name, &c.Config, &c.Enabled, &c.CreatedAt); err != nil {
			continue
		}
		connectors = append(connectors, c)
	}
	return connectors, nil
}

// UpdateConnectorConfig 更新 connector 配置
func (db *DB) UpdateConnectorConfig(connectorID, userID, config string) error {
	_, err := db.conn.Exec(
		`UPDATE connectors SET config = ? WHERE id = ? AND user_id = ?`,
		config, connectorID, userID,
	)
	return err
}

// SetConnectorEnabled 启用/禁用
func (db *DB) SetConnectorEnabled(connectorID, userID string, enabled bool) error {
	val := 0
	if enabled {
		val = 1
	}
	_, err := db.conn.Exec(
		`UPDATE connectors SET enabled = ? WHERE id = ? AND user_id = ?`,
		val, connectorID, userID,
	)
	return err
}

// DeleteConnector 删除 connector 及其 receivers 和 app_connectors 关联
func (db *DB) DeleteConnector(connectorID, userID string) error {
	// FK CASCADE 会自动清理 connector_receivers 和 app_connectors
	_, err := db.conn.Exec(
		`DELETE FROM connectors WHERE id = ? AND user_id = ?`,
		connectorID, userID,
	)
	return err
}

// ===================== Receiver CRUD =====================

// AddConnectorReceiver 添加接收者
func (db *DB) AddConnectorReceiver(connectorID, identifier, displayName, avatarURL, metadata string) (*ConnectorReceiver, error) {
	now := time.Now().Format("2006-01-02 15:04:05")
	result, err := db.conn.Exec(
		`INSERT INTO connector_receivers (connector_id, identifier, display_name, avatar_url, metadata, verified_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(connector_id, identifier) DO UPDATE SET
		   display_name = excluded.display_name,
		   avatar_url = excluded.avatar_url,
		   metadata = excluded.metadata,
		   verified_at = excluded.verified_at`,
		connectorID, identifier, displayName, avatarURL, metadata, now,
	)
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	return &ConnectorReceiver{
		ID:          id,
		ConnectorID: connectorID,
		Identifier:  identifier,
		DisplayName: displayName,
		AvatarURL:   avatarURL,
		Metadata:    metadata,
		VerifiedAt:  now,
	}, nil
}

// ListConnectorReceivers 列出 connector 的所有接收者
func (db *DB) ListConnectorReceivers(connectorID string) ([]*ConnectorReceiver, error) {
	rows, err := db.conn.Query(
		`SELECT id, connector_id, identifier, display_name, avatar_url, metadata, verified_at
		 FROM connector_receivers WHERE connector_id = ? ORDER BY verified_at`,
		connectorID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var receivers []*ConnectorReceiver
	for rows.Next() {
		r := &ConnectorReceiver{}
		if err := rows.Scan(&r.ID, &r.ConnectorID, &r.Identifier, &r.DisplayName, &r.AvatarURL, &r.Metadata, &r.VerifiedAt); err != nil {
			continue
		}
		receivers = append(receivers, r)
	}
	return receivers, nil
}

// GetConnectorReceiver 获取单个接收者
func (db *DB) GetConnectorReceiver(receiverID int64) (*ConnectorReceiver, error) {
	r := &ConnectorReceiver{}
	err := db.conn.QueryRow(
		`SELECT id, connector_id, identifier, display_name, avatar_url, metadata, verified_at
		 FROM connector_receivers WHERE id = ?`,
		receiverID,
	).Scan(&r.ID, &r.ConnectorID, &r.Identifier, &r.DisplayName, &r.AvatarURL, &r.Metadata, &r.VerifiedAt)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// DeleteConnectorReceiver 删除接收者
func (db *DB) DeleteConnectorReceiver(receiverID int64) error {
	_, err := db.conn.Exec(`DELETE FROM connector_receivers WHERE id = ?`, receiverID)
	return err
}

// ===================== App-Connector 多对多 =====================

// LinkAppConnector 绑定 app 到 connector
func (db *DB) LinkAppConnector(appID, connectorID string) error {
	_, err := db.conn.Exec(
		`INSERT OR IGNORE INTO app_connectors (app_id, connector_id) VALUES (?, ?)`,
		appID, connectorID,
	)
	return err
}

// UnlinkAppConnector 解绑
func (db *DB) UnlinkAppConnector(appID, connectorID string) error {
	_, err := db.conn.Exec(
		`DELETE FROM app_connectors WHERE app_id = ? AND connector_id = ?`,
		appID, connectorID,
	)
	return err
}

// ListAppConnectorIDs 列出 app 绑定的 connector IDs
func (db *DB) ListAppConnectorIDs(appID string) ([]string, error) {
	rows, err := db.conn.Query(
		`SELECT connector_id FROM app_connectors WHERE app_id = ?`, appID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// FindConnectorByType 按用户+类型查找 global connector
func (db *DB) FindConnectorByType(userID string, ctype ConnectorType) (*Connector, error) {
	c := &Connector{}
	err := db.conn.QueryRow(
		`SELECT id, user_id, scope, type, name, config, enabled, created_at
		 FROM connectors WHERE user_id = ? AND type = ? AND scope = 'global:*' LIMIT 1`,
		userID, string(ctype),
	).Scan(&c.ID, &c.UserID, &c.Scope, &c.Type, &c.Name, &c.Config, &c.Enabled, &c.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return c, nil
}

// GetConnectorByID 按 ID 查找 connector（不限 userID，内部使用）
func (db *DB) GetConnectorByID(connectorID string) (*Connector, error) {
	c := &Connector{}
	err := db.conn.QueryRow(
		`SELECT id, user_id, scope, type, name, config, enabled, created_at
		 FROM connectors WHERE id = ?`,
		connectorID,
	).Scan(&c.ID, &c.UserID, &c.Scope, &c.Type, &c.Name, &c.Config, &c.Enabled, &c.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return c, nil
}

// ListAllConnectorsByType 查询所有启用的指定类型 Connector（跨用户）
func (db *DB) ListAllConnectorsByType(ctype ConnectorType) ([]*Connector, error) {
	rows, err := db.conn.Query(
		`SELECT id, user_id, scope, type, name, config, enabled, created_at
		 FROM connectors WHERE type = ? AND enabled = 1 ORDER BY created_at`,
		string(ctype),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var connectors []*Connector
	for rows.Next() {
		c := &Connector{}
		if err := rows.Scan(&c.ID, &c.UserID, &c.Scope, &c.Type, &c.Name, &c.Config, &c.Enabled, &c.CreatedAt); err != nil {
			continue
		}
		connectors = append(connectors, c)
	}
	return connectors, nil
}

// ===================== Receiver CRUD (extended) =====================

// GetReceiverByIdentifier 根据 connectorID + identifier 查找接收者
func (db *DB) GetReceiverByIdentifier(connectorID, identifier string) (*ConnectorReceiver, error) {
	r := &ConnectorReceiver{}
	err := db.conn.QueryRow(
		`SELECT id, connector_id, identifier, display_name, avatar_url, metadata, verified_at
		 FROM connector_receivers WHERE connector_id = ? AND identifier = ?`,
		connectorID, identifier,
	).Scan(&r.ID, &r.ConnectorID, &r.Identifier, &r.DisplayName, &r.AvatarURL, &r.Metadata, &r.VerifiedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

// ===================== Bridge Sessions =====================

func (db *DB) initBridgeSessionsTable() error {
	_, err := db.conn.Exec(`
	CREATE TABLE IF NOT EXISTS bridge_sessions (
		connector_id TEXT NOT NULL,
		chat_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY(connector_id, chat_id),
		FOREIGN KEY(connector_id) REFERENCES connectors(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_bridge_sessions_user ON bridge_sessions(user_id);
	`)
	return err
}

// GetBridgeSession 查找已有的 bridge → chat session 映射
func (db *DB) GetBridgeSession(connectorID, chatID string) (string, error) {
	var sessionID string
	err := db.conn.QueryRow(
		`SELECT session_id FROM bridge_sessions WHERE connector_id = ? AND chat_id = ?`,
		connectorID, chatID,
	).Scan(&sessionID)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return sessionID, nil
}

// SetBridgeSession 设置或更新 bridge → chat session 映射
func (db *DB) SetBridgeSession(connectorID, chatID, userID, sessionID string) error {
	_, err := db.conn.Exec(
		`INSERT INTO bridge_sessions (connector_id, chat_id, user_id, session_id)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(connector_id, chat_id) DO UPDATE SET
		   session_id = excluded.session_id`,
		connectorID, chatID, userID, sessionID,
	)
	return err
}

// DeleteBridgeSession 删除映射（/new 命令时使用）
func (db *DB) DeleteBridgeSession(connectorID, chatID string) error {
	_, err := db.conn.Exec(
		`DELETE FROM bridge_sessions WHERE connector_id = ? AND chat_id = ?`,
		connectorID, chatID,
	)
	return err
}

// DeleteBridgeSessionsByConnector 删除 connector 下所有映射
func (db *DB) DeleteBridgeSessionsByConnector(connectorID string) error {
	_, err := db.conn.Exec(
		`DELETE FROM bridge_sessions WHERE connector_id = ?`,
		connectorID,
	)
	return err
}
