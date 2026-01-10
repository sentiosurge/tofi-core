package storage

import (
	"database/sql"
	"log"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type ExecutionRecord struct {
	ID           string
	WorkflowName string
	User         string
	Status       string
	StateJSON    string // 中间状态
	ResultJSON   string // 最终报告
	CreatedAt    sql.NullString
}

type UserRecord struct {
	ID           string
	Username     string
	PasswordHash string
	Role         string // admin, user
	CreatedAt    sql.NullString
}

type SecretRecord struct {
	ID             string
	User           string
	Name           string
	EncryptedValue string
	CreatedAt      sql.NullString
	UpdatedAt      sql.NullString
}

type DB struct {
	conn *sql.DB
}

func InitDB(homeDir string) (*DB, error) {
	dbPath := filepath.Join(homeDir, "tofi.db")
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	// 创建 users 表
	userQuery := `
	CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		username TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		role TEXT DEFAULT 'user',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`
	if _, err := conn.Exec(userQuery); err != nil {
		return nil, err
	}

	// 创建 executions 表
	query := `
	CREATE TABLE IF NOT EXISTS executions (
		id TEXT PRIMARY KEY,
		workflow_name TEXT,
		user TEXT,
		status TEXT,
		state_json TEXT,
		result_json TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`
	if _, err := conn.Exec(query); err != nil {
		return nil, err
	}

	// 创建 secrets 表
	secretsQuery := `
	CREATE TABLE IF NOT EXISTS secrets (
		id TEXT PRIMARY KEY,
		user TEXT NOT NULL,
		name TEXT NOT NULL,
		encrypted_value TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(user, name)
	);`
	if _, err := conn.Exec(secretsQuery); err != nil {
		return nil, err
	}

	// 创建 logs 表 (结构化日志)
	logsQuery := `
	CREATE TABLE IF NOT EXISTS execution_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		execution_id TEXT NOT NULL,
		node_id TEXT,
		log_type TEXT, -- info, think, tool_call, tool_result, error
		content TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_logs_exec ON execution_logs(execution_id);`
	if _, err := conn.Exec(logsQuery); err != nil {
		return nil, err
	}

	return &DB{conn: conn}, nil
}

// User Management

func (db *DB) SaveUser(id, username, passwordHash, role string) error {
	query := `INSERT INTO users (id, username, password_hash, role) VALUES (?, ?, ?, ?)`
	_, err := db.conn.Exec(query, id, username, passwordHash, role)
	return err
}

func (db *DB) GetUser(username string) (*UserRecord, error) {
	query := `SELECT id, username, password_hash, role, created_at FROM users WHERE username = ?`
	row := db.conn.QueryRow(query, username)
	var u UserRecord
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (db *DB) CountUsers() (int, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	return count, err
}

// Admin: ListAllUsers 返回所有用户
func (db *DB) ListAllUsers() ([]*UserRecord, error) {
	query := `SELECT id, username, password_hash, role, created_at FROM users ORDER BY created_at DESC`
	rows, err := db.conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*UserRecord
	for rows.Next() {
		var u UserRecord
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt); err != nil {
			continue
		}
		records = append(records, &u)
	}
	return records, nil
}

// Admin: DeleteUser 删除用户
func (db *DB) DeleteUser(id string) error {
	query := `DELETE FROM users WHERE id = ?`
	result, err := db.conn.Exec(query, id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Admin: ListAllExecutions 返回所有用户的执行记录
func (db *DB) ListAllExecutions(limit, offset int) ([]*ExecutionRecord, error) {
	query := `SELECT id, workflow_name, user, status, state_json, result_json, created_at FROM executions ORDER BY created_at DESC LIMIT ? OFFSET ?`
	rows, err := db.conn.Query(query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*ExecutionRecord
	for rows.Next() {
		var r ExecutionRecord
		if err := rows.Scan(&r.ID, &r.WorkflowName, &r.User, &r.Status, &r.StateJSON, &r.ResultJSON, &r.CreatedAt); err != nil {
			continue
		}
		records = append(records, &r)
	}
	return records, nil
}

// SystemStats 系统统计数据
type SystemStats struct {
	TotalUsers           int `json:"total_users"`
	TotalExecutions      int `json:"total_executions"`
	SuccessfulExecutions int `json:"successful_executions"`
	FailedExecutions     int `json:"failed_executions"`
	RunningExecutions    int `json:"running_executions"`
}

// Admin: GetSystemStats 返回系统统计数据
func (db *DB) GetSystemStats() (*SystemStats, error) {
	var stats SystemStats

	db.conn.QueryRow("SELECT COUNT(*) FROM users").Scan(&stats.TotalUsers)
	db.conn.QueryRow("SELECT COUNT(*) FROM executions").Scan(&stats.TotalExecutions)
	db.conn.QueryRow("SELECT COUNT(*) FROM executions WHERE status = 'SUCCESS'").Scan(&stats.SuccessfulExecutions)
	db.conn.QueryRow("SELECT COUNT(*) FROM executions WHERE status = 'ERROR'").Scan(&stats.FailedExecutions)
	db.conn.QueryRow("SELECT COUNT(*) FROM executions WHERE status = 'RUNNING'").Scan(&stats.RunningExecutions)

	return &stats, nil
}

// AddLog 插入一条结构化日志
func (db *DB) AddLog(execID, nodeID, logType, content string) error {
	query := `INSERT INTO execution_logs (execution_id, node_id, log_type, content) VALUES (?, ?, ?, ?)`
	_, err := db.conn.Exec(query, execID, nodeID, logType, content)
	return err
}

// GetLogs 获取指定执行的所有日志
func (db *DB) GetLogs(execID string) ([]map[string]interface{}, error) {
	query := `SELECT node_id, log_type, content, created_at FROM execution_logs WHERE execution_id = ? ORDER BY id ASC`
	rows, err := db.conn.Query(query, execID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []map[string]interface{}
	for rows.Next() {
		var nodeID, logType, content, createdAt string
		if err := rows.Scan(&nodeID, &logType, &content, &createdAt); err != nil {
			continue
		}
		logs = append(logs, map[string]interface{}{
			"node_id":    nodeID,
			"type":       logType,
			"content":    content,
			"created_at": createdAt,
		})
	}
	return logs, nil
}

func (db *DB) Close() error {
	return db.conn.Close()
}

// SaveExecution 既可以保存中间状态，也可以保存最终结果 (使用 REPLACE INTO)
func (db *DB) SaveExecution(id, name, user, status, stateJSON, resultJSON string) error {
	query := `
	INSERT OR REPLACE INTO executions (id, workflow_name, user, status, state_json, result_json, created_at)
	VALUES (?, ?, ?, ?, ?, ?, (SELECT created_at FROM executions WHERE id = ? OR CURRENT_TIMESTAMP));`
	
	// 注意：SQLite 的 REPLACE 会导致 created_at 丢失，所以我们用一个小技巧保留它
	_, err := db.conn.Exec(query, id, name, user, status, stateJSON, resultJSON, id)
	return err
}

func (db *DB) GetExecution(id string) (*ExecutionRecord, error) {
	row := db.conn.QueryRow("SELECT id, workflow_name, user, status, state_json, result_json, created_at FROM executions WHERE id = ?", id)
	var r ExecutionRecord
	err := row.Scan(&r.ID, &r.WorkflowName, &r.User, &r.Status, &r.StateJSON, &r.ResultJSON, &r.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (db *DB) ListExecutions(user string, limit, offset int) ([]*ExecutionRecord, error) {
	query := `SELECT id, workflow_name, user, status, state_json, result_json, created_at FROM executions WHERE user = ? ORDER BY created_at DESC LIMIT ? OFFSET ?`
	rows, err := db.conn.Query(query, user, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*ExecutionRecord
	for rows.Next() {
		var r ExecutionRecord
		if err := rows.Scan(&r.ID, &r.WorkflowName, &r.User, &r.Status, &r.StateJSON, &r.ResultJSON, &r.CreatedAt); err != nil {
			continue
		}
		records = append(records, &r)
	}
	return records, nil
}

func (db *DB) ListRunningExecutions() ([]*ExecutionRecord, error) {
	rows, err := db.conn.Query("SELECT id, workflow_name, user, status, state_json, result_json, created_at FROM executions WHERE status = 'RUNNING'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*ExecutionRecord
	for rows.Next() {
		var r ExecutionRecord
		if err := rows.Scan(&r.ID, &r.WorkflowName, &r.User, &r.Status, &r.StateJSON, &r.ResultJSON, &r.CreatedAt); err != nil {
			// 添加调试日志
			log.Printf("⚠️  扫描执行记录失败: %v", err)
			continue
		}
		records = append(records, &r)
	}
	return records, nil
}

// UpdateStatus 更新执行记录的状态
func (db *DB) UpdateStatus(id, status string) error {
	query := `UPDATE executions SET status = ? WHERE id = ?`
	_, err := db.conn.Exec(query, status, id)
	return err
}

// SaveSecret 保存或更新一个 Secret
func (db *DB) SaveSecret(id, user, name, encryptedValue string) error {
	query := `
	INSERT INTO secrets (id, user, name, encrypted_value, created_at, updated_at)
	VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	ON CONFLICT(user, name) DO UPDATE SET
		encrypted_value = excluded.encrypted_value,
		updated_at = CURRENT_TIMESTAMP;`

	_, err := db.conn.Exec(query, id, user, name, encryptedValue)
	return err
}

// GetSecret 获取指定用户的指定 Secret
func (db *DB) GetSecret(user, name string) (*SecretRecord, error) {
	query := `SELECT id, user, name, encrypted_value, created_at, updated_at FROM secrets WHERE user = ? AND name = ?`
	row := db.conn.QueryRow(query, user, name)

	var r SecretRecord
	err := row.Scan(&r.ID, &r.User, &r.Name, &r.EncryptedValue, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ListSecrets 列出指定用户的所有 Secrets（仅返回名称和元数据，不包含加密值）
func (db *DB) ListSecrets(user string) ([]*SecretRecord, error) {
	query := `SELECT id, user, name, created_at, updated_at FROM secrets WHERE user = ?`
	rows, err := db.conn.Query(query, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*SecretRecord
	for rows.Next() {
		var r SecretRecord
		if err := rows.Scan(&r.ID, &r.User, &r.Name, &r.CreatedAt, &r.UpdatedAt); err != nil {
			log.Printf("⚠️  扫描 Secret 记录失败: %v", err)
			continue
		}
		records = append(records, &r)
	}
	return records, nil
}

// DeleteSecret 删除指定用户的指定 Secret
func (db *DB) DeleteSecret(user, name string) error {
	query := `DELETE FROM secrets WHERE user = ? AND name = ?`
	result, err := db.conn.Exec(query, user, name)
	if err != nil {
		return err
	}

	// 检查是否有行被删除
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}

	return nil
}

// Admin: ListAllSecrets 返回所有用户的 secrets 元数据（不含加密值）
func (db *DB) ListAllSecrets() ([]*SecretRecord, error) {
	query := `SELECT id, user, name, created_at, updated_at FROM secrets ORDER BY user, name`
	rows, err := db.conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var secrets []*SecretRecord
	for rows.Next() {
		var r SecretRecord
		if err := rows.Scan(&r.ID, &r.User, &r.Name, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		// 注意：不包含 EncryptedValue
		secrets = append(secrets, &r)
	}
	return secrets, nil
}

// Admin: DeleteSecretByID 通过 ID 删除 secret
func (db *DB) DeleteSecretByID(id string) error {
	query := `DELETE FROM secrets WHERE id = ?`
	result, err := db.conn.Exec(query, id)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}