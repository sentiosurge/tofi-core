package main

import (
	"flag"
	"fmt"
	"os"
	"time"
	"tofi-core/internal/engine"
	"tofi-core/internal/models"
	"tofi-core/internal/parser"
	"tofi-core/internal/pkg/logger"
	"tofi-core/internal/server"
	"tofi-core/internal/storage"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

func main() {
	// 0. 加载环境变量 (开发环境)
	_ = godotenv.Load()

	// 1. 初始化全局日志 (轮转系统日志)
	// 简单预解析 home 目录，默认为 .tofi

homeDir := ".tofi"
	for i, arg := range os.Args {
		if (arg == "-home" || arg == "--home") && i+1 < len(os.Args) {
			homeDir = os.Args[i+1]
		}
	}
	logger.Init(homeDir)

	if len(os.Args) < 2 {
		printHelp()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		runCommand(os.Args[2:])
	case "server":
		serverCommand(os.Args[2:])
	case "token":
		tokenCommand(os.Args[2:])
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
	fmt.Println("  token   Generate a test JWT token for a user")
	fmt.Println()
	fmt.Println("Use 'tofi <command> -h' for more information about a command.")
}

func tokenCommand(args []string) {
	tokenCmd := flag.NewFlagSet("token", flag.ExitOnError)
	user := tokenCmd.String("user", "jack", "Username to encode in token")
	secret := tokenCmd.String("secret", "", "JWT secret (defaults to TOFI_JWT_SECRET env)")

	tokenCmd.Parse(args)

	if *secret != "" {
		os.Setenv("TOFI_JWT_SECRET", *secret)
	}

	server.InitAuth()
	token, err := server.GenerateToken(*user, "user") // CLI 生成的 token 默认 user 角色
	if err != nil {
		logger.Fatalf("Failed to generate token: %v", err)
	}

	fmt.Printf("JWT Token for user '%s':\n\n%s\n\n", *user, token)
	fmt.Println("Usage:")
	fmt.Printf("curl -H \"Authorization: Bearer %s\" ...\n", token)
}

func runCommand(args []string) {
	runCmd := flag.NewFlagSet("run", flag.ExitOnError)
	workflowPath := runCmd.String("workflow", "workflows/tofi_test_2.yaml", "Path to workflow YAML file")
	resumeID := runCmd.String("resume", "", "Execution ID to resume from (.tofi/states/)")
	homeDir := runCmd.String("home", ".tofi", "Tofi runtime directory")

	runCmd.Parse(args)

	// 0. 初始化数据库
	db, err := storage.InitDB(*homeDir)
	if err != nil {
		logger.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	var ctx *models.ExecutionContext
	var execID string

	// 1. 初始化 Context (新建或恢复)
	if *resumeID != "" {
		execID = *resumeID
		ctx, err = engine.LoadState(execID, db, *homeDir)
		if err != nil {
			logger.Fatalf("Resume failed: %v", err)
		}
		ctx.Log("Attempting to resume execution: %s", execID)
	} else {
		uuidStr := uuid.New().String()[:4]
		execID = time.Now().Format("102150405") + "-" + uuidStr
		ctx = models.NewExecutionContext(execID, "cli-admin", *homeDir)
		ctx.DB = db
	}

	// 2. 环境准备 (仅创建日志目录，其他按需创建)
	os.MkdirAll(ctx.Paths.Logs, 0755)

	// [REMOVED] Individual .log file per execution is now replaced by structured DB logs 
	// and shared system rotating log.
	/*
	logFilePath := filepath.Join(ctx.Paths.Logs, execID+".log")
	if f, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
		defer f.Close()
		ctx.SetLogger(f)
	}
	*/

	// 3. 加载 YAML
	wf, err := parser.LoadWorkflow(*workflowPath)
	if err != nil {
		ctx.Log("Failed to load workflow %s: %v", *workflowPath, err)
		os.Exit(1)
	}
	ctx.SetWorkflowName(wf.Name)

	// 4. 验证
	if err := engine.ValidateAll(wf); err != nil {
		ctx.Log("Configuration validation failed:\n%v", err)
		os.Exit(1)
	}

	ctx.Log("🐱 Tofi Engine Started (Home: %s)...", *homeDir)
	engine.Start(wf, ctx, nil) // CLI 暂时不传初始 inputs
	ctx.Wg.Wait()

	engine.Cleanup(ctx)
	engine.PrintSummary(ctx)

	// 8. 保存最终报告 (DB)
	if err := engine.SaveReport(wf, ctx, db); err != nil {
		ctx.Log("Failed to save report to DB: %v", err)
	} else {
		ctx.Log("Execution record saved to database.")
	}

	ctx.Log("🏁 Done.")
}

func serverCommand(args []string) {
	serverCmd := flag.NewFlagSet("server", flag.ExitOnError)
	port := serverCmd.Int("port", 8080, "HTTP server port")
	homeDir := serverCmd.String("home", ".tofi", "Tofi runtime directory")
	maxWorkers := serverCmd.Int("workers", 10, "Maximum concurrent workflows (default: 10)")
	sandboxMode := serverCmd.String("sandbox", "direct", "Sandbox mode: 'direct' or 'docker'")

	serverCmd.Parse(args)

	cfg := server.Config{
		Port:                   *port,
		HomeDir:                *homeDir,
		MaxConcurrentWorkflows: *maxWorkers,
		SandboxMode:            *sandboxMode,
	}

	srv, err := server.NewServer(cfg)
	if err != nil {
		logger.Fatalf("Failed to initialize server: %v", err)
	}
	if err := srv.Start(); err != nil {
		logger.Fatalf("Server failed: %v", err)
	}
}