package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"tofi-core/internal/models"
)

var stateLock sync.Mutex

// StateData 定义中间状态文件的结构
type StateData struct {
	ExecutionID string            `json:"execution_id"`
	UpdateTime  time.Time         `json:"update_time"`
	Results     map[string]string `json:"results"`
}

// SaveState 将当前的运行状态写入 states/ 目录 (Snapshot)
func SaveState(ctx *models.ExecutionContext) error {
	stateLock.Lock()
	defer stateLock.Unlock()

	// 1. 确保目录存在
	if err := os.MkdirAll(ctx.Paths.States, 0755); err != nil {
		return err
	}

	// 2. 获取数据快照
	results, _ := ctx.Snapshot()

	state := StateData{
		ExecutionID: ctx.ExecutionID,
		UpdateTime:  time.Now(),
		Results:     results,
	}

	// 3. 写入文件
	filePath := filepath.Join(ctx.Paths.States, fmt.Sprintf("state-%s.json", ctx.ExecutionID))
	fileData, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filePath, fileData, 0644)
}

// LoadState 加载指定 ExecutionID 的状态
func LoadState(execID, homeDir string) (*models.ExecutionContext, error) {
	filePath := filepath.Join(homeDir, "states", fmt.Sprintf("state-%s.json", execID))
	
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("读取状态文件失败: %v", err)
	}

	var state StateData
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("解析状态文件失败: %v", err)
	}

	// 重建 Context
	ctx := models.NewExecutionContext(execID, homeDir)
	
	// 恢复 Results
	// 关键策略：只恢复“成功”的结果。
	// 如果是 ERR_PROPAGATION 或 SKIPPED_BY，则清除它们，让引擎重新跑这些节点
	for k, v := range state.Results {
		if strings.HasPrefix(v, "ERR_PROPAGATION:") || strings.HasPrefix(v, "SKIPPED_BY:") {
			continue // 丢弃错误记录，实现“重试”
		}
		ctx.Results[k] = v
	}

	return ctx, nil
}

// SaveReport 生成最终报告写入 reports/ 目录
func SaveReport(wf *models.Workflow, ctx *models.ExecutionContext) error {
	results, stats := ctx.Snapshot()

	// 1. 计算整体状态
	overallStatus := "SUCCESS"
	startTime := time.Now() // 默认值
	if len(stats) > 0 {
		startTime = stats[0].StartTime
		for _, stat := range stats {
			if stat.Status == "ERROR" {
				overallStatus = "FAILED"
				break
			}
		}
	}

	// 2. 构造 ExecutionResult 对象
	result := models.ExecutionResult{
		ExecutionID:  ctx.ExecutionID,
		WorkflowName: wf.Name,
		Status:       overallStatus,
		StartTime:    startTime,
		EndTime:      time.Now(),
		Duration:     time.Since(startTime).String(),
		Stats:        stats,
		Outputs:      results,
	}

	// 3. 写入文件
	if err := os.MkdirAll(ctx.Paths.Reports, 0755); err != nil {
		return err
	}

	filePath := filepath.Join(ctx.Paths.Reports, fmt.Sprintf("report-%s.json", ctx.ExecutionID))
	fileData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(filePath, fileData, 0644); err != nil {
		return err
	}

	// 4. 清理逻辑：如果整体成功，则删除中间状态文件 (Snapshot)
	if overallStatus == "SUCCESS" {
		statePath := filepath.Join(ctx.Paths.States, fmt.Sprintf("state-%s.json", ctx.ExecutionID))
		_ = os.Remove(statePath) // 忽略删除失败的情况
	}

	return nil
}
