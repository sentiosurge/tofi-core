package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
	"tofi-core/internal/engine"
	"tofi-core/internal/models"
	"tofi-core/internal/parser"
	"tofi-core/internal/server"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

func main() {
	// 0. 加载环境变量 (开发环境)
	_ = godotenv.Load()

	if len(os.Args) < 2 {
		printHelp()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		runCommand(os.Args[2:])
	case "server":
		serverCommand(os.Args[2:])
	case "help", "-h", "--help":
		printHelp()
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		printHelp()
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Println("Tofi Workflow Engine CLI")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  tofi <command> [arguments]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  run     Execute a workflow file immediately (CLI mode)")
	fmt.Println("  server  Start the workflow engine server (HTTP API)")
	fmt.Println()
	fmt.Println("Use 'tofi <command> -h' for more information about a command.")
}

func runCommand(args []string) {
	// 定义 run 子命令的 FlagSet
	runCmd := flag.NewFlagSet("run", flag.ExitOnError)
	workflowPath := runCmd.String("workflow", "workflows/tofi_test_2.yaml", "Path to workflow YAML file")
	resumeID := runCmd.String("resume", "", "Execution ID to resume from (.tofi/states/)")
	homeDir := runCmd.String("home", ".tofi", "Tofi runtime directory (logs, states, reports)")

	// 解析参数
	runCmd.Parse(args)

	var ctx *models.ExecutionContext
	var err error
	var execID string

	// 1. 初始化 Context (新建或恢复)
	if *resumeID != "" {
		execID = *resumeID
		log.Printf("Attempting to resume execution: %s", execID)
		ctx, err = engine.LoadState(execID, *homeDir)
		if err != nil {
			log.Fatalf("Resume failed: %v", err)
		}
	} else {
		uuidStr := uuid.New().String()[:4]
		execID = time.Now().Format("102150405") + "-" + uuidStr
		ctx = models.NewExecutionContext(execID, *homeDir)
	}

	// 2. 环境准备 (Logs)
	if err := os.MkdirAll(ctx.Paths.Logs, 0755); err != nil {
		log.Fatalf("Failed to create log directory: %v", err)
	}
	logFileName := time.Now().Format("20060102") + ".log"
	f, _ := os.OpenFile(filepath.Join(ctx.Paths.Logs, logFileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)

	log.SetFlags(log.Ldate | log.Ltime)
	log.SetOutput(io.MultiWriter(os.Stdout, f))

	// 3. 加载 YAML 工作流
	wf, err := parser.LoadWorkflow(*workflowPath)
	if err != nil {
		log.Fatalf("Failed to load workflow %s: %v", *workflowPath, err)
	}

	// 4. 预先验证工作流 Schema
	if err := engine.ValidateAll(wf); err != nil {
		log.Fatalf("Configuration validation failed:\n%v", err)
	}

	log.Printf("[%s] 🐱 Tofi Engine Started (Home: %s)...", execID, *homeDir)

	// 5. 智能寻找入口并启动
	engine.Start(wf, ctx)

	// 6. 等待所有节点运行结束
	ctx.Wg.Wait()

	// 7. 打印精美的 ASCII 总结表格
	engine.PrintSummary(ctx)

	// 8. 保存最终报告 (Reports)
	if err := engine.SaveReport(wf, ctx); err != nil {
		log.Printf("Failed to save report: %v", err)
	} else {
		log.Printf("Report saved to: %s", ctx.Paths.Reports)
	}

	log.Println("🏁 Done.")
}

func serverCommand(args []string) {
	serverCmd := flag.NewFlagSet("server", flag.ExitOnError)
	port := serverCmd.Int("port", 8080, "HTTP server port")
	homeDir := serverCmd.String("home", ".tofi", "Tofi runtime directory")
	
	serverCmd.Parse(args)

	cfg := server.Config{
		Port:    *port,
		HomeDir: *homeDir,
	}

	srv := server.NewServer(cfg)
	if err := srv.Start(); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}