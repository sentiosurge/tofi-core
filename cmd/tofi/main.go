package main

import (
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
	// 1. 环境准备
	uuid := uuid.New().String()[:4]
	execID := time.Now().Format("102150405") + "-" + uuid
	logDir := "./logs"
	os.MkdirAll(logDir, 0755)
	logFileName := time.Now().Format("20060102") + ".log"
	f, _ := os.OpenFile(filepath.Join(logDir, logFileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)

	log.SetFlags(log.Ldate | log.Ltime)
	log.SetOutput(io.MultiWriter(os.Stdout, f))

	// 2. 加载 YAML 工作流
	wf, err := parser.LoadWorkflow("workflows/tofi_test_2.yaml")
	if err != nil {
		log.Fatalf("无法加载工作流: %v", err)
	}

	// 3. 【关键】：使用构造函数初始化上下文
	ctx := models.NewExecutionContext(execID)

	log.Printf("[%s] 🐱 Tofi Engine 启动...", execID)

	// 4. 智能寻找入口并启动
	engine.Start(wf, ctx)

	// 5. 等待所有节点运行结束
	ctx.Wg.Wait()

	// 6. 打印精美的 ASCII 总结表格
	engine.PrintSummary(ctx)

	if err := engine.SaveExecutionResult(wf, ctx); err != nil {
		log.Printf("保存执行结果失败: %v", err)
	}

	log.Println("🏁 Done.")
}
