package storage

import (
	"database/sql"
	"time"
)

// SettingRecord 系统/用户级设置
type SettingRecord struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Scope     string `json:"scope"`      // "system" | 用户ID
	UpdatedAt string `json:"updated_at"`
}

// initSettingsTable 创建 settings 表
func (db *DB) initSettingsTable() error {
	query := `
	CREATE TABLE IF NOT EXISTS settings (
		key TEXT NOT NULL,
		scope TEXT NOT NULL DEFAULT 'system',
		value TEXT NOT NULL,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (key, scope)
	);`

	_, err := db.conn.Exec(query)
	return err
}

// --- AI Key Management ---

// GetSetting 获取设置值（先查用户级，再查系统级）
func (db *DB) GetSetting(key, userID string) (string, error) {
	// 优先用户级
	if userID != "" {
		var val string
		err := db.conn.QueryRow("SELECT value FROM settings WHERE key = ? AND scope = ?", key, userID).Scan(&val)
		if err == nil {
			return val, nil
		}
	}
	// 回退系统级
	var val string
	err := db.conn.QueryRow("SELECT value FROM settings WHERE key = ? AND scope = 'system'", key).Scan(&val)
	if err != nil {
		return "", err
	}
	return val, nil
}

// SetSetting 设置值
func (db *DB) SetSetting(key, scope, value string) error {
	query := `
	INSERT INTO settings (key, scope, value, updated_at) VALUES (?, ?, ?, ?)
	ON CONFLICT(key, scope) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`

	now := time.Now().Format("2006-01-02 15:04:05")
	_, err := db.conn.Exec(query, key, scope, value, now)
	return err
}

// DeleteSetting 删除设置
func (db *DB) DeleteSetting(key, scope string) error {
	_, err := db.conn.Exec("DELETE FROM settings WHERE key = ? AND scope = ?", key, scope)
	return err
}

// ListSettings 列出某 scope 的所有设置
func (db *DB) ListSettings(scope string) ([]*SettingRecord, error) {
	query := `SELECT key, scope, value, updated_at FROM settings WHERE scope = ? ORDER BY key`
	rows, err := db.conn.Query(query, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*SettingRecord
	for rows.Next() {
		var r SettingRecord
		if err := rows.Scan(&r.Key, &r.Scope, &r.Value, &r.UpdatedAt); err != nil {
			continue
		}
		records = append(records, &r)
	}
	return records, nil
}

// --- 便捷方法: AI API Key ---

// AI Key 存储约定:
//   key = "ai_key_{provider}"   (如 ai_key_anthropic, ai_key_openai)
//   scope = "system"            (系统默认 Key)
//   scope = "{userID}"          (用户自定义 Key)

// GetAIKey 获取 AI API Key（用户级优先，回退系统级）
func (db *DB) GetAIKey(provider, userID string) (string, error) {
	key := "ai_key_" + provider
	return db.GetSetting(key, userID)
}

// SetAIKey 设置 AI API Key
func (db *DB) SetAIKey(provider, scope, apiKey string) error {
	key := "ai_key_" + provider
	return db.SetSetting(key, scope, apiKey)
}

// DeleteAIKey 删除 AI API Key
func (db *DB) DeleteAIKey(provider, scope string) error {
	key := "ai_key_" + provider
	return db.DeleteSetting(key, scope)
}

// ListAIKeys 列出所有 AI Key 配置（不返回完整 key，仅前后各 4 位）
func (db *DB) ListAIKeys(scope string) ([]map[string]string, error) {
	query := `SELECT key, value, updated_at FROM settings WHERE scope = ? AND key LIKE 'ai_key_%' ORDER BY key`
	rows, err := db.conn.Query(query, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []map[string]string
	for rows.Next() {
		var key, value, updatedAt string
		if err := rows.Scan(&key, &value, &updatedAt); err != nil {
			continue
		}
		// 提取 provider 名称
		provider := key[7:] // 去掉 "ai_key_" 前缀
		// 掩码处理
		masked := maskAPIKey(value)
		results = append(results, map[string]string{
			"provider":   provider,
			"masked_key": masked,
			"updated_at": updatedAt,
		})
	}
	return results, nil
}

// maskAPIKey 掩码 API Key: "sk-abc...xyz" → "sk-a****xyz"
func maskAPIKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}

// ResolveAIKey 解析 AI Key（优先级：用户 > 系统 > 报错）
func (db *DB) ResolveAIKey(provider, userID string) (string, error) {
	apiKey, err := db.GetAIKey(provider, userID)
	if err == nil && apiKey != "" {
		return apiKey, nil
	}
	// 如果用户级没有，尝试系统级
	if err == sql.ErrNoRows || apiKey == "" {
		apiKey, err = db.GetAIKey(provider, "")
		if err == nil && apiKey != "" {
			return apiKey, nil
		}
	}
	return "", sql.ErrNoRows
}
