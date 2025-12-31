package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
	"tofi-core/internal/crypto"
	"tofi-core/internal/storage"
)

type Config struct {
	Port                  int
	HomeDir               string
	MaxConcurrentWorkflows int // 最大并发工作流数（默认 10）
}

type Server struct {
	config     Config
	registry   *ExecutionRegistry
	db         *storage.DB
	workerPool *WorkerPool
}

func NewServer(config Config) (*Server, error) {
	// 初始化 JWT Auth
	InitAuth()

	// 初始化加密（从环境变量获取密钥，或使用默认密钥）
	encryptionKey := os.Getenv("TOFI_ENCRYPTION_KEY")
	if encryptionKey == "" {
		// 默认密钥（生产环境必须使用环境变量！）
		encryptionKey = "tofi-default-encryption-key!!123" // 恰好 32 字节
		log.Println("⚠️  警告：使用默认加密密钥，生产环境请设置 TOFI_ENCRYPTION_KEY 环境变量")
	}
	if err := crypto.InitEncryption(encryptionKey); err != nil {
		return nil, fmt.Errorf("failed to initialize encryption: %v", err)
	}

	db, err := storage.InitDB(config.HomeDir)
	if err != nil {
		return nil, err
	}

	// 设置默认并发数
	if config.MaxConcurrentWorkflows <= 0 {
		config.MaxConcurrentWorkflows = 10
	}

	registry := NewExecutionRegistry()
	workerPool := NewWorkerPool(config.MaxConcurrentWorkflows, registry)

	return &Server{
		config:     config,
		registry:   registry,
		db:         db,
		workerPool: workerPool,
	}, nil
}

func (s *Server) Start() error {
	defer s.db.Close()
	defer s.workerPool.Shutdown()

	// 启动工作池
	s.workerPool.Start()

	// 启动前恢复僵尸任务（通过工作池提交）
	if err := s.recoverZombiesWithPool(); err != nil {
		log.Printf("⚠️  僵尸任务恢复失败: %v", err)
	}

	mux := http.NewServeMux()

	// 公开路由
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/stats", s.handleStats) // 工作池统计

	// 受保护的 API 路由 (包裹 AuthMiddleware)
	mux.HandleFunc("POST /api/v1/run", s.AuthMiddleware(s.handleRunWorkflow))
	mux.HandleFunc("GET /api/v1/executions/{id}", s.AuthMiddleware(s.handleGetExecution))
	mux.HandleFunc("GET /api/v1/executions/{id}/logs", s.AuthMiddleware(s.handleGetExecutionLogs))
	mux.HandleFunc("GET /api/v1/executions/{id}/artifacts", s.AuthMiddleware(s.handleListArtifacts))
	mux.HandleFunc("GET /api/v1/executions/{id}/artifacts/{filename}", s.AuthMiddleware(s.handleDownloadArtifact))
	mux.HandleFunc("POST /api/v1/executions/{id}/uploads", s.AuthMiddleware(s.handleUploadFile))

	// Secret 管理路由
	mux.HandleFunc("POST /api/v1/secrets", s.AuthMiddleware(s.handleCreateSecret))
	mux.HandleFunc("GET /api/v1/secrets", s.AuthMiddleware(s.handleListSecrets))
	mux.HandleFunc("GET /api/v1/secrets/{name}", s.AuthMiddleware(s.handleGetSecret))
	mux.HandleFunc("DELETE /api/v1/secrets/{name}", s.AuthMiddleware(s.handleDeleteSecret))

	// 配置 Server
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", s.config.Port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	log.Printf("🚀 Tofi Server listening on port %d", s.config.Port)
	return srv.ListenAndServe()
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// handleStats 返回工作池的统计信息
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats := s.workerPool.GetStats()
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}
