package server

import (
	"log"
	"os"
	"path/filepath"
	"tofi-core/internal/engine"
	"tofi-core/internal/parser"
)

// recoverZombiesWithPool 使用工作池恢复僵尸任务
func (s *Server) recoverZombiesWithPool() error {
	log.Println("🔍 开始扫描僵尸任务...")

	zombies, err := s.db.ListRunningExecutions()
	if err != nil {
		return err
	}

	if len(zombies) == 0 {
		log.Println("✅ 未发现僵尸任务")
		return nil
	}

	log.Printf("⚠️  发现 %d 个僵尸任务，开始恢复...", len(zombies))

	for _, record := range zombies {
		execID := record.ID
		log.Printf("🔄 恢复任务: %s (工作流: %s, 用户: %s)", execID, record.WorkflowName, record.User)

		// 1. 恢复执行上下文
		ctx, err := engine.LoadState(execID, s.db, s.config.HomeDir)
		if err != nil {
			s.db.UpdateStatus(execID, "FAILED")
			log.Printf("❌ 任务 %s 恢复失败: %v", execID, err)
			continue
		}

		// 2. 重新加载工作流定义
		wf, err := parser.ResolveWorkflow(record.WorkflowName, "workflows")
		if err != nil {
			s.db.UpdateStatus(execID, "FAILED")
			ctx.Log("恢复失败: 无法加载工作流定义 (%v)", err)
			log.Printf("❌ 任务 %s 恢复失败: %v", execID, err)
			continue
		}

		// 3. 重新设置日志文件
		logFilePath := filepath.Join(ctx.Paths.Logs, execID+".log")
		if f, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
			ctx.SetLogger(f)
		}

		// 4. 提交到工作池
		job := &WorkflowJob{
			ExecutionID:   execID,
			Workflow:      wf,
			Context:       ctx,
			InitialInputs: nil,
			DB:            s.db,
		}

		if err := s.workerPool.Submit(job); err != nil {
			log.Printf("❌ 任务 %s 提交失败: %v", execID, err)
			s.db.UpdateStatus(execID, "FAILED")
			continue
		}

		log.Printf("✅ 任务 %s 已提交到工作池恢复", execID)
	}

	log.Println("✅ 僵尸任务恢复完成")
	return nil
}
