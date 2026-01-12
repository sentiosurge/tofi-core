package engine

import (
	"encoding/json"
	"tofi-core/internal/models"
	"tofi-core/internal/storage"
)

// SaveState 保存工作流执行的中间状态到数据库
// 使用脱敏后的快照，确保 secrets 不会被持久化
func SaveState(ctx *models.ExecutionContext) error {
	if ctx.DB == nil {
		return nil
	}
	db, ok := ctx.DB.(*storage.DB)
	if !ok {
		return nil
	}

	// 使用脱敏后的快照
	results, stats := ctx.MaskedSnapshot()
	state := models.ExecutionResult{
		ExecutionID:  ctx.ExecutionID,
		WorkflowID:   ctx.WorkflowID,
		WorkflowName: ctx.WorkflowName,
		Status:       "RUNNING",
		Outputs:      results,
		Stats:        stats,
	}

	jb, _ := json.Marshal(state)
	return db.SaveExecution(ctx.ExecutionID, ctx.WorkflowID, ctx.WorkflowName, ctx.User, "RUNNING", string(jb), "")
}

// LoadState 从数据库中恢复执行状态
func LoadState(execID string, db *storage.DB, homeDir string) (*models.ExecutionContext, error) {
	record, err := db.GetExecution(execID)
	if err != nil {
		return nil, err
	}

	var state models.ExecutionResult
	if err := json.Unmarshal([]byte(record.StateJSON), &state); err != nil {
		return nil, err
	}

	ctx := models.NewExecutionContext(execID, record.User, homeDir)
	ctx.WorkflowName = record.WorkflowName
	ctx.WorkflowID = record.WorkflowID
	ctx.DB = db
	
	for k, v := range state.Outputs {
		ctx.SetResult(k, v)
	}
	ctx.Stats = state.Stats
	
	return ctx, nil
}

// SaveReport 将最终报告存入数据库
// 使用脱敏后的快照，确保 secrets 不会被持久化
func SaveReport(wf *models.Workflow, ctx *models.ExecutionContext, db *storage.DB) error {
	if db == nil {
		return nil
	}

	// 使用脱敏后的快照
	results, stats := ctx.MaskedSnapshot()

	// 检测是否有节点失败
	status := "COMPLETED"
	for _, stat := range stats {
		if stat.Status == "ERROR" {
			status = "FAILED"
			break
		}
	}

	report := models.ExecutionResult{
		ExecutionID:  ctx.ExecutionID,
		WorkflowID:   wf.ID,
		WorkflowName: wf.Name,
		Status:       status,
		Stats:        stats,
		Outputs:      results,
	}

	jb, _ := json.Marshal(report)
	return db.SaveExecution(ctx.ExecutionID, wf.ID, wf.Name, ctx.User, status, "", string(jb))
}