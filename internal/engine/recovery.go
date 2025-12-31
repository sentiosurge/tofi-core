package engine

import (
	"log"
	"os"
	"path/filepath"
	"tofi-core/internal/models"
	"tofi-core/internal/parser"
	"tofi-core/internal/storage"
)

// ContextRegistry 定义执行上下文注册接口（解耦对 server.ExecutionRegistry 的依赖）
type ContextRegistry interface {
	Register(id string, ctx *models.ExecutionContext)
	Unregister(id string)
}

// RecoverZombies 扫描并恢复所有僵尸任务（服务器启动时调用）
// 僵尸任务定义：数据库中状态为 RUNNING，但内存中不存在的执行
func RecoverZombies(db *storage.DB, homeDir string, registry ContextRegistry) error {
	log.Println("🔍 开始扫描僵尸任务...")

	zombies, err := db.ListRunningExecutions()
	if err != nil {
		return err
	}

	if len(zombies) == 0 {
		log.Println("✅ 未发现僵尸任务")
		return nil
	}

	log.Printf("⚠️  发现 %d 个僵尸任务，开始恢复...", len(zombies))

	for _, record := range zombies {
		if err := recoverSingleExecution(record, db, homeDir, registry); err != nil {
			log.Printf("❌ 任务 %s 恢复失败: %v", record.ID, err)
		}
	}

	log.Println("✅ 僵尸任务恢复完成")
	return nil
}

// recoverSingleExecution 恢复单个执行任务
func recoverSingleExecution(record *storage.ExecutionRecord, db *storage.DB, homeDir string, registry ContextRegistry) error {
	execID := record.ID
	log.Printf("🔄 恢复任务: %s (工作流: %s, 用户: %s)", execID, record.WorkflowName, record.User)

	// 1. 恢复执行上下文
	ctx, err := LoadState(execID, db, homeDir)
	if err != nil {
		// 无法恢复上下文，标记为失败
		db.UpdateStatus(execID, "FAILED")
		return err
	}

	// 2. 重新加载工作流定义
	wf, err := parser.ResolveWorkflow(record.WorkflowName, "workflows")
	if err != nil {
		// 工作流文件不存在或解析失败，标记为失败
		db.UpdateStatus(execID, "FAILED")
		ctx.Log("恢复失败: 无法加载工作流定义 (%v)", err)
		return err
	}

	// 3. 重新设置日志文件
	logFilePath := filepath.Join(ctx.Paths.Logs, execID+".log")
	if f, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
		ctx.SetLogger(f)
	}

	// 4. 注册到内存 Registry
	registry.Register(execID, ctx)

	// 5. 异步恢复执行
	go func() {
		defer registry.Unregister(execID)
		defer ctx.Close()

		defer func() {
			if r := recover(); r != nil {
				ctx.Log("PANIC RECOVERED: %v", r)
				db.UpdateStatus(execID, "FAILED")
			}
		}()

		ctx.Log("♻️  从僵尸状态恢复执行")

		// 重新启动工作流（已完成的节点会被自动跳过）
		Start(wf, ctx, nil)
		ctx.Wg.Wait()

		// 保存最终报告
		if err := SaveReport(wf, ctx, db); err != nil {
			ctx.Log("Failed to save report to DB: %v", err)
		} else {
			ctx.Log("执行记录已保存到数据库")
		}

		ctx.Log("🏁 恢复的任务执行完成")
	}()

	log.Printf("✅ 任务 %s 已提交恢复", execID)
	return nil
}
