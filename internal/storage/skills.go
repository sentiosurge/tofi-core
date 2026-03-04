package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SkillRecord 数据库中的技能记录
type SkillRecord struct {
	ID          string `json:"id"`          // slug: "skill-name" 或 "owner/skill-name"
	Name        string `json:"name"`        // 从 SKILL.md name 字段
	Description string `json:"description"` // 从 SKILL.md description
	Version     string `json:"version"`     // 版本号

	// 来源信息
	Source    string `json:"source"`               // "local" | "git" | "registry"
	SourceURL string `json:"source_url,omitempty"` // git repo URL 或 registry URL

	// 内容
	ManifestJSON string `json:"manifest_json"` // 完整 frontmatter JSON
	Instructions string `json:"instructions"`  // SKILL.md body (Markdown)

	// 能力标记
	HasScripts      bool   `json:"has_scripts"`      // 是否有 scripts/ 目录
	RequiredSecrets string `json:"required_secrets"`  // JSON array: ["API_KEY", "TOKEN"]
	AllowedTools    string `json:"allowed_tools"`     // JSON array: ["Bash(git:*)"]

	// 所有者
	UserID string `json:"user_id"` // 安装者

	// 时间
	InstalledAt string `json:"installed_at"`
	UpdatedAt   string `json:"updated_at"`
}

// RequiredSecretsList 解析 JSON 数组返回 secret 列表
func (r *SkillRecord) RequiredSecretsList() []string {
	if r.RequiredSecrets == "" || r.RequiredSecrets == "[]" {
		return nil
	}
	var list []string
	json.Unmarshal([]byte(r.RequiredSecrets), &list)
	return list
}

// AllowedToolsList 解析 JSON 数组返回工具列表
func (r *SkillRecord) AllowedToolsList() []string {
	if r.AllowedTools == "" || r.AllowedTools == "[]" {
		return nil
	}
	var list []string
	json.Unmarshal([]byte(r.AllowedTools), &list)
	return list
}

// initSkillsTable 创建 skills 表
func (db *DB) initSkillsTable() error {
	query := `
	CREATE TABLE IF NOT EXISTS skills (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT,
		version TEXT DEFAULT '1.0',
		source TEXT NOT NULL DEFAULT 'local',
		source_url TEXT,
		manifest_json TEXT NOT NULL,
		instructions TEXT NOT NULL,
		has_scripts INTEGER DEFAULT 0,
		required_secrets TEXT DEFAULT '[]',
		allowed_tools TEXT DEFAULT '[]',
		user_id TEXT NOT NULL,
		installed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_skills_user ON skills(user_id);
	CREATE INDEX IF NOT EXISTS idx_skills_name ON skills(name);`

	_, err := db.conn.Exec(query)
	return err
}

// --- Skills CRUD ---

// SaveSkill 保存或更新一个 Skill 记录
func (db *DB) SaveSkill(r *SkillRecord) error {
	query := `
	INSERT INTO skills (id, name, description, version, source, source_url, manifest_json, instructions, has_scripts, required_secrets, allowed_tools, user_id, installed_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		name = excluded.name,
		description = excluded.description,
		version = excluded.version,
		source = excluded.source,
		source_url = excluded.source_url,
		manifest_json = excluded.manifest_json,
		instructions = excluded.instructions,
		has_scripts = excluded.has_scripts,
		required_secrets = excluded.required_secrets,
		allowed_tools = excluded.allowed_tools,
		updated_at = excluded.updated_at`

	now := time.Now().Format("2006-01-02 15:04:05")
	installedAt := r.InstalledAt
	if installedAt == "" {
		installedAt = now
	}

	hasScripts := 0
	if r.HasScripts {
		hasScripts = 1
	}

	_, err := db.conn.Exec(query,
		r.ID, r.Name, r.Description, r.Version,
		r.Source, r.SourceURL,
		r.ManifestJSON, r.Instructions,
		hasScripts, r.RequiredSecrets, r.AllowedTools,
		r.UserID,
		installedAt, now,
	)
	return err
}

// GetSkill 获取指定 ID 的 Skill
func (db *DB) GetSkill(id string) (*SkillRecord, error) {
	query := `SELECT id, name, description, version, source, COALESCE(source_url,''), manifest_json, instructions, has_scripts, COALESCE(required_secrets,'[]'), COALESCE(allowed_tools,'[]'), user_id, installed_at, updated_at
	FROM skills WHERE id = ?`

	row := db.conn.QueryRow(query, id)
	return scanSkillRecord(row)
}

// GetSkillByName 根据名称获取 Skill（某用户的）
func (db *DB) GetSkillByName(userID, name string) (*SkillRecord, error) {
	query := `SELECT id, name, description, version, source, COALESCE(source_url,''), manifest_json, instructions, has_scripts, COALESCE(required_secrets,'[]'), COALESCE(allowed_tools,'[]'), user_id, installed_at, updated_at
	FROM skills WHERE user_id = ? AND name = ?`

	row := db.conn.QueryRow(query, userID, name)
	return scanSkillRecord(row)
}

// ListSkills 列出某用户安装的所有 Skills
func (db *DB) ListSkills(userID string) ([]*SkillRecord, error) {
	query := `SELECT id, name, description, version, source, COALESCE(source_url,''), manifest_json, instructions, has_scripts, COALESCE(required_secrets,'[]'), COALESCE(allowed_tools,'[]'), user_id, installed_at, updated_at
	FROM skills WHERE user_id = ? ORDER BY installed_at DESC`

	rows, err := db.conn.Query(query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanSkillRecords(rows)
}

// ListAllSkills 列出所有用户的 Skills (管理员用)
func (db *DB) ListAllSkills() ([]*SkillRecord, error) {
	query := `SELECT id, name, description, version, source, COALESCE(source_url,''), manifest_json, instructions, has_scripts, COALESCE(required_secrets,'[]'), COALESCE(allowed_tools,'[]'), user_id, installed_at, updated_at
	FROM skills ORDER BY installed_at DESC`

	rows, err := db.conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanSkillRecords(rows)
}

// SearchSkills 按名称或描述搜索 Skills
func (db *DB) SearchSkills(userID, keyword string) ([]*SkillRecord, error) {
	pattern := "%" + strings.ToLower(keyword) + "%"
	query := `SELECT id, name, description, version, source, COALESCE(source_url,''), manifest_json, instructions, has_scripts, COALESCE(required_secrets,'[]'), COALESCE(allowed_tools,'[]'), user_id, installed_at, updated_at
	FROM skills WHERE user_id = ? AND (LOWER(name) LIKE ? OR LOWER(description) LIKE ?)
	ORDER BY installed_at DESC`

	rows, err := db.conn.Query(query, userID, pattern, pattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanSkillRecords(rows)
}

// DeleteSkill 删除指定 Skill
func (db *DB) DeleteSkill(id, userID string) error {
	query := `DELETE FROM skills WHERE id = ? AND user_id = ?`
	result, err := db.conn.Exec(query, id, userID)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("skill not found or not owned by user")
	}
	return nil
}

// CountSkills 统计用户安装的技能数量
func (db *DB) CountSkills(userID string) (int, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM skills WHERE user_id = ?", userID).Scan(&count)
	return count, err
}

// --- 内部扫描函数 ---

func scanSkillRecord(row *sql.Row) (*SkillRecord, error) {
	var r SkillRecord
	var hasScripts int

	err := row.Scan(
		&r.ID, &r.Name, &r.Description, &r.Version,
		&r.Source, &r.SourceURL,
		&r.ManifestJSON, &r.Instructions,
		&hasScripts, &r.RequiredSecrets, &r.AllowedTools,
		&r.UserID,
		&r.InstalledAt, &r.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	r.HasScripts = hasScripts == 1
	return &r, nil
}

func scanSkillRecords(rows *sql.Rows) ([]*SkillRecord, error) {
	var records []*SkillRecord
	for rows.Next() {
		var r SkillRecord
		var hasScripts int

		if err := rows.Scan(
			&r.ID, &r.Name, &r.Description, &r.Version,
			&r.Source, &r.SourceURL,
			&r.ManifestJSON, &r.Instructions,
			&hasScripts, &r.RequiredSecrets, &r.AllowedTools,
			&r.UserID,
			&r.InstalledAt, &r.UpdatedAt,
		); err != nil {
			continue
		}

		r.HasScripts = hasScripts == 1
		records = append(records, &r)
	}
	return records, nil
}
