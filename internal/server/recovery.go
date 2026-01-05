package server

import (
	"tofi-core/internal/engine"
	"tofi-core/internal/parser"
	"tofi-core/internal/pkg/logger"
)

// recoverZombiesWithPool 使用工作池恢复僵尸任务
func (s *Server) recoverZombiesWithPool() error {
	logger.Printf("🔍 开始扫描僵尸任务...")

	zombies, err := s.db.ListRunningExecutions()
	if err != nil {
		return err
	}

	if len(zombies) == 0 {
		logger.Printf("✅ 未发现僵尸任务")
		return nil
	}

	logger.Printf("⚠️  发现 %d 个僵尸任务，开始恢复...", len(zombies))

	for _, record := range zombies {
		execID := record.ID
		logger.Printf("🔄 恢复任务: %s (工作流: %s, 用户: %s)", execID, record.WorkflowName, record.User)

		// 1. 恢复执行上下文
		ctx, err := engine.LoadState(execID, s.db, s.config.HomeDir)
		if err != nil {
			s.db.UpdateStatus(execID, "FAILED")
			logger.Printf("❌ 任务 %s 恢复失败: %v", execID, err)
			continue
		}

		// 2. 重新加载工作流定义
		wf, err := parser.ResolveWorkflow(record.WorkflowName, "workflows")
		if err != nil {
			s.db.UpdateStatus(execID, "FAILED")
			ctx.Log("恢复失败: 无法加载工作流定义 (%v)", err)
			logger.Printf("❌ 任务 %s 恢复失败: %v", execID, err)
			continue
		}

		// 3. 提交到工作池
		job := &WorkflowJob{
			ExecutionID:   execID,
			Workflow:      wf,
			Context:       ctx,
			InitialInputs: nil,
			DB:            s.db,
		}

		if err := s.workerPool.Submit(job); err != nil {
			logger.Printf("❌ 任务 %s 提交失败: %v", execID, err)
			s.db.UpdateStatus(execID, "FAILED")
			continue
		}

		logger.Printf("✅ 任务 %s 已提交到工作池恢复", execID)
	}

	logger.Printf("✅ 僵尸任务恢复完成")
	return nil
}
