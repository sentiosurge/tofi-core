package storage

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
)

// AppRecord represents an AI App (formerly Agent)
type AppRecord struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Prompt           string `json:"prompt"`            // task prompt template (may contain {{params}})
	SystemPrompt     string `json:"system_prompt"`     // custom system prompt
	Model            string `json:"model"`             // empty = auto-detect
	Skills           string `json:"skills"`            // JSON array of skill names
	ScheduleRules    string `json:"schedule_rules"`    // JSON ScheduleRule
	Capabilities     string `json:"capabilities"`      // JSON: capability config
	BufferSize       int    `json:"buffer_size"`       // max pending runs
	RenewalThreshold int    `json:"renewal_threshold"` // renew when pending < this
	IsActive         bool   `json:"is_active"`
	UserID           string `json:"user_id"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`

	// New App fields
	Parameters    string `json:"parameters"`     // JSON: user-filled parameter values
	ParameterDefs string `json:"parameter_defs"` // JSON: parameter definitions from APP.md
	Source        string `json:"source"`          // "local" | "git"
	SourceURL     string `json:"source_url"`      // git repo URL
	Version       string `json:"version"`
	Author        string `json:"author"`
	ManifestJSON  string `json:"manifest_json"`  // full APP.md frontmatter JSON
	HasScripts    bool   `json:"has_scripts"`    // has scripts/ directory
}

// AppRunRecord represents a single run (scheduled or manual)
type AppRunRecord struct {
	ID           string `json:"id"`
	AppID        string `json:"app_id"`
	ScheduledAt  string `json:"scheduled_at"`
	Status       string `json:"status"` // pending / running / done / failed / cancelled / skipped
	Trigger      string `json:"trigger"` // scheduled / manual
	KanbanCardID string `json:"kanban_card_id"`
	SessionID    string `json:"session_id,omitempty"` // chat session ID (new: replaces kanban_card_id)
	Result       string `json:"result"`
	UserID       string `json:"user_id"`
	CreatedAt    string `json:"created_at"`
	StartedAt    string `json:"started_at,omitempty"`
	CompletedAt  string `json:"completed_at,omitempty"`
}

func (db *DB) initAppsTable() error {
	appsQuery := `
	CREATE TABLE IF NOT EXISTS apps (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT DEFAULT '',
		prompt TEXT DEFAULT '',
		system_prompt TEXT DEFAULT '',
		model TEXT DEFAULT '',
		skills TEXT DEFAULT '[]',
		schedule_rules TEXT DEFAULT '[]',
		capabilities TEXT DEFAULT '{}',
		buffer_size INTEGER DEFAULT 20,
		renewal_threshold INTEGER DEFAULT 5,
		is_active INTEGER DEFAULT 0,
		user_id TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		parameters TEXT DEFAULT '{}',
		parameter_defs TEXT DEFAULT '{}',
		source TEXT DEFAULT 'local',
		source_url TEXT DEFAULT '',
		version TEXT DEFAULT '',
		author TEXT DEFAULT '',
		manifest_json TEXT DEFAULT '',
		has_scripts INTEGER DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_apps_user ON apps(user_id);
	`
	if _, err := db.conn.Exec(appsQuery); err != nil {
		return fmt.Errorf("create apps table: %w", err)
	}

	runsQuery := `
	CREATE TABLE IF NOT EXISTS app_runs (
		id TEXT PRIMARY KEY,
		app_id TEXT NOT NULL,
		scheduled_at DATETIME NOT NULL,
		status TEXT DEFAULT 'pending',
		trigger_type TEXT DEFAULT 'scheduled',
		kanban_card_id TEXT DEFAULT '',
		result TEXT DEFAULT '',
		user_id TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		started_at DATETIME,
		completed_at DATETIME,
		FOREIGN KEY (app_id) REFERENCES apps(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_app_runs_pending ON app_runs(status, scheduled_at);
	CREATE INDEX IF NOT EXISTS idx_app_runs_app ON app_runs(app_id);
	`
	if _, err := db.conn.Exec(runsQuery); err != nil {
		return fmt.Errorf("create app_runs table: %w", err)
	}

	// Migration: add trigger_type column for existing databases
	db.conn.Exec(`ALTER TABLE app_runs ADD COLUMN trigger_type TEXT DEFAULT 'scheduled'`)

	// Migration: add session_id column for chat-session-based app runs
	db.conn.Exec(`ALTER TABLE app_runs ADD COLUMN session_id TEXT DEFAULT ''`)

	// Enable foreign key support
	if _, err := db.conn.Exec("PRAGMA foreign_keys = ON"); err != nil {
		log.Printf("Failed to enable foreign keys: %v", err)
	}

	return nil
}

// migrateAgentsToApps copies data from legacy agents/agent_runs tables to apps/app_runs
func (db *DB) migrateAgentsToApps() {
	// Check if agents table exists
	var tableName string
	err := db.conn.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='agents'").Scan(&tableName)
	if err != nil {
		return // No agents table, nothing to migrate
	}

	// Check if already migrated (apps table has data)
	var count int
	db.conn.QueryRow("SELECT COUNT(*) FROM apps").Scan(&count)
	if count > 0 {
		return // Already migrated
	}

	// Copy agents → apps
	_, err = db.conn.Exec(`
		INSERT OR IGNORE INTO apps (id, name, description, prompt, system_prompt, model, skills, schedule_rules, capabilities, buffer_size, renewal_threshold, is_active, user_id, created_at, updated_at)
		SELECT id, name, COALESCE(description,''), COALESCE(prompt,''), COALESCE(system_prompt,''), COALESCE(model,''), COALESCE(skills,'[]'), COALESCE(schedule_rules,'[]'), COALESCE(capabilities,'{}'), buffer_size, renewal_threshold, is_active, user_id, created_at, updated_at
		FROM agents
	`)
	if err != nil {
		log.Printf("Migration agents→apps failed: %v", err)
	} else {
		log.Printf("Migrated agents → apps successfully")
	}

	// Copy agent_runs → app_runs
	var runsTable string
	err = db.conn.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='agent_runs'").Scan(&runsTable)
	if err == nil {
		_, err = db.conn.Exec(`
			INSERT OR IGNORE INTO app_runs (id, app_id, scheduled_at, status, kanban_card_id, result, user_id, created_at, started_at, completed_at)
			SELECT id, agent_id, scheduled_at, status, COALESCE(kanban_card_id,''), COALESCE(result,''), user_id, created_at, started_at, completed_at
			FROM agent_runs
		`)
		if err != nil {
			log.Printf("Migration agent_runs→app_runs failed: %v", err)
		} else {
			log.Printf("Migrated agent_runs → app_runs successfully")
		}
	}
}

// migrateKanbanAppID adds app_id column to kanban_cards and copies from agent_id
func (db *DB) migrateKanbanAppID() {
	db.conn.Exec("ALTER TABLE kanban_cards ADD COLUMN app_id TEXT DEFAULT ''")
	db.conn.Exec("UPDATE kanban_cards SET app_id = agent_id WHERE app_id = '' AND agent_id != ''")
}

// ── App CRUD ──

func (db *DB) CreateApp(app *AppRecord) error {
	if app.Capabilities == "" {
		app.Capabilities = "{}"
	}
	if app.Parameters == "" {
		app.Parameters = "{}"
	}
	if app.ParameterDefs == "" {
		app.ParameterDefs = "{}"
	}
	if app.Source == "" {
		app.Source = "local"
	}
	query := `INSERT INTO apps (id, name, description, prompt, system_prompt, model, skills, schedule_rules, capabilities, buffer_size, renewal_threshold, is_active, user_id, parameters, parameter_defs, source, source_url, version, author, manifest_json, has_scripts, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`
	_, err := db.conn.Exec(query,
		app.ID, app.Name, app.Description, app.Prompt, app.SystemPrompt,
		app.Model, app.Skills, app.ScheduleRules, app.Capabilities,
		app.BufferSize, app.RenewalThreshold, boolToInt(app.IsActive), app.UserID,
		app.Parameters, app.ParameterDefs, app.Source, app.SourceURL,
		app.Version, app.Author, app.ManifestJSON, boolToInt(app.HasScripts),
	)
	return err
}

func (db *DB) GetApp(id string) (*AppRecord, error) {
	query := `SELECT id, name, COALESCE(description,''), COALESCE(prompt,''), COALESCE(system_prompt,''),
		COALESCE(model,''), COALESCE(skills,'[]'), COALESCE(schedule_rules,'[]'), COALESCE(capabilities,'{}'),
		buffer_size, renewal_threshold, is_active, user_id, created_at, updated_at,
		COALESCE(parameters,'{}'), COALESCE(parameter_defs,'{}'),
		COALESCE(source,'local'), COALESCE(source_url,''), COALESCE(version,''), COALESCE(author,''),
		COALESCE(manifest_json,''), COALESCE(has_scripts,0)
		FROM apps WHERE id = ?`
	row := db.conn.QueryRow(query, id)
	return scanAppRecord(row)
}

func (db *DB) ListApps(userID string) ([]*AppRecord, error) {
	query := `SELECT id, name, COALESCE(description,''), COALESCE(prompt,''), COALESCE(system_prompt,''),
		COALESCE(model,''), COALESCE(skills,'[]'), COALESCE(schedule_rules,'[]'), COALESCE(capabilities,'{}'),
		buffer_size, renewal_threshold, is_active, user_id, created_at, updated_at,
		COALESCE(parameters,'{}'), COALESCE(parameter_defs,'{}'),
		COALESCE(source,'local'), COALESCE(source_url,''), COALESCE(version,''), COALESCE(author,''),
		COALESCE(manifest_json,''), COALESCE(has_scripts,0)
		FROM apps WHERE user_id = ? ORDER BY updated_at DESC`
	rows, err := db.conn.Query(query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAppRecords(rows)
}

func (db *DB) UpdateApp(app *AppRecord) error {
	if app.Capabilities == "" {
		app.Capabilities = "{}"
	}
	if app.Parameters == "" {
		app.Parameters = "{}"
	}
	if app.ParameterDefs == "" {
		app.ParameterDefs = "{}"
	}
	query := `UPDATE apps SET name = ?, description = ?, prompt = ?, system_prompt = ?,
		model = ?, skills = ?, schedule_rules = ?, capabilities = ?, buffer_size = ?, renewal_threshold = ?,
		is_active = ?, parameters = ?, parameter_defs = ?, source = ?, source_url = ?,
		version = ?, author = ?, manifest_json = ?, has_scripts = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND user_id = ?`
	result, err := db.conn.Exec(query,
		app.Name, app.Description, app.Prompt, app.SystemPrompt,
		app.Model, app.Skills, app.ScheduleRules, app.Capabilities,
		app.BufferSize, app.RenewalThreshold, boolToInt(app.IsActive),
		app.Parameters, app.ParameterDefs, app.Source, app.SourceURL,
		app.Version, app.Author, app.ManifestJSON, boolToInt(app.HasScripts),
		app.ID, app.UserID,
	)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("app not found or not owned by user")
	}
	return nil
}

func (db *DB) DeleteApp(id, userID string) error {
	result, err := db.conn.Exec(`DELETE FROM apps WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("app not found or not owned by user")
	}
	return nil
}

func (db *DB) SetAppActive(id, userID string, active bool) error {
	result, err := db.conn.Exec(`UPDATE apps SET is_active = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ?`,
		boolToInt(active), id, userID)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("app not found or not owned by user")
	}
	return nil
}

func (db *DB) ListActiveApps() ([]*AppRecord, error) {
	query := `SELECT id, name, COALESCE(description,''), COALESCE(prompt,''), COALESCE(system_prompt,''),
		COALESCE(model,''), COALESCE(skills,'[]'), COALESCE(schedule_rules,'[]'), COALESCE(capabilities,'{}'),
		buffer_size, renewal_threshold, is_active, user_id, created_at, updated_at,
		COALESCE(parameters,'{}'), COALESCE(parameter_defs,'{}'),
		COALESCE(source,'local'), COALESCE(source_url,''), COALESCE(version,''), COALESCE(author,''),
		COALESCE(manifest_json,''), COALESCE(has_scripts,0)
		FROM apps WHERE is_active = 1`
	rows, err := db.conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAppRecords(rows)
}

// ── App Run CRUD ──

func (db *DB) CreateAppRun(run *AppRunRecord) error {
	if run.ID == "" {
		run.ID = uuid.New().String()
	}
	if run.Trigger == "" {
		run.Trigger = "scheduled"
	}
	query := `INSERT INTO app_runs (id, app_id, scheduled_at, status, trigger_type, kanban_card_id, result, user_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`
	_, err := db.conn.Exec(query, run.ID, run.AppID, run.ScheduledAt, run.Status, run.Trigger, run.KanbanCardID, run.Result, run.UserID)
	return err
}

func (db *DB) ListAppRuns(appID string, status string, limit int) ([]*AppRunRecord, error) {
	var query string
	var args []any

	if status != "" {
		query = `SELECT id, app_id, scheduled_at, status, COALESCE(trigger_type,'scheduled'), COALESCE(kanban_card_id,''), COALESCE(session_id,''), COALESCE(result,''),
			user_id, created_at, COALESCE(started_at,''), COALESCE(completed_at,'')
			FROM app_runs WHERE app_id = ? AND status = ? ORDER BY scheduled_at ASC LIMIT ?`
		args = []any{appID, status, limit}
	} else {
		query = `SELECT id, app_id, scheduled_at, status, COALESCE(trigger_type,'scheduled'), COALESCE(kanban_card_id,''), COALESCE(session_id,''), COALESCE(result,''),
			user_id, created_at, COALESCE(started_at,''), COALESCE(completed_at,'')
			FROM app_runs WHERE app_id = ? ORDER BY scheduled_at DESC LIMIT ?`
		args = []any{appID, limit}
	}

	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []*AppRunRecord
	for rows.Next() {
		var r AppRunRecord
		if err := rows.Scan(&r.ID, &r.AppID, &r.ScheduledAt, &r.Status, &r.Trigger, &r.KanbanCardID, &r.SessionID, &r.Result,
			&r.UserID, &r.CreatedAt, &r.StartedAt, &r.CompletedAt); err != nil {
			return nil, err
		}
		runs = append(runs, &r)
	}
	return runs, nil
}

func (db *DB) GetPendingAppRunsDue(before time.Time) ([]*AppRunRecord, error) {
	query := `SELECT r.id, r.app_id, r.scheduled_at, r.status, COALESCE(r.trigger_type,'scheduled'), COALESCE(r.kanban_card_id,''), COALESCE(r.session_id,''), COALESCE(r.result,''),
		r.user_id, r.created_at, COALESCE(r.started_at,''), COALESCE(r.completed_at,'')
		FROM app_runs r JOIN apps a ON r.app_id = a.id
		WHERE r.status = 'pending' AND r.scheduled_at <= ? AND a.is_active = 1
		ORDER BY r.scheduled_at ASC`
	rows, err := db.conn.Query(query, before.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []*AppRunRecord
	for rows.Next() {
		var r AppRunRecord
		if err := rows.Scan(&r.ID, &r.AppID, &r.ScheduledAt, &r.Status, &r.Trigger, &r.KanbanCardID, &r.SessionID, &r.Result,
			&r.UserID, &r.CreatedAt, &r.StartedAt, &r.CompletedAt); err != nil {
			return nil, err
		}
		runs = append(runs, &r)
	}
	return runs, nil
}

func (db *DB) CountPendingAppRuns(appID string) (int, error) {
	var count int
	err := db.conn.QueryRow(`SELECT COUNT(*) FROM app_runs WHERE app_id = ? AND status = 'pending'`, appID).Scan(&count)
	return count, err
}

func (db *DB) UpdateAppRunStatus(id, status, kanbanCardID string) error {
	switch status {
	case "running":
		_, err := db.conn.Exec(`UPDATE app_runs SET status = ?, started_at = CURRENT_TIMESTAMP WHERE id = ?`, status, id)
		return err
	case "done", "failed":
		_, err := db.conn.Exec(`UPDATE app_runs SET status = ?, kanban_card_id = ?, completed_at = CURRENT_TIMESTAMP WHERE id = ?`, status, kanbanCardID, id)
		return err
	default:
		_, err := db.conn.Exec(`UPDATE app_runs SET status = ? WHERE id = ?`, status, id)
		return err
	}
}

// UpdateAppRunStatusWithSession updates an app run's status and links it to a chat session.
func (db *DB) UpdateAppRunStatusWithSession(id, status, sessionID string) error {
	switch status {
	case "running":
		_, err := db.conn.Exec(`UPDATE app_runs SET status = ?, session_id = ?, started_at = CURRENT_TIMESTAMP WHERE id = ?`, status, sessionID, id)
		return err
	case "done", "failed":
		_, err := db.conn.Exec(`UPDATE app_runs SET status = ?, session_id = ?, completed_at = CURRENT_TIMESTAMP WHERE id = ?`, status, sessionID, id)
		return err
	default:
		_, err := db.conn.Exec(`UPDATE app_runs SET status = ?, session_id = ? WHERE id = ?`, status, sessionID, id)
		return err
	}
}

func (db *DB) CancelPendingAppRuns(appID string) (int, error) {
	result, err := db.conn.Exec(`UPDATE app_runs SET status = 'cancelled' WHERE app_id = ? AND status = 'pending'`, appID)
	if err != nil {
		return 0, err
	}
	rows, _ := result.RowsAffected()
	return int(rows), nil
}

func (db *DB) GetLastAppScheduledTime(appID string) (time.Time, error) {
	var ts sql.NullString
	err := db.conn.QueryRow(
		`SELECT MAX(scheduled_at) FROM app_runs WHERE app_id = ? AND status IN ('pending','running','done')`,
		appID,
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

// RecoverRunningAppRuns marks zombie "running" app_runs as "failed" on server restart.
// These runs were interrupted mid-execution — marking as "failed" prevents duplicate execution.
func (db *DB) RecoverRunningAppRuns() (int, error) {
	result, err := db.conn.Exec(`UPDATE app_runs SET status = 'failed', completed_at = CURRENT_TIMESTAMP WHERE status = 'running'`)
	if err != nil {
		return 0, err
	}
	rows, _ := result.RowsAffected()
	return int(rows), nil
}

func (db *DB) DeleteAppRuns(appID string) error {
	_, err := db.conn.Exec(`DELETE FROM app_runs WHERE app_id = ?`, appID)
	return err
}

// UpcomingRunRecord is a run with app name for the schedules page
type UpcomingRunRecord struct {
	ID          string `json:"id"`
	AppID       string `json:"app_id"`
	AppName     string `json:"app_name"`
	ScheduledAt string `json:"scheduled_at"`
	Status      string `json:"status"`
	UserID      string `json:"user_id"`
}

func (db *DB) GetUpcomingRuns(userID string, limit int) ([]*UpcomingRunRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT r.id, r.app_id, a.name, r.scheduled_at, r.status, r.user_id
		FROM app_runs r JOIN apps a ON r.app_id = a.id
		WHERE r.user_id = ? AND r.status = 'pending' AND a.is_active = 1
		ORDER BY r.scheduled_at ASC LIMIT ?`
	rows, err := db.conn.Query(query, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []*UpcomingRunRecord
	for rows.Next() {
		var r UpcomingRunRecord
		if err := rows.Scan(&r.ID, &r.AppID, &r.AppName, &r.ScheduledAt, &r.Status, &r.UserID); err != nil {
			return nil, err
		}
		runs = append(runs, &r)
	}
	return runs, nil
}

func (db *DB) SkipAppRun(runID, userID string) error {
	_, err := db.conn.Exec(`UPDATE app_runs SET status = 'skipped', completed_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ? AND status = 'pending'`, runID, userID)
	return err
}

// ── Internal scan helpers ──

func scanAppRecord(row *sql.Row) (*AppRecord, error) {
	var a AppRecord
	var isActive, hasScripts int
	if err := row.Scan(&a.ID, &a.Name, &a.Description, &a.Prompt, &a.SystemPrompt,
		&a.Model, &a.Skills, &a.ScheduleRules, &a.Capabilities,
		&a.BufferSize, &a.RenewalThreshold, &isActive, &a.UserID, &a.CreatedAt, &a.UpdatedAt,
		&a.Parameters, &a.ParameterDefs,
		&a.Source, &a.SourceURL, &a.Version, &a.Author,
		&a.ManifestJSON, &hasScripts,
	); err != nil {
		return nil, err
	}
	a.IsActive = isActive != 0
	a.HasScripts = hasScripts != 0
	return &a, nil
}

func scanAppRecords(rows *sql.Rows) ([]*AppRecord, error) {
	var apps []*AppRecord
	for rows.Next() {
		var a AppRecord
		var isActive, hasScripts int
		if err := rows.Scan(&a.ID, &a.Name, &a.Description, &a.Prompt, &a.SystemPrompt,
			&a.Model, &a.Skills, &a.ScheduleRules, &a.Capabilities,
			&a.BufferSize, &a.RenewalThreshold, &isActive, &a.UserID, &a.CreatedAt, &a.UpdatedAt,
			&a.Parameters, &a.ParameterDefs,
			&a.Source, &a.SourceURL, &a.Version, &a.Author,
			&a.ManifestJSON, &hasScripts,
		); err != nil {
			return nil, err
		}
		a.IsActive = isActive != 0
		a.HasScripts = hasScripts != 0
		apps = append(apps, &a)
	}
	return apps, nil
}
