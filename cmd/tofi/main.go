package main

import (
	"flag"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
	"tofi-core/internal/engine"
	"tofi-core/internal/models"
	"tofi-core/internal/parser"

	"github.com/google/uuid"
)

func main() {
	// 0. 解析命令行参数
	workflowPath := flag.String("workflow", "workflows/tofi_test_2.yaml", "工作流 YAML 文件路径")
	resumeID := flag.String("resume", "", "要恢复的 Execution ID (从 .tofi/states/ 加载)")
	homeDir := flag.String("home", ".tofi", "Tofi 运行时目录 (存放 logs, states, reports)")
	flag.Parse()

	var ctx *models.ExecutionContext
	var err error
	var execID string

	// 1. 初始化 Context (新建或恢复)
	if *resumeID != "" {
		execID = *resumeID
		log.Printf("尝试恢复执行: %s", execID)
		ctx, err = engine.LoadState(execID, *homeDir)
		if err != nil {
			log.Fatalf("恢复失败: %v", err)
		}
	} else {
		uuid := uuid.New().String()[:4]
		execID = time.Now().Format("102150405") + "-" + uuid
		ctx = models.NewExecutionContext(execID, *homeDir)
	}

	// 2. 环境准备 (Logs)
	if err := os.MkdirAll(ctx.Paths.Logs, 0755); err != nil {
		log.Fatalf("无法创建日志目录: %v", err)
	}
	logFileName := time.Now().Format("20060102") + ".log"
	f, _ := os.OpenFile(filepath.Join(ctx.Paths.Logs, logFileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)

	log.SetFlags(log.Ldate | log.Ltime)
	log.SetOutput(io.MultiWriter(os.Stdout, f))

	// 3. 加载 YAML 工作流
	// 注意：Resume 模式下，理想情况应该从 State 里恢复 Workflow 内容（防止 YAML 被改了）。
	// 但为了简单，我们还是重新加载 YAML。用户需确保 YAML 没变。
	wf, err := parser.LoadWorkflow(*workflowPath)
	if err != nil {
		log.Fatalf("无法加载工作流 %s: %v", *workflowPath, err)
	}

	// 4. 预先验证工作流 Schema
	if err := engine.ValidateAll(wf); err != nil {
		log.Fatalf("配置校验失败，请检查 YAML:\n%v", err)
	}

	log.Printf("[%s] 🐱 Tofi Engine 启动 (Home: %s)...", execID, *homeDir)

	// 5. 智能寻找入口并启动
	engine.Start(wf, ctx)

	// 6. 等待所有节点运行结束
	ctx.Wg.Wait()

	// 7. 打印精美的 ASCII 总结表格
	engine.PrintSummary(ctx)

	// 8. 保存最终报告 (Reports)
	if err := engine.SaveReport(wf, ctx); err != nil {
		log.Printf("保存执行结果失败: %v", err)
	} else {
		log.Printf("报告已保存至: %s", ctx.Paths.Reports)
	}

	// 9. 清理中间状态 (可选)
	// engine.CleanupState(ctx) 

	log.Println("🏁 Done.")
}