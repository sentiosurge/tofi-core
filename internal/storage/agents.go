package storage

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
)

// AgentRecord represents a pre-configured Agent (a "pre-orchestrated Wish")
type AgentRecord struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Prompt           string `json:"prompt"`            // one-line task prompt
	SystemPrompt     string `json:"system_prompt"`     // custom system prompt (empty = default)
	Model            string `json:"model"`             // empty = auto-detect
	Skills           string `json:"skills"`            // JSON array of skill IDs
	ScheduleRules    string `json:"schedule_rules"`    // JSON ScheduleRule
	Capabilities     string `json:"capabilities"`      // JSON: capability config (mcp_servers, web_search, notify, etc.)
	BufferSize       int    `json:"buffer_size"`       // max pending runs
	RenewalThreshold int    `json:"renewal_threshold"` // renew when pending < this
	IsActive         bool   `json:"is_active"`
	UserID           string `json:"user_id"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

// AgentRunRecord represents a single scheduled run (appointment)
type AgentRunRecord struct {
	ID           string `json:"id"`
	AgentID      string `json:"agent_id"`
	ScheduledAt  string `json:"scheduled_at"`
	Status       string `json:"status"` // pending / running / done / failed / cancelled
	KanbanCardID string `json:"kanban_card_id"`
	Result       string `json:"result"`
	UserID       string `json:"user_id"`
	CreatedAt    string `json:"created_at"`
	StartedAt    string `json:"started_at,omitempty"`
	CompletedAt  string `json:"completed_at,omitempty"`
}

func (db *DB) initAgentsTable() error {
	agentsQuery := `
	CREATE TABLE IF NOT EXISTS agents (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT DEFAULT '',
		prompt TEXT DEFAULT '',
		system_prompt TEXT DEFAULT '',
		model TEXT DEFAULT '',
		skills TEXT DEFAULT '[]',
		schedule_rules TEXT DEFAULT '[]',
		buffer_size INTEGER DEFAULT 20,
		renewal_threshold INTEGER DEFAULT 5,
		is_active INTEGER DEFAULT 0,
		user_id TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_agents_user ON agents(user_id);
	`
	if _, err := db.conn.Exec(agentsQuery); err != nil {
		return fmt.Errorf("create agents table: %w", err)
	}

	runsQuery := `
	CREATE TABLE IF NOT EXISTS agent_runs (
		id TEXT PRIMARY KEY,
		agent_id TEXT NOT NULL,
		scheduled_at DATETIME NOT NULL,
		status TEXT DEFAULT 'pending',
		kanban_card_id TEXT DEFAULT '',
		result TEXT DEFAULT '',
		user_id TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		started_at DATETIME,
		completed_at DATETIME,
		FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_agent_runs_pending ON agent_runs(status, scheduled_at);
	CREATE INDEX IF NOT EXISTS idx_agent_runs_agent ON agent_runs(agent_id);
	`
	if _, err := db.conn.Exec(runsQuery); err != nil {
		return fmt.Errorf("create agent_runs table: %w", err)
	}

	// Migration: add capabilities column
	db.conn.Exec("ALTER TABLE agents ADD COLUMN capabilities TEXT DEFAULT '{}'")

	// Enable foreign key support
	if _, err := db.conn.Exec("PRAGMA foreign_keys = ON"); err != nil {
		log.Printf("⚠️  Failed to enable foreign keys: %v", err)
	}

	return nil
}

// ── Agent CRUD ──

func (db *DB) CreateAgent(agent *AgentRecord) error {
	if agent.Capabilities == "" {
		agent.Capabilities = "{}"
	}
	query := `INSERT INTO agents (id, name, description, prompt, system_prompt, model, skills, schedule_rules, capabilities, buffer_size, renewal_threshold, is_active, user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`
	_, err := db.conn.Exec(query,
		agent.ID, agent.Name, agent.Description, agent.Prompt, agent.SystemPrompt,
		agent.Model, agent.Skills, agent.ScheduleRules, agent.Capabilities,
		agent.BufferSize, agent.RenewalThreshold, boolToInt(agent.IsActive), agent.UserID,
	)
	return err
}

func (db *DB) GetAgent(id string) (*AgentRecord, error) {
	query := `SELECT id, name, COALESCE(description,''), COALESCE(prompt,''), COALESCE(system_prompt,''),
		COALESCE(model,''), COALESCE(skills,'[]'), COALESCE(schedule_rules,'[]'), COALESCE(capabilities,'{}'),
		buffer_size, renewal_threshold, is_active, user_id, created_at, updated_at
		FROM agents WHERE id = ?`
	row := db.conn.QueryRow(query, id)
	var a AgentRecord
	var isActive int
	if err := row.Scan(&a.ID, &a.Name, &a.Description, &a.Prompt, &a.SystemPrompt,
		&a.Model, &a.Skills, &a.ScheduleRules, &a.Capabilities,
		&a.BufferSize, &a.RenewalThreshold, &isActive, &a.UserID, &a.CreatedAt, &a.UpdatedAt,
	); err != nil {
		return nil, err
	}
	a.IsActive = isActive != 0
	return &a, nil
}

func (db *DB) ListAgents(userID string) ([]*AgentRecord, error) {
	query := `SELECT id, name, COALESCE(description,''), COALESCE(prompt,''), COALESCE(system_prompt,''),
		COALESCE(model,''), COALESCE(skills,'[]'), COALESCE(schedule_rules,'[]'), COALESCE(capabilities,'{}'),
		buffer_size, renewal_threshold, is_active, user_id, created_at, updated_at
		FROM agents WHERE user_id = ? ORDER BY updated_at DESC`
	rows, err := db.conn.Query(query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []*AgentRecord
	for rows.Next() {
		var a AgentRecord
		var isActive int
		if err := rows.Scan(&a.ID, &a.Name, &a.Description, &a.Prompt, &a.SystemPrompt,
			&a.Model, &a.Skills, &a.ScheduleRules, &a.Capabilities,
			&a.BufferSize, &a.RenewalThreshold, &isActive, &a.UserID, &a.CreatedAt, &a.UpdatedAt,
		); err != nil {
			return nil, err
		}
		a.IsActive = isActive != 0
		agents = append(agents, &a)
	}
	return agents, nil
}

func (db *DB) UpdateAgent(agent *AgentRecord) error {
	if agent.Capabilities == "" {
		agent.Capabilities = "{}"
	}
	query := `UPDATE agents SET name = ?, description = ?, prompt = ?, system_prompt = ?,
		model = ?, skills = ?, schedule_rules = ?, capabilities = ?, buffer_size = ?, renewal_threshold = ?,
		is_active = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND user_id = ?`
	result, err := db.conn.Exec(query,
		agent.Name, agent.Description, agent.Prompt, agent.SystemPrompt,
		agent.Model, agent.Skills, agent.ScheduleRules, agent.Capabilities,
		agent.BufferSize, agent.RenewalThreshold, boolToInt(agent.IsActive),
		agent.ID, agent.UserID,
	)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("agent not found or not owned by user")
	}
	return nil
}

func (db *DB) DeleteAgent(id, userID string) error {
	// Delete agent (agent_runs cascade via FK)
	result, err := db.conn.Exec(`DELETE FROM agents WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("agent not found or not owned by user")
	}
	return nil
}

func (db *DB) SetAgentActive(id, userID string, active bool) error {
	result, err := db.conn.Exec(`UPDATE agents SET is_active = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ?`,
		boolToInt(active), id, userID)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("agent not found or not owned by user")
	}
	return nil
}

func (db *DB) ListActiveAgents() ([]*AgentRecord, error) {
	query := `SELECT id, name, COALESCE(description,''), COALESCE(prompt,''), COALESCE(system_prompt,''),
		COALESCE(model,''), COALESCE(skills,'[]'), COALESCE(schedule_rules,'[]'), COALESCE(capabilities,'{}'),
		buffer_size, renewal_threshold, is_active, user_id, created_at, updated_at
		FROM agents WHERE is_active = 1`
	rows, err := db.conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []*AgentRecord
	for rows.Next() {
		var a AgentRecord
		var isActive int
		if err := rows.Scan(&a.ID, &a.Name, &a.Description, &a.Prompt, &a.SystemPrompt,
			&a.Model, &a.Skills, &a.ScheduleRules, &a.Capabilities,
			&a.BufferSize, &a.RenewalThreshold, &isActive, &a.UserID, &a.CreatedAt, &a.UpdatedAt,
		); err != nil {
			return nil, err
		}
		a.IsActive = isActive != 0
		agents = append(agents, &a)
	}
	return agents, nil
}

// ── Agent Run CRUD ──

func (db *DB) CreateAgentRun(run *AgentRunRecord) error {
	if run.ID == "" {
		run.ID = uuid.New().String()
	}
	query := `INSERT INTO agent_runs (id, agent_id, scheduled_at, status, kanban_card_id, result, user_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`
	_, err := db.conn.Exec(query, run.ID, run.AgentID, run.ScheduledAt, run.Status, run.KanbanCardID, run.Result, run.UserID)
	return err
}

func (db *DB) ListAgentRuns(agentID string, status string, limit int) ([]*AgentRunRecord, error) {
	var query string
	var args []interface{}

	if status != "" {
		query = `SELECT id, agent_id, scheduled_at, status, COALESCE(kanban_card_id,''), COALESCE(result,''),
			user_id, created_at, COALESCE(started_at,''), COALESCE(completed_at,'')
			FROM agent_runs WHERE agent_id = ? AND status = ? ORDER BY scheduled_at ASC LIMIT ?`
		args = []interface{}{agentID, status, limit}
	} else {
		query = `SELECT id, agent_id, scheduled_at, status, COALESCE(kanban_card_id,''), COALESCE(result,''),
			user_id, created_at, COALESCE(started_at,''), COALESCE(completed_at,'')
			FROM agent_runs WHERE agent_id = ? ORDER BY scheduled_at DESC LIMIT ?`
		args = []interface{}{agentID, limit}
	}

	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []*AgentRunRecord
	for rows.Next() {
		var r AgentRunRecord
		if err := rows.Scan(&r.ID, &r.AgentID, &r.ScheduledAt, &r.Status, &r.KanbanCardID, &r.Result,
			&r.UserID, &r.CreatedAt, &r.StartedAt, &r.CompletedAt); err != nil {
			return nil, err
		}
		runs = append(runs, &r)
	}
	return runs, nil
}

// GetAllPendingRuns returns all pending runs (for scheduler startup)
func (db *DB) GetAllPendingRuns() ([]*AgentRunRecord, error) {
	query := `SELECT id, agent_id, scheduled_at, status, COALESCE(kanban_card_id,''), COALESCE(result,''),
		user_id, created_at, COALESCE(started_at,''), COALESCE(completed_at,'')
		FROM agent_runs WHERE status = 'pending' ORDER BY scheduled_at ASC`
	rows, err := db.conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []*AgentRunRecord
	for rows.Next() {
		var r AgentRunRecord
		if err := rows.Scan(&r.ID, &r.AgentID, &r.ScheduledAt, &r.Status, &r.KanbanCardID, &r.Result,
			&r.UserID, &r.CreatedAt, &r.StartedAt, &r.CompletedAt); err != nil {
			return nil, err
		}
		runs = append(runs, &r)
	}
	return runs, nil
}

// GetPendingRunsDue returns pending runs whose scheduled_at <= before
func (db *DB) GetPendingRunsDue(before time.Time) ([]*AgentRunRecord, error) {
	query := `SELECT id, agent_id, scheduled_at, status, COALESCE(kanban_card_id,''), COALESCE(result,''),
		user_id, created_at, COALESCE(started_at,''), COALESCE(completed_at,'')
		FROM agent_runs WHERE status = 'pending' AND scheduled_at <= ? ORDER BY scheduled_at ASC`
	rows, err := db.conn.Query(query, before.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []*AgentRunRecord
	for rows.Next() {
		var r AgentRunRecord
		if err := rows.Scan(&r.ID, &r.AgentID, &r.ScheduledAt, &r.Status, &r.KanbanCardID, &r.Result,
			&r.UserID, &r.CreatedAt, &r.StartedAt, &r.CompletedAt); err != nil {
			return nil, err
		}
		runs = append(runs, &r)
	}
	return runs, nil
}

func (db *DB) CountPendingRuns(agentID string) (int, error) {
	var count int
	err := db.conn.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE agent_id = ? AND status = 'pending'`, agentID).Scan(&count)
	return count, err
}

func (db *DB) UpdateAgentRunStatus(id, status, kanbanCardID string) error {
	var query string
	switch status {
	case "running":
		query = `UPDATE agent_runs SET status = ?, started_at = CURRENT_TIMESTAMP WHERE id = ?`
		_, err := db.conn.Exec(query, status, id)
		return err
	case "done", "failed":
		query = `UPDATE agent_runs SET status = ?, kanban_card_id = ?, completed_at = CURRENT_TIMESTAMP WHERE id = ?`
		_, err := db.conn.Exec(query, status, kanbanCardID, id)
		return err
	default:
		query = `UPDATE agent_runs SET status = ? WHERE id = ?`
		_, err := db.conn.Exec(query, status, id)
		return err
	}
}

// CancelPendingRuns cancels all pending runs for an agent
func (db *DB) CancelPendingRuns(agentID string) (int, error) {
	result, err := db.conn.Exec(`UPDATE agent_runs SET status = 'cancelled' WHERE agent_id = ? AND status = 'pending'`, agentID)
	if err != nil {
		return 0, err
	}
	rows, _ := result.RowsAffected()
	return int(rows), nil
}

// GetLastScheduledTime returns the latest scheduled_at for an agent's pending/done runs
func (db *DB) GetLastScheduledTime(agentID string) (time.Time, error) {
	var ts sql.NullString
	err := db.conn.QueryRow(
		`SELECT MAX(scheduled_at) FROM agent_runs WHERE agent_id = ? AND status IN ('pending','running','done')`,
		agentID,
	).Scan(&ts)
	if err != nil || !ts.Valid {
		return time.Now(), nil
	}
	t, err := time.Parse("2006-01-02 15:04:05", ts.String)
	if err != nil {
		return time.Now(), nil
	}
	return t, nil
}

// DeleteAgentRuns deletes all runs for an agent
func (db *DB) DeleteAgentRuns(agentID string) error {
	_, err := db.conn.Exec(`DELETE FROM agent_runs WHERE agent_id = ?`, agentID)
	return err
}

// helper
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
