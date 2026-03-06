package storage

import (
	"database/sql"
	"log"
	"path/filepath"
	"tofi-core/internal/models"

	_ "modernc.org/sqlite"
)

type ExecutionRecord struct {
	ID           string         `json:"id"`
	WorkflowID   string         `json:"workflow_id"`
	WorkflowName string         `json:"workflow_name"`
	User         string         `json:"user"`
	Status       string         `json:"status"`
	StateJSON    string         `json:"state_json"`
	ResultJSON   string         `json:"result_json"`
	CreatedAt    sql.NullString `json:"created_at"`
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
		workflow_id TEXT,
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

	// Migration: Ensure workflow_id exists
	// We ignore error "duplicate column name"
	conn.Exec("ALTER TABLE executions ADD COLUMN workflow_id TEXT")

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

	// 创建 user_files 表 (Global File Library)
	filesQuery := `
	CREATE TABLE IF NOT EXISTS user_files (
		uuid TEXT PRIMARY KEY,
		file_id TEXT NOT NULL,
		user TEXT NOT NULL,
		original_filename TEXT,
		mime_type TEXT,
		size_bytes INTEGER,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		hash TEXT,
		UNIQUE(user, file_id)
	);`
	if _, err := conn.Exec(filesQuery); err != nil {
		return nil, err
	}

	// Migration: Add new columns for File ID system
	conn.Exec("ALTER TABLE user_files ADD COLUMN workflow_id TEXT")
	conn.Exec("ALTER TABLE user_files ADD COLUMN node_id TEXT")
	conn.Exec("ALTER TABLE user_files ADD COLUMN source TEXT DEFAULT 'library'")

	// 创建 execution_artifacts 表
	artifactsQuery := `
	CREATE TABLE IF NOT EXISTS execution_artifacts (
		id TEXT PRIMARY KEY,
		execution_id TEXT NOT NULL,
		filename TEXT NOT NULL,
		size_bytes INTEGER,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_artifacts_exec ON execution_artifacts(execution_id);`
	if _, err := conn.Exec(artifactsQuery); err != nil {
		return nil, err
	}

	// Migration: add mime_type and relative_path to execution_artifacts
	conn.Exec("ALTER TABLE execution_artifacts ADD COLUMN mime_type TEXT")
	conn.Exec("ALTER TABLE execution_artifacts ADD COLUMN relative_path TEXT")

	// 创建 webhooks 表
	webhooksQuery := `
	CREATE TABLE IF NOT EXISTS webhooks (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		workflow_id TEXT NOT NULL,
		token TEXT UNIQUE NOT NULL,
		secret TEXT,
		active INTEGER DEFAULT 1,
		description TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_webhooks_token ON webhooks(token);
	CREATE INDEX IF NOT EXISTS idx_webhooks_user ON webhooks(user_id);`
	if _, err := conn.Exec(webhooksQuery); err != nil {
		log.Printf("⚠️  webhooks table creation (may already exist): %v", err)
	}

	// 创建 cron_triggers 表
	cronQuery := `
	CREATE TABLE IF NOT EXISTS cron_triggers (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		workflow_id TEXT NOT NULL,
		expression TEXT NOT NULL,
		timezone TEXT DEFAULT 'UTC',
		active INTEGER DEFAULT 1,
		description TEXT,
		last_executed DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_cron_user ON cron_triggers(user_id);`
	if _, err := conn.Exec(cronQuery); err != nil {
		log.Printf("⚠️  cron_triggers table creation (may already exist): %v", err)
	}

	db := &DB{conn: conn}

	// 创建 skills 表
	if err := db.initSkillsTable(); err != nil {
		log.Printf("⚠️  skills table creation (may already exist): %v", err)
	}
	db.migrateSkillsTable() // 添加新字段（scope, input_schema, output_schema）

	// 创建 settings 表 (AI Key 管理等)
	if err := db.initSettingsTable(); err != nil {
		log.Printf("⚠️  settings table creation (may already exist): %v", err)
	}

	// 创建 kanban_cards 表
	if err := db.initKanbanTable(); err != nil {
		log.Printf("⚠️  kanban_cards table creation (may already exist): %v", err)
	}

	return db, nil
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
	query := `SELECT id, workflow_id, workflow_name, user, status, state_json, result_json, created_at FROM executions ORDER BY created_at DESC LIMIT ? OFFSET ?`
	rows, err := db.conn.Query(query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*ExecutionRecord
	for rows.Next() {
		var r ExecutionRecord
		if err := rows.Scan(&r.ID, &r.WorkflowID, &r.WorkflowName, &r.User, &r.Status, &r.StateJSON, &r.ResultJSON, &r.CreatedAt); err != nil {
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
func (db *DB) SaveExecution(id, workflowID, name, user, status, stateJSON, resultJSON string) error {
	query := `
	INSERT OR REPLACE INTO executions (id, workflow_id, workflow_name, user, status, state_json, result_json, created_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, (SELECT created_at FROM executions WHERE id = ? OR CURRENT_TIMESTAMP));`

	// 注意：SQLite 的 REPLACE 会导致 created_at 丢失，所以我们用一个小技巧保留它
	_, err := db.conn.Exec(query, id, workflowID, name, user, status, stateJSON, resultJSON, id)
	return err
}

func (db *DB) GetExecution(id string) (*ExecutionRecord, error) {
	row := db.conn.QueryRow("SELECT id, workflow_id, workflow_name, user, status, state_json, result_json, created_at FROM executions WHERE id = ?", id)
	var r ExecutionRecord
	err := row.Scan(&r.ID, &r.WorkflowID, &r.WorkflowName, &r.User, &r.Status, &r.StateJSON, &r.ResultJSON, &r.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (db *DB) ListExecutions(user string, limit, offset int) ([]*ExecutionRecord, error) {
	query := `SELECT id, workflow_id, workflow_name, user, status, state_json, result_json, created_at FROM executions WHERE user = ? ORDER BY created_at DESC LIMIT ? OFFSET ?`
	rows, err := db.conn.Query(query, user, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*ExecutionRecord
	for rows.Next() {
		var r ExecutionRecord
		if err := rows.Scan(&r.ID, &r.WorkflowID, &r.WorkflowName, &r.User, &r.Status, &r.StateJSON, &r.ResultJSON, &r.CreatedAt); err != nil {
			continue
		}
		records = append(records, &r)
	}
	return records, nil
}

func (db *DB) ListExecutionsByWorkflow(user, workflowID string, limit int) ([]*ExecutionRecord, error) {
	// Try matching by ID first, then Name (for backward compatibility or if ID is name)
	query := `SELECT id, workflow_id, workflow_name, user, status, state_json, result_json, created_at 
	          FROM executions 
	          WHERE user = ? AND (workflow_id = ? OR workflow_name = ?) 
	          ORDER BY created_at DESC LIMIT ?`

	rows, err := db.conn.Query(query, user, workflowID, workflowID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*ExecutionRecord
	for rows.Next() {
		var r ExecutionRecord
		if err := rows.Scan(&r.ID, &r.WorkflowID, &r.WorkflowName, &r.User, &r.Status, &r.StateJSON, &r.ResultJSON, &r.CreatedAt); err != nil {
			continue
		}
		records = append(records, &r)
	}
	return records, nil
}

// CancelRunningExecutions 将指定工作流的所有正在运行的任务标记为 CANCELLED
func (db *DB) CancelRunningExecutions(user, workflowID string) error {
	// Match by ID or Name to catch all variants
	query := `UPDATE executions SET status = 'CANCELLED' WHERE user = ? AND (workflow_id = ? OR workflow_name = ?) AND status = 'RUNNING'`
	_, err := db.conn.Exec(query, user, workflowID, workflowID)
	return err
}

func (db *DB) ListRunningExecutions() ([]*ExecutionRecord, error) {
	rows, err := db.conn.Query("SELECT id, workflow_id, workflow_name, user, status, state_json, result_json, created_at FROM executions WHERE status = 'RUNNING'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*ExecutionRecord
	for rows.Next() {
		var r ExecutionRecord
		if err := rows.Scan(&r.ID, &r.WorkflowID, &r.WorkflowName, &r.User, &r.Status, &r.StateJSON, &r.ResultJSON, &r.CreatedAt); err != nil {
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

// --- File Handling ---

func (db *DB) SaveUserFile(uuid, fileID, user, originalFilename, mimeType string, size int64, hash string) error {
	query := `INSERT INTO user_files (uuid, file_id, user, original_filename, mime_type, size_bytes, hash, source) VALUES (?, ?, ?, ?, ?, ?, ?, 'library')`
	_, err := db.conn.Exec(query, uuid, fileID, user, originalFilename, mimeType, size, hash)
	return err
}

// SaveWorkflowFile saves a file uploaded to a specific workflow
func (db *DB) SaveWorkflowFile(uuid, fileID, user, originalFilename, mimeType string, size int64, hash, workflowID, nodeID string) error {
	query := `INSERT INTO user_files (uuid, file_id, user, original_filename, mime_type, size_bytes, hash, workflow_id, node_id, source)
	          VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'workflow')`
	_, err := db.conn.Exec(query, uuid, fileID, user, originalFilename, mimeType, size, hash, workflowID, nodeID)
	return err
}

func (db *DB) GetUserFileID(user, fileID string) (*models.UserFileRecord, error) {
	query := `SELECT uuid, file_id, user, original_filename, mime_type, size_bytes, created_at, hash,
	          COALESCE(workflow_id,''), COALESCE(node_id,''), COALESCE(source,'library')
	          FROM user_files WHERE user = ? AND file_id = ?`
	row := db.conn.QueryRow(query, user, fileID)
	var r models.UserFileRecord
	err := row.Scan(&r.UUID, &r.FileID, &r.User, &r.OriginalFilename, &r.MimeType, &r.SizeBytes, &r.CreatedAt, &r.Hash,
		&r.WorkflowID, &r.NodeID, &r.Source)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (db *DB) ListUserFiles(user string) ([]*models.UserFileRecord, error) {
	query := `SELECT uuid, file_id, user, original_filename, mime_type, size_bytes, created_at, hash,
	          COALESCE(workflow_id,''), COALESCE(node_id,''), COALESCE(source,'library')
	          FROM user_files WHERE user = ? ORDER BY created_at DESC`
	rows, err := db.conn.Query(query, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*models.UserFileRecord
	for rows.Next() {
		var r models.UserFileRecord
		if err := rows.Scan(&r.UUID, &r.FileID, &r.User, &r.OriginalFilename, &r.MimeType, &r.SizeBytes, &r.CreatedAt, &r.Hash,
			&r.WorkflowID, &r.NodeID, &r.Source); err != nil {
			continue
		}
		records = append(records, &r)
	}
	return records, nil
}

// ListWorkflowFiles lists all files belonging to a specific workflow
func (db *DB) ListWorkflowFiles(user, workflowID string) ([]*models.UserFileRecord, error) {
	query := `SELECT uuid, file_id, user, original_filename, mime_type, size_bytes, created_at, hash,
	          COALESCE(workflow_id,''), COALESCE(node_id,''), COALESCE(source,'library')
	          FROM user_files WHERE user = ? AND workflow_id = ? ORDER BY created_at DESC`
	rows, err := db.conn.Query(query, user, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*models.UserFileRecord
	for rows.Next() {
		var r models.UserFileRecord
		if err := rows.Scan(&r.UUID, &r.FileID, &r.User, &r.OriginalFilename, &r.MimeType, &r.SizeBytes, &r.CreatedAt, &r.Hash,
			&r.WorkflowID, &r.NodeID, &r.Source); err != nil {
			continue
		}
		records = append(records, &r)
	}
	return records, nil
}

func (db *DB) GetUserFile(user, fileID string) (*models.UserFileRecord, error) {
	query := `SELECT uuid, file_id, user, original_filename, mime_type, size_bytes, created_at, hash,
	          COALESCE(workflow_id,''), COALESCE(node_id,''), COALESCE(source,'library')
	          FROM user_files WHERE user = ? AND file_id = ?`
	var r models.UserFileRecord
	err := db.conn.QueryRow(query, user, fileID).Scan(&r.UUID, &r.FileID, &r.User, &r.OriginalFilename, &r.MimeType, &r.SizeBytes, &r.CreatedAt, &r.Hash,
		&r.WorkflowID, &r.NodeID, &r.Source)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (db *DB) DeleteUserFile(user, fileID string) error {
	// Try deleting by file_id first
	query := `DELETE FROM user_files WHERE user = ? AND file_id = ?`
	res, err := db.conn.Exec(query, user, fileID)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows > 0 {
		return nil
	}

	// If no rows affected, try deleting by UUID (in case input is UUID)
	query = `DELETE FROM user_files WHERE user = ? AND uuid = ?`
	res, err = db.conn.Exec(query, user, fileID)
	if err != nil {
		return err
	}
	rows, _ = res.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (db *DB) GetUserTotalFileSize(user string) (int64, error) {
	query := `SELECT COALESCE(SUM(size_bytes), 0) FROM user_files WHERE user = ?`
	var size int64
	err := db.conn.QueryRow(query, user).Scan(&size)
	return size, err
}

func (db *DB) CheckFileIDExists(user, fileID string) (bool, error) {
	query := `SELECT 1 FROM user_files WHERE user = ? AND file_id = ?`
	var exists int
	err := db.conn.QueryRow(query, user, fileID).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// Artifacts

func (db *DB) RecordArtifact(execID, filename, relativePath, mimeType string, size int64) error {
	id := execID + "_" + filename // Simple deterministic ID
	query := `INSERT OR REPLACE INTO execution_artifacts (id, execution_id, filename, relative_path, mime_type, size_bytes) VALUES (?, ?, ?, ?, ?, ?)`
	_, err := db.conn.Exec(query, id, execID, filename, relativePath, mimeType, size)
	return err
}

func (db *DB) ListExecutionArtifacts(executionID string) ([]*models.ArtifactRecord, error) {
	query := `SELECT id, execution_id, filename, COALESCE(relative_path,''), COALESCE(mime_type,''), size_bytes, created_at
	FROM execution_artifacts WHERE execution_id = ? ORDER BY filename`

	rows, err := db.conn.Query(query, executionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*models.ArtifactRecord
	for rows.Next() {
		var r models.ArtifactRecord
		if err := rows.Scan(&r.ID, &r.ExecutionID, &r.Filename, &r.RelativePath, &r.MimeType, &r.SizeBytes, &r.CreatedAt); err != nil {
			continue
		}
		records = append(records, &r)
	}
	return records, nil
}

func (db *DB) ListAllArtifacts(user string, limit, offset int) ([]*models.ArtifactRecord, error) {
	query := `
	SELECT a.id, a.execution_id, e.workflow_name, a.filename, COALESCE(a.relative_path,''), COALESCE(a.mime_type,''), a.size_bytes, a.created_at
	FROM execution_artifacts a
	JOIN executions e ON a.execution_id = e.id
	WHERE e.user = ?
	ORDER BY a.created_at DESC
	LIMIT ? OFFSET ?`

	rows, err := db.conn.Query(query, user, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*models.ArtifactRecord
	for rows.Next() {
		var r models.ArtifactRecord
		var wfName sql.NullString
		if err := rows.Scan(&r.ID, &r.ExecutionID, &wfName, &r.Filename, &r.RelativePath, &r.MimeType, &r.SizeBytes, &r.CreatedAt); err != nil {
			continue
		}
		r.WorkflowName = wfName.String
		records = append(records, &r)
	}
	return records, nil
}

// --- Webhook Management ---

type WebhookRecord struct {
	ID          string         `json:"id"`
	UserID      string         `json:"user_id"`
	WorkflowID  string         `json:"workflow_id"`
	Token       string         `json:"token"`
	Secret      string         `json:"secret,omitempty"`
	Active      bool           `json:"active"`
	Description string         `json:"description,omitempty"`
	CreatedAt   sql.NullString `json:"created_at"`
}

func (db *DB) CreateWebhook(id, userID, workflowID, token, secret, description string) error {
	query := `INSERT INTO webhooks (id, user_id, workflow_id, token, secret, description) VALUES (?, ?, ?, ?, ?, ?)`
	_, err := db.conn.Exec(query, id, userID, workflowID, token, secret, description)
	return err
}

func (db *DB) GetWebhookByToken(token string) (*WebhookRecord, error) {
	query := `SELECT id, user_id, workflow_id, token, secret, active, description, created_at FROM webhooks WHERE token = ? AND active = 1`
	row := db.conn.QueryRow(query, token)
	var w WebhookRecord
	var active int
	err := row.Scan(&w.ID, &w.UserID, &w.WorkflowID, &w.Token, &w.Secret, &active, &w.Description, &w.CreatedAt)
	if err != nil {
		return nil, err
	}
	w.Active = active == 1
	return &w, nil
}

func (db *DB) ListWebhooks(userID string, workflowID string) ([]*WebhookRecord, error) {
	query := `SELECT id, user_id, workflow_id, token, secret, active, description, created_at FROM webhooks WHERE user_id = ?`
	args := []interface{}{userID}
	if workflowID != "" {
		query += ` AND workflow_id = ?`
		args = append(args, workflowID)
	}
	query += ` ORDER BY created_at DESC`

	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*WebhookRecord
	for rows.Next() {
		var w WebhookRecord
		var active int
		if err := rows.Scan(&w.ID, &w.UserID, &w.WorkflowID, &w.Token, &w.Secret, &active, &w.Description, &w.CreatedAt); err != nil {
			continue
		}
		w.Active = active == 1
		records = append(records, &w)
	}
	return records, nil
}

func (db *DB) DeleteWebhook(id, userID string) error {
	query := `DELETE FROM webhooks WHERE id = ? AND user_id = ?`
	_, err := db.conn.Exec(query, id, userID)
	return err
}

func (db *DB) ToggleWebhook(id, userID string, active bool) error {
	activeInt := 0
	if active {
		activeInt = 1
	}
	query := `UPDATE webhooks SET active = ? WHERE id = ? AND user_id = ?`
	_, err := db.conn.Exec(query, activeInt, id, userID)
	return err
}

// --- Cron Trigger Management ---

type CronTriggerRecord struct {
	ID           string         `json:"id"`
	UserID       string         `json:"user_id"`
	WorkflowID   string         `json:"workflow_id"`
	Expression   string         `json:"expression"`
	Timezone     string         `json:"timezone"`
	Active       bool           `json:"active"`
	Description  string         `json:"description,omitempty"`
	LastExecuted sql.NullString `json:"last_executed"`
	CreatedAt    sql.NullString `json:"created_at"`
}

func (db *DB) CreateCronTrigger(id, userID, workflowID, expression, timezone, description string) error {
	query := `INSERT INTO cron_triggers (id, user_id, workflow_id, expression, timezone, description) VALUES (?, ?, ?, ?, ?, ?)`
	_, err := db.conn.Exec(query, id, userID, workflowID, expression, timezone, description)
	return err
}

func (db *DB) ListCronTriggers(userID string, workflowID string) ([]*CronTriggerRecord, error) {
	query := `SELECT id, user_id, workflow_id, expression, timezone, active, description, last_executed, created_at FROM cron_triggers WHERE user_id = ?`
	args := []interface{}{userID}
	if workflowID != "" {
		query += ` AND workflow_id = ?`
		args = append(args, workflowID)
	}
	query += ` ORDER BY created_at DESC`

	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*CronTriggerRecord
	for rows.Next() {
		var c CronTriggerRecord
		var active int
		if err := rows.Scan(&c.ID, &c.UserID, &c.WorkflowID, &c.Expression, &c.Timezone, &active, &c.Description, &c.LastExecuted, &c.CreatedAt); err != nil {
			continue
		}
		c.Active = active == 1
		records = append(records, &c)
	}
	return records, nil
}

func (db *DB) ListActiveCronTriggers() ([]*CronTriggerRecord, error) {
	query := `SELECT id, user_id, workflow_id, expression, timezone, active, description, last_executed, created_at FROM cron_triggers WHERE active = 1`
	rows, err := db.conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*CronTriggerRecord
	for rows.Next() {
		var c CronTriggerRecord
		var active int
		if err := rows.Scan(&c.ID, &c.UserID, &c.WorkflowID, &c.Expression, &c.Timezone, &active, &c.Description, &c.LastExecuted, &c.CreatedAt); err != nil {
			continue
		}
		c.Active = active == 1
		records = append(records, &c)
	}
	return records, nil
}

func (db *DB) UpdateCronTrigger(id, userID, expression, timezone, description string, active bool) error {
	activeInt := 0
	if active {
		activeInt = 1
	}
	query := `UPDATE cron_triggers SET expression = ?, timezone = ?, description = ?, active = ? WHERE id = ? AND user_id = ?`
	_, err := db.conn.Exec(query, expression, timezone, description, activeInt, id, userID)
	return err
}

func (db *DB) UpdateCronLastExecuted(id string) error {
	query := `UPDATE cron_triggers SET last_executed = CURRENT_TIMESTAMP WHERE id = ?`
	_, err := db.conn.Exec(query, id)
	return err
}

func (db *DB) DeleteCronTrigger(id, userID string) error {
	query := `DELETE FROM cron_triggers WHERE id = ? AND user_id = ?`
	_, err := db.conn.Exec(query, id, userID)
	return err
}
