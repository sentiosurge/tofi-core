package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// KanbanCardRecord 看板卡片记录
type KanbanCardRecord struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"` // todo, working, hold, done, failed
	AgentID     string `json:"agent_id,omitempty"`
	ExecutionID string `json:"execution_id,omitempty"`
	Progress    int    `json:"progress"` // 0-100
	Result      string `json:"result,omitempty"`
	Steps       string `json:"steps,omitempty"`   // JSON array of step objects
	Actions     string `json:"actions,omitempty"` // JSON array of action objects (e.g. install_skill)
	UserID      string `json:"user_id"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// KanbanAction represents a pending action (e.g. skill installation) that requires user approval
type KanbanAction struct {
	Type    string `json:"type"`              // "install_skill"
	SkillID string `json:"skill_id"`          // e.g. "owner/repo@skill-name"
	Name    string `json:"name"`              // Human-readable name
	Reason  string `json:"reason,omitempty"`  // Why this skill is useful
	Status  string `json:"status"`            // "pending", "installing", "installed", "failed", "aborted"
	Error   string `json:"error,omitempty"`   // Error message if failed
}

// initKanbanTable 创建 kanban_cards 表
func (db *DB) initKanbanTable() error {
	query := `
	CREATE TABLE IF NOT EXISTS kanban_cards (
		id TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		description TEXT DEFAULT '',
		status TEXT NOT NULL DEFAULT 'todo',
		agent_id TEXT DEFAULT '',
		execution_id TEXT DEFAULT '',
		progress INTEGER DEFAULT 0,
		result TEXT DEFAULT '',
		steps TEXT DEFAULT '[]',
		user_id TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_kanban_user ON kanban_cards(user_id);
	CREATE INDEX IF NOT EXISTS idx_kanban_status ON kanban_cards(status);`

	_, err := db.conn.Exec(query)
	if err != nil {
		return err
	}

	// Migration: add steps column if it doesn't exist
	db.conn.Exec(`ALTER TABLE kanban_cards ADD COLUMN steps TEXT DEFAULT '[]'`)
	// Migration: add actions column if it doesn't exist
	db.conn.Exec(`ALTER TABLE kanban_cards ADD COLUMN actions TEXT DEFAULT '[]'`)
	return nil
}

// CreateKanbanCard 创建一张看板卡片
func (db *DB) CreateKanbanCard(card *KanbanCardRecord) error {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	if card.CreatedAt == "" {
		card.CreatedAt = now
	}
	card.UpdatedAt = now

	if card.Status == "" {
		card.Status = "todo"
	}

	if card.Steps == "" {
		card.Steps = "[]"
	}
	if card.Actions == "" {
		card.Actions = "[]"
	}
	query := `INSERT INTO kanban_cards (id, title, description, status, agent_id, execution_id, progress, result, steps, actions, user_id, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := db.conn.Exec(query,
		card.ID, card.Title, card.Description, card.Status,
		card.AgentID, card.ExecutionID, card.Progress, card.Result, card.Steps, card.Actions,
		card.UserID, card.CreatedAt, card.UpdatedAt,
	)
	return err
}

// GetKanbanCard 获取单张卡片
func (db *DB) GetKanbanCard(id string) (*KanbanCardRecord, error) {
	query := `SELECT id, title, COALESCE(description,''), status, COALESCE(agent_id,''), COALESCE(execution_id,''), progress, COALESCE(result,''), COALESCE(steps,'[]'), COALESCE(actions,'[]'), user_id, created_at, updated_at
	FROM kanban_cards WHERE id = ?`

	row := db.conn.QueryRow(query, id)
	var c KanbanCardRecord
	err := row.Scan(&c.ID, &c.Title, &c.Description, &c.Status,
		&c.AgentID, &c.ExecutionID, &c.Progress, &c.Result, &c.Steps, &c.Actions,
		&c.UserID, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ListKanbanCards 列出用户的所有卡片，按 updated_at DESC 排序
func (db *DB) ListKanbanCards(userID string) ([]*KanbanCardRecord, error) {
	query := `SELECT id, title, COALESCE(description,''), status, COALESCE(agent_id,''), COALESCE(execution_id,''), progress, COALESCE(result,''), COALESCE(steps,'[]'), COALESCE(actions,'[]'), user_id, created_at, updated_at
	FROM kanban_cards WHERE user_id = ? ORDER BY updated_at DESC`

	rows, err := db.conn.Query(query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cards []*KanbanCardRecord
	for rows.Next() {
		var c KanbanCardRecord
		if err := rows.Scan(&c.ID, &c.Title, &c.Description, &c.Status,
			&c.AgentID, &c.ExecutionID, &c.Progress, &c.Result, &c.Steps, &c.Actions,
			&c.UserID, &c.CreatedAt, &c.UpdatedAt); err != nil {
			continue
		}
		cards = append(cards, &c)
	}
	return cards, nil
}

// UpdateKanbanCard 更新卡片字段（仅更新非空字段）
func (db *DB) UpdateKanbanCard(card *KanbanCardRecord) error {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	query := `UPDATE kanban_cards SET
		title = ?, description = ?, status = ?,
		agent_id = ?, execution_id = ?, progress = ?,
		result = ?, updated_at = ?
	WHERE id = ? AND user_id = ?`

	result, err := db.conn.Exec(query,
		card.Title, card.Description, card.Status,
		card.AgentID, card.ExecutionID, card.Progress,
		card.Result, now,
		card.ID, card.UserID,
	)
	if err != nil {
		return err
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("card not found or not owned by user")
	}
	return nil
}

// DeleteKanbanCard 删除卡片（仅自己的）
func (db *DB) DeleteKanbanCard(id, userID string) error {
	query := `DELETE FROM kanban_cards WHERE id = ? AND user_id = ?`
	result, err := db.conn.Exec(query, id, userID)
	if err != nil {
		return err
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// AppendKanbanStep 追加一个步骤到卡片的 steps JSON 数组
func (db *DB) AppendKanbanStep(id string, step map[string]interface{}) error {
	// Read current steps
	var stepsJSON string
	err := db.conn.QueryRow(`SELECT COALESCE(steps,'[]') FROM kanban_cards WHERE id = ?`, id).Scan(&stepsJSON)
	if err != nil {
		return err
	}

	var steps []map[string]interface{}
	if err := json.Unmarshal([]byte(stepsJSON), &steps); err != nil {
		steps = []map[string]interface{}{}
	}

	steps = append(steps, step)

	newJSON, err := json.Marshal(steps)
	if err != nil {
		return err
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	_, err = db.conn.Exec(`UPDATE kanban_cards SET steps = ?, updated_at = ? WHERE id = ?`, string(newJSON), now, id)
	return err
}

// UpdateKanbanStep 更新最后一个步骤的状态（如 running → done），并可附带结果
func (db *DB) UpdateKanbanStep(id string, toolName string, status string, result string, durationMs int64) error {
	var stepsJSON string
	err := db.conn.QueryRow(`SELECT COALESCE(steps,'[]') FROM kanban_cards WHERE id = ?`, id).Scan(&stepsJSON)
	if err != nil {
		return err
	}

	var steps []map[string]interface{}
	if err := json.Unmarshal([]byte(stepsJSON), &steps); err != nil {
		return err
	}

	// Find the last step with this tool name and update its status + result
	for i := len(steps) - 1; i >= 0; i-- {
		if name, ok := steps[i]["name"].(string); ok && name == toolName {
			steps[i]["status"] = status
			if result != "" {
				// Truncate result to 5000 chars to avoid DB bloat
				if len(result) > 5000 {
					result = result[:5000] + "..."
				}
				steps[i]["result"] = result
			}
			if durationMs > 0 {
				steps[i]["duration_ms"] = durationMs
			}
			break
		}
	}

	newJSON, err := json.Marshal(steps)
	if err != nil {
		return err
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	_, err = db.conn.Exec(`UPDATE kanban_cards SET steps = ?, updated_at = ? WHERE id = ?`, string(newJSON), now, id)
	return err
}

// AppendKanbanAction 追加一个 action 到卡片的 actions JSON 数组
func (db *DB) AppendKanbanAction(id string, action KanbanAction) error {
	var actionsJSON string
	err := db.conn.QueryRow(`SELECT COALESCE(actions,'[]') FROM kanban_cards WHERE id = ?`, id).Scan(&actionsJSON)
	if err != nil {
		return err
	}

	var actions []KanbanAction
	if err := json.Unmarshal([]byte(actionsJSON), &actions); err != nil {
		actions = []KanbanAction{}
	}

	actions = append(actions, action)

	newJSON, err := json.Marshal(actions)
	if err != nil {
		return err
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	_, err = db.conn.Exec(`UPDATE kanban_cards SET actions = ?, updated_at = ? WHERE id = ?`, string(newJSON), now, id)
	return err
}

// UpdateKanbanAction 更新卡片的指定 action（按 index）
func (db *DB) UpdateKanbanAction(id string, index int, status string, errMsg string) error {
	var actionsJSON string
	err := db.conn.QueryRow(`SELECT COALESCE(actions,'[]') FROM kanban_cards WHERE id = ?`, id).Scan(&actionsJSON)
	if err != nil {
		return err
	}

	var actions []KanbanAction
	if err := json.Unmarshal([]byte(actionsJSON), &actions); err != nil {
		return fmt.Errorf("failed to parse actions: %v", err)
	}

	if index < 0 || index >= len(actions) {
		return fmt.Errorf("action index %d out of range (have %d actions)", index, len(actions))
	}

	actions[index].Status = status
	if errMsg != "" {
		actions[index].Error = errMsg
	} else {
		actions[index].Error = "" // Clear previous error on success
	}

	newJSON, err := json.Marshal(actions)
	if err != nil {
		return err
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	_, err = db.conn.Exec(`UPDATE kanban_cards SET actions = ?, updated_at = ? WHERE id = ?`, string(newJSON), now, id)
	return err
}

// GetKanbanActions 获取卡片的 actions 列表
func (db *DB) GetKanbanActions(id string) ([]KanbanAction, error) {
	var actionsJSON string
	err := db.conn.QueryRow(`SELECT COALESCE(actions,'[]') FROM kanban_cards WHERE id = ?`, id).Scan(&actionsJSON)
	if err != nil {
		return nil, err
	}

	var actions []KanbanAction
	if err := json.Unmarshal([]byte(actionsJSON), &actions); err != nil {
		return nil, err
	}
	return actions, nil
}

// QueryHoldCards 查询所有 hold 状态的卡片 ID（用于 server 重启恢复）
func (db *DB) QueryHoldCards() ([]string, error) {
	rows, err := db.conn.Query(`SELECT id FROM kanban_cards WHERE status = 'hold'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// UpdateKanbanCardStatus 只更新卡片状态（不改 progress/result）
func (db *DB) UpdateKanbanCardStatus(id string, status string) error {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	_, err := db.conn.Exec(`UPDATE kanban_cards SET status = ?, updated_at = ? WHERE id = ?`, status, now, id)
	return err
}

// RecoverZombieKanbanCards marks any 'working' kanban cards as 'failed'.
// Called on server startup to clean up cards that were interrupted by a restart.
func (db *DB) RecoverZombieKanbanCards() (int, error) {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	result, err := db.conn.Exec(
		`UPDATE kanban_cards SET status = 'failed', result = COALESCE(result,'') || ' [Server restarted during execution]', updated_at = ? WHERE status = 'working'`,
		now,
	)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// UpdateKanbanCardBySystem 系统级更新（MCP Tool 用，不检查 user_id）
func (db *DB) UpdateKanbanCardBySystem(id string, status string, progress int, result string) error {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	query := `UPDATE kanban_cards SET status = ?, progress = ?, result = ?, updated_at = ? WHERE id = ?`
	res, err := db.conn.Exec(query, status, progress, result, now, id)
	if err != nil {
		return err
	}

	rows, _ := res.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}
