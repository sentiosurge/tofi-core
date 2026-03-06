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
	ID          string `json:"id"`          // slug: "public/skill-name" 或 "user/skill-name"
	Name        string `json:"name"`        // 从 SKILL.md name 字段
	Description string `json:"description"` // 从 SKILL.md description
	Version     string `json:"version"`     // 版本号

	// 作用域
	Scope string `json:"scope"` // "public" (skills.sh 共享) | "private" (用户私有)

	// 来源信息
	Source    string `json:"source"`               // "local" | "git" | "registry"
	SourceURL string `json:"source_url,omitempty"` // git repo URL 或 registry URL

	// 内容
	ManifestJSON string `json:"manifest_json"` // 完整 frontmatter JSON
	Instructions string `json:"instructions"`  // SKILL.md body (Markdown)

	// Schema
	InputSchema  string `json:"input_schema"`  // JSON: 输入参数定义
	OutputSchema string `json:"output_schema"` // JSON: 输出格式定义

	// 能力标记
	HasScripts      bool   `json:"has_scripts"`      // 是否有 scripts/ 目录
	RequiredSecrets string `json:"required_secrets"`  // JSON array: ["API_KEY", "TOKEN"]
	AllowedTools    string `json:"allowed_tools"`     // JSON array: ["Bash(git:*)"]

	// 所有者
	UserID string `json:"user_id"` // 安装者（public scope 时为 "system"）

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
		scope TEXT NOT NULL DEFAULT 'private',
		source TEXT NOT NULL DEFAULT 'local',
		source_url TEXT,
		manifest_json TEXT NOT NULL,
		instructions TEXT NOT NULL,
		input_schema TEXT DEFAULT '{}',
		output_schema TEXT DEFAULT '{}',
		has_scripts INTEGER DEFAULT 0,
		required_secrets TEXT DEFAULT '[]',
		allowed_tools TEXT DEFAULT '[]',
		user_id TEXT NOT NULL,
		installed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_skills_user ON skills(user_id);
	CREATE INDEX IF NOT EXISTS idx_skills_name ON skills(name);
	CREATE INDEX IF NOT EXISTS idx_skills_scope ON skills(scope);
	CREATE INDEX IF NOT EXISTS idx_skills_source_url ON skills(source_url);`

	_, err := db.conn.Exec(query)
	return err
}

// migrateSkillsTable 为已有 skills 表添加新字段（兼容旧 DB）
func (db *DB) migrateSkillsTable() {
	db.conn.Exec("ALTER TABLE skills ADD COLUMN scope TEXT NOT NULL DEFAULT 'private'")
	db.conn.Exec("ALTER TABLE skills ADD COLUMN input_schema TEXT DEFAULT '{}'")
	db.conn.Exec("ALTER TABLE skills ADD COLUMN output_schema TEXT DEFAULT '{}'")
}

// --- Skills CRUD ---

// SaveSkill 保存或更新一个 Skill 记录
func (db *DB) SaveSkill(r *SkillRecord) error {
	query := `
	INSERT INTO skills (id, name, description, version, scope, source, source_url, manifest_json, instructions, input_schema, output_schema, has_scripts, required_secrets, allowed_tools, user_id, installed_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		name = excluded.name,
		description = excluded.description,
		version = excluded.version,
		scope = excluded.scope,
		source = excluded.source,
		source_url = excluded.source_url,
		manifest_json = excluded.manifest_json,
		instructions = excluded.instructions,
		input_schema = excluded.input_schema,
		output_schema = excluded.output_schema,
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

	scope := r.Scope
	if scope == "" {
		scope = "private"
	}
	inputSchema := r.InputSchema
	if inputSchema == "" {
		inputSchema = "{}"
	}
	outputSchema := r.OutputSchema
	if outputSchema == "" {
		outputSchema = "{}"
	}

	_, err := db.conn.Exec(query,
		r.ID, r.Name, r.Description, r.Version,
		scope, r.Source, r.SourceURL,
		r.ManifestJSON, r.Instructions,
		inputSchema, outputSchema,
		hasScripts, r.RequiredSecrets, r.AllowedTools,
		r.UserID,
		installedAt, now,
	)
	return err
}

// GetSkill 获取指定 ID 的 Skill
func (db *DB) GetSkill(id string) (*SkillRecord, error) {
	query := `SELECT id, name, description, version, COALESCE(scope,'private'), source, COALESCE(source_url,''), manifest_json, instructions, COALESCE(input_schema,'{}'), COALESCE(output_schema,'{}'), has_scripts, COALESCE(required_secrets,'[]'), COALESCE(allowed_tools,'[]'), user_id, installed_at, updated_at
	FROM skills WHERE id = ?`

	row := db.conn.QueryRow(query, id)
	return scanSkillRecord(row)
}

// GetSkillByName 根据名称获取 Skill（某用户的）
func (db *DB) GetSkillByName(userID, name string) (*SkillRecord, error) {
	query := `SELECT id, name, description, version, COALESCE(scope,'private'), source, COALESCE(source_url,''), manifest_json, instructions, COALESCE(input_schema,'{}'), COALESCE(output_schema,'{}'), has_scripts, COALESCE(required_secrets,'[]'), COALESCE(allowed_tools,'[]'), user_id, installed_at, updated_at
	FROM skills WHERE user_id = ? AND name = ?`

	row := db.conn.QueryRow(query, userID, name)
	return scanSkillRecord(row)
}

// ListSkills 列出某用户可见的所有 Skills（用户私有 + 公共 Skills）
// 按 name 去重：同名 Skill 优先显示公共版本
func (db *DB) ListSkills(userID string) ([]*SkillRecord, error) {
	query := `WITH ranked AS (
		SELECT id, name, description, version, COALESCE(scope,'private') AS scope, source, COALESCE(source_url,'') AS source_url, manifest_json, instructions, COALESCE(input_schema,'{}') AS input_schema, COALESCE(output_schema,'{}') AS output_schema, has_scripts, COALESCE(required_secrets,'[]') AS required_secrets, COALESCE(allowed_tools,'[]') AS allowed_tools, user_id, installed_at, updated_at,
		ROW_NUMBER() OVER (PARTITION BY name ORDER BY CASE WHEN scope = 'public' THEN 0 ELSE 1 END, installed_at DESC) AS rn
		FROM skills WHERE user_id = ? OR scope = 'public'
	)
	SELECT id, name, description, version, scope, source, source_url, manifest_json, instructions, input_schema, output_schema, has_scripts, required_secrets, allowed_tools, user_id, installed_at, updated_at
	FROM ranked WHERE rn = 1 ORDER BY installed_at DESC`

	rows, err := db.conn.Query(query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanSkillRecords(rows)
}

// ListAllSkills 列出所有用户的 Skills (管理员用)
func (db *DB) ListAllSkills() ([]*SkillRecord, error) {
	query := `SELECT id, name, description, version, COALESCE(scope,'private'), source, COALESCE(source_url,''), manifest_json, instructions, COALESCE(input_schema,'{}'), COALESCE(output_schema,'{}'), has_scripts, COALESCE(required_secrets,'[]'), COALESCE(allowed_tools,'[]'), user_id, installed_at, updated_at
	FROM skills ORDER BY installed_at DESC`

	rows, err := db.conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanSkillRecords(rows)
}

// SearchSkills 按名称或描述搜索 Skills（用户私有 + 公共，按 name 去重）
func (db *DB) SearchSkills(userID, keyword string) ([]*SkillRecord, error) {
	pattern := "%" + strings.ToLower(keyword) + "%"
	query := `WITH ranked AS (
		SELECT id, name, description, version, COALESCE(scope,'private') AS scope, source, COALESCE(source_url,'') AS source_url, manifest_json, instructions, COALESCE(input_schema,'{}') AS input_schema, COALESCE(output_schema,'{}') AS output_schema, has_scripts, COALESCE(required_secrets,'[]') AS required_secrets, COALESCE(allowed_tools,'[]') AS allowed_tools, user_id, installed_at, updated_at,
		ROW_NUMBER() OVER (PARTITION BY name ORDER BY CASE WHEN scope = 'public' THEN 0 ELSE 1 END, installed_at DESC) AS rn
		FROM skills WHERE (user_id = ? OR scope = 'public') AND (LOWER(name) LIKE ? OR LOWER(description) LIKE ?)
	)
	SELECT id, name, description, version, scope, source, source_url, manifest_json, instructions, input_schema, output_schema, has_scripts, required_secrets, allowed_tools, user_id, installed_at, updated_at
	FROM ranked WHERE rn = 1 ORDER BY installed_at DESC`

	rows, err := db.conn.Query(query, userID, pattern, pattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanSkillRecords(rows)
}

// FindPublicSkillBySource 按 source_url 查找已安装的公共 Skill（去重用）
func (db *DB) FindPublicSkillBySource(sourceURL string) (*SkillRecord, error) {
	query := `SELECT id, name, description, version, COALESCE(scope,'private'), source, COALESCE(source_url,''), manifest_json, instructions, COALESCE(input_schema,'{}'), COALESCE(output_schema,'{}'), has_scripts, COALESCE(required_secrets,'[]'), COALESCE(allowed_tools,'[]'), user_id, installed_at, updated_at
	FROM skills WHERE scope = 'public' AND source_url = ?`

	row := db.conn.QueryRow(query, sourceURL)
	return scanSkillRecord(row)
}

// DeleteSkill 删除指定 Skill（公共 Skill 仅管理员可删除）
func (db *DB) DeleteSkill(id, userID string) error {
	// 公共 Skill 允许任何用户删除（本地引擎，简化权限）
	query := `DELETE FROM skills WHERE id = ? AND (user_id = ? OR scope = 'public')`
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

// ListSkillsBySourceURL 按 source_url 获取同一 collection 的所有 skills
func (db *DB) ListSkillsBySourceURL(sourceURL string) ([]*SkillRecord, error) {
	query := `SELECT id, name, description, version, COALESCE(scope,'private'), source, COALESCE(source_url,''), manifest_json, instructions, COALESCE(input_schema,'{}'), COALESCE(output_schema,'{}'), has_scripts, COALESCE(required_secrets,'[]'), COALESCE(allowed_tools,'[]'), user_id, installed_at, updated_at
	FROM skills WHERE source_url = ? ORDER BY name`

	rows, err := db.conn.Query(query, sourceURL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSkillRecords(rows)
}

// DeleteSkillsBySourceURL 删除同一 source_url 的所有 skills（Collection 卸载）
func (db *DB) DeleteSkillsBySourceURL(sourceURL, userID string) (int, error) {
	query := `DELETE FROM skills WHERE source_url = ? AND (user_id = ? OR scope = 'public')`
	result, err := db.conn.Exec(query, sourceURL, userID)
	if err != nil {
		return 0, err
	}
	rows, _ := result.RowsAffected()
	return int(rows), nil
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
		&r.Scope, &r.Source, &r.SourceURL,
		&r.ManifestJSON, &r.Instructions,
		&r.InputSchema, &r.OutputSchema,
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
			&r.Scope, &r.Source, &r.SourceURL,
			&r.ManifestJSON, &r.Instructions,
			&r.InputSchema, &r.OutputSchema,
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
