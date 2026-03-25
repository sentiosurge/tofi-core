package storage

import (
	"database/sql"
	"log"
)

// ChatSessionIndex is the SQLite index record for a chat session.
// The actual message content lives in XML files; this is for fast listing/searching.
type ChatSessionIndex struct {
	ID                string  `json:"id"`
	UserID            string  `json:"user_id"`
	Scope             string  `json:"scope"`              // "" = user main chat, "agent:{name}" = agent chat
	Title             string  `json:"title"`
	Model             string  `json:"model"`
	MessageCount      int     `json:"message_count"`
	TotalInputTokens  int64   `json:"total_input_tokens"`
	TotalOutputTokens int64   `json:"total_output_tokens"`
	TotalCost         float64 `json:"total_cost"`
	FilePath          string  `json:"-"`                  // internal, not exposed in API
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
}

func (db *DB) initChatSessionsTable() error {
	query := `
	CREATE TABLE IF NOT EXISTS chat_sessions (
		id                  TEXT PRIMARY KEY,
		user_id             TEXT NOT NULL,
		scope               TEXT DEFAULT '',
		title               TEXT DEFAULT '',
		model               TEXT DEFAULT '',
		message_count       INTEGER DEFAULT 0,
		total_input_tokens  INTEGER DEFAULT 0,
		total_output_tokens INTEGER DEFAULT 0,
		total_cost          REAL DEFAULT 0,
		file_path           TEXT NOT NULL,
		created_at          DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at          DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_chat_sessions_user_scope
		ON chat_sessions(user_id, scope, updated_at DESC);
	CREATE INDEX IF NOT EXISTS idx_chat_sessions_created
		ON chat_sessions(created_at);`
	_, err := db.conn.Exec(query)
	return err
}

// UpsertChatSessionIndex inserts or updates a session index record.
func (db *DB) UpsertChatSessionIndex(s *ChatSessionIndex) error {
	query := `
	INSERT INTO chat_sessions (id, user_id, scope, title, model, message_count,
		total_input_tokens, total_output_tokens, total_cost, file_path, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		title = excluded.title,
		model = excluded.model,
		message_count = excluded.message_count,
		total_input_tokens = excluded.total_input_tokens,
		total_output_tokens = excluded.total_output_tokens,
		total_cost = excluded.total_cost,
		updated_at = excluded.updated_at`
	_, err := db.conn.Exec(query,
		s.ID, s.UserID, s.Scope, s.Title, s.Model, s.MessageCount,
		s.TotalInputTokens, s.TotalOutputTokens, s.TotalCost,
		s.FilePath, s.CreatedAt, s.UpdatedAt)
	return err
}

// ListChatSessions returns session index records for a user, optionally filtered by scope.
func (db *DB) ListChatSessions(userID, scope string, limit int) ([]*ChatSessionIndex, error) {
	var rows *sql.Rows
	var err error

	if scope == "*" {
		// All scopes
		rows, err = db.conn.Query(
			`SELECT id, user_id, scope, title, model, message_count,
				total_input_tokens, total_output_tokens, total_cost,
				file_path, created_at, updated_at
			FROM chat_sessions WHERE user_id = ?
			ORDER BY updated_at DESC LIMIT ?`, userID, limit)
	} else {
		rows, err = db.conn.Query(
			`SELECT id, user_id, scope, title, model, message_count,
				total_input_tokens, total_output_tokens, total_cost,
				file_path, created_at, updated_at
			FROM chat_sessions WHERE user_id = ? AND scope = ?
			ORDER BY updated_at DESC LIMIT ?`, userID, scope, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*ChatSessionIndex
	for rows.Next() {
		s := &ChatSessionIndex{}
		if err := rows.Scan(&s.ID, &s.UserID, &s.Scope, &s.Title, &s.Model,
			&s.MessageCount, &s.TotalInputTokens, &s.TotalOutputTokens,
			&s.TotalCost, &s.FilePath, &s.CreatedAt, &s.UpdatedAt); err != nil {
			log.Printf("⚠️  scan chat session: %v", err)
			continue
		}
		results = append(results, s)
	}
	return results, nil
}

// GetChatSessionIndex returns a single session index record.
func (db *DB) GetChatSessionIndex(id string) (*ChatSessionIndex, error) {
	s := &ChatSessionIndex{}
	err := db.conn.QueryRow(
		`SELECT id, user_id, scope, title, model, message_count,
			total_input_tokens, total_output_tokens, total_cost,
			file_path, created_at, updated_at
		FROM chat_sessions WHERE id = ?`, id).Scan(
		&s.ID, &s.UserID, &s.Scope, &s.Title, &s.Model,
		&s.MessageCount, &s.TotalInputTokens, &s.TotalOutputTokens,
		&s.TotalCost, &s.FilePath, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// DeleteChatSessionIndex removes a session index record.
func (db *DB) DeleteChatSessionIndex(id string) error {
	_, err := db.conn.Exec(`DELETE FROM chat_sessions WHERE id = ?`, id)
	return err
}

// ModelUsage holds aggregated usage statistics for a single model.
type ModelUsage struct {
	Model        string  `json:"model"`
	Sessions     int     `json:"sessions"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalCost    float64 `json:"total_cost"`
}

// GetUsageByModel returns usage statistics grouped by model.
// userID="" means all users. startDate/endDate="" means no date filter.
func (db *DB) GetUsageByModel(userID, startDate, endDate string) ([]ModelUsage, error) {
	var query string
	var args []any

	if startDate != "" && endDate != "" && userID != "" {
		query = `SELECT COALESCE(NULLIF(model, ''), '(unknown)') as model,
			COUNT(*) as sessions,
			COALESCE(SUM(total_input_tokens), 0) as input_tokens,
			COALESCE(SUM(total_output_tokens), 0) as output_tokens,
			COALESCE(SUM(total_cost), 0) as total_cost
			FROM chat_sessions
			WHERE created_at >= ? AND created_at < ? AND user_id = ?
			GROUP BY model ORDER BY total_cost DESC`
		args = []any{startDate, endDate, userID}
	} else if startDate != "" && endDate != "" {
		query = `SELECT COALESCE(NULLIF(model, ''), '(unknown)') as model,
			COUNT(*) as sessions,
			COALESCE(SUM(total_input_tokens), 0) as input_tokens,
			COALESCE(SUM(total_output_tokens), 0) as output_tokens,
			COALESCE(SUM(total_cost), 0) as total_cost
			FROM chat_sessions
			WHERE created_at >= ? AND created_at < ?
			GROUP BY model ORDER BY total_cost DESC`
		args = []any{startDate, endDate}
	} else if userID != "" {
		query = `SELECT COALESCE(NULLIF(model, ''), '(unknown)') as model,
			COUNT(*) as sessions,
			COALESCE(SUM(total_input_tokens), 0) as input_tokens,
			COALESCE(SUM(total_output_tokens), 0) as output_tokens,
			COALESCE(SUM(total_cost), 0) as total_cost
			FROM chat_sessions
			WHERE user_id = ?
			GROUP BY model ORDER BY total_cost DESC`
		args = []any{userID}
	} else {
		query = `SELECT COALESCE(NULLIF(model, ''), '(unknown)') as model,
			COUNT(*) as sessions,
			COALESCE(SUM(total_input_tokens), 0) as input_tokens,
			COALESCE(SUM(total_output_tokens), 0) as output_tokens,
			COALESCE(SUM(total_cost), 0) as total_cost
			FROM chat_sessions
			GROUP BY model ORDER BY total_cost DESC`
	}

	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ModelUsage
	for rows.Next() {
		var m ModelUsage
		if err := rows.Scan(&m.Model, &m.Sessions, &m.InputTokens, &m.OutputTokens, &m.TotalCost); err != nil {
			continue
		}
		results = append(results, m)
	}
	return results, nil
}
