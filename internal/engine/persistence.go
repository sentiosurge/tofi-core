package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"tofi-core/internal/models"
)

// SaveExecutionResult 将执行上下文转换为正式的结果对象并持久化
func SaveExecutionResult(wf *models.Workflow, ctx *models.ExecutionContext) error {
	// 1. 计算整体状态
	overallStatus := "SUCCESS"
	for _, stat := range ctx.Stats {
		if stat.Status == "ERROR" {
			overallStatus = "FAILED"
			break
		}
	}

	// 2. 构造 ExecutionResult 对象
	startTime := ctx.Stats[0].StartTime // 粗略取第一个节点的开始时间
	result := models.ExecutionResult{
		ExecutionID:  ctx.ExecutionID,
		WorkflowName: wf.Name,
		Status:       overallStatus,
		StartTime:    startTime,
		EndTime:      time.Now(),
		Duration:     time.Since(startTime).String(),
		Stats:        ctx.Stats,
		Outputs:      ctx.Results,
	}

	// 3. 写入文件
	outputDir := "./executions"
	os.MkdirAll(outputDir, 0755)

	filePath := filepath.Join(outputDir, fmt.Sprintf("%s.json", ctx.ExecutionID))
	fileData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filePath, fileData, 0644)
}
