package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"tofi-core/internal/bridge"
	"tofi-core/internal/daemon"
	"tofi-core/internal/chat"
	"tofi-core/internal/crypto"
	"tofi-core/internal/executor"
	"tofi-core/internal/models"
	"tofi-core/internal/skills"
	"tofi-core/internal/storage"
	"tofi-core/internal/workspace"
)

type Config struct {
	Port                   int
	HomeDir                string
	MaxConcurrentWorkflows int // 最大并发工作流数（默认 10）
}

// HoldSignal is sent through a hold channel to resume a paused agent
type HoldSignal struct {
	Action string // "continue" or "abort"
}

// PreviewSession 缓存已 clone 但尚未确认安装的 skill 预览
type PreviewSession struct {
	Skills    []*models.SkillFile
	Source    *skills.ParsedSource
	Cleanup   func()
	CreatedAt time.Time
	Scope     string // "public" (git) or "private" (zip upload)
	UserID    string // installer user ID (used when Scope="private")
}

type Server struct {
	config     Config
	registry   *ExecutionRegistry
	db         *storage.DB
	workerPool *WorkerPool
	scheduler  *Scheduler
	executor   executor.Executor // Sandbox command executor
	// Hold channel management for agent tofi_suggest_install blocking
	holdMu       sync.Mutex
	holdChannels map[string]chan HoldSignal // cardID → signal channel

	// Preview session cache for two-phase skill install
	previewMu       sync.Mutex
	previewSessions map[string]*PreviewSession // sessionID → session

	// App scheduler (min-heap + timer for scheduled app runs)
	appScheduler *AppScheduler

	// File-based workspace (source of truth for agents)
	workspace     *workspace.Workspace
	workspaceSync *workspace.Sync

	// Chat session store (XML files + SQLite index)
	chatStore *chat.Store

	// Bridge Manager (Telegram bidirectional chat)
	bridgeManager *bridge.ChatBridgeManager

	// Access token for token-mode auth (read from config.yaml)
	accessToken string
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

	// Initialize sandbox executor (direct execution with software-level isolation)
	exec := executor.NewDirectExecutor(config.HomeDir)

	return &Server{
		config:          config,
		registry:        registry,
		db:              db,
		workerPool:      workerPool,
		executor:        exec,
		holdChannels:    make(map[string]chan HoldSignal),
		previewSessions: make(map[string]*PreviewSession),
		chatStore:       chat.NewStore(config.HomeDir, db),
	}, nil
}

// createHoldChannel creates a buffered channel for a card to wait on
func (s *Server) createHoldChannel(cardID string) chan HoldSignal {
	s.holdMu.Lock()
	defer s.holdMu.Unlock()
	ch := make(chan HoldSignal, 1)
	s.holdChannels[cardID] = ch
	return ch
}

// signalHold sends a signal to unblock the agent waiting on this card
func (s *Server) signalHold(cardID string, action string) bool {
	s.holdMu.Lock()
	defer s.holdMu.Unlock()
	ch, ok := s.holdChannels[cardID]
	if !ok {
		return false
	}
	ch <- HoldSignal{Action: action}
	delete(s.holdChannels, cardID)
	return true
}

// removeHoldChannel cleans up a hold channel (used on timeout/cleanup)
func (s *Server) removeHoldChannel(cardID string) {
	s.holdMu.Lock()
	defer s.holdMu.Unlock()
	delete(s.holdChannels, cardID)
}

// --- Preview Session Management ---

func (s *Server) createPreviewSession(id string, session *PreviewSession) {
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	s.previewSessions[id] = session
}

func (s *Server) getPreviewSession(id string) *PreviewSession {
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	return s.previewSessions[id]
}

func (s *Server) removePreviewSession(id string) {
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	if sess, ok := s.previewSessions[id]; ok {
		if sess.Cleanup != nil {
			sess.Cleanup()
		}
		delete(s.previewSessions, id)
	}
}

func (s *Server) cleanupPreviewSessions() {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.previewMu.Lock()
		now := time.Now()
		for id, sess := range s.previewSessions {
			if now.Sub(sess.CreatedAt) > 10*time.Minute {
				if sess.Cleanup != nil {
					sess.Cleanup()
				}
				delete(s.previewSessions, id)
				log.Printf("[skills] preview session %s expired, cleaned up", id)
			}
		}
		s.previewMu.Unlock()
	}
}

func (s *Server) Start() error {
	defer s.db.Close()
	defer s.workerPool.Shutdown()

	// 启动工作池
	s.workerPool.Start()

	// 启动 Cron 调度器
	s.scheduler = NewScheduler(s)
	if err := s.scheduler.Start(); err != nil {
		log.Printf("⚠️  Cron 调度器启动失败: %v", err)
	}
	defer s.scheduler.Stop()

	// Load access token for token-mode auth
	s.loadAccessToken()

	// 初始化文件 workspace 并同步到 DB 索引
	s.initWorkspace()
	s.syncWorkspaceOnStartup()

	// 安装/更新 System Skills（内置技能）
	skills.InstallSystemSkills(s.db, s.config.HomeDir)

	// 统一恢复所有僵尸状态
	s.recoverAll()

	// 启动 preview session 清理 goroutine
	go s.cleanupPreviewSessions()

	// 启动 App 调度器（DB-poll based）
	s.appScheduler = NewAppScheduler(s)
	if err := s.appScheduler.Start(); err != nil {
		log.Printf("App Scheduler start failed: %v", err)
	}
	defer s.appScheduler.Stop()

	// Start Bridge Manager (Telegram bidirectional chat)
	dispatcher := bridge.NewDispatcher(s.db, s.chatStore, func(userID, scope string, session *chat.Session, message string, opts *bridge.ExecuteOptions) error {
		_, err := s.executeChatSession(userID, scope, session, message, nil, opts)
		return err
	})
	dispatcher.SetRestartFn(func(botToken, chatID string) {
		log.Printf("[Server] Restart requested via Telegram (chat %s)...", chatID)
		exe, exeErr := os.Executable()
		if exeErr != nil {
			log.Printf("[Server] Cannot find executable: %v", exeErr)
			return
		}
		// Write restart-notify file so the new process can send confirmation
		if botToken != "" && chatID != "" {
			notifyFile := filepath.Join(s.config.HomeDir, ".restart-notify")
			_ = os.WriteFile(notifyFile, []byte(botToken+"\n"+chatID), 0600)
		}
		// Fork detached shell: wait for us to die (release port), then start new daemon
		restartSh := fmt.Sprintf("sleep 2 && %s start --home %s --port %d", exe, s.config.HomeDir, s.config.Port)
		cmd := exec.Command("sh", "-c", restartSh)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if startErr := cmd.Start(); startErr != nil {
			log.Printf("[Server] Failed to schedule restart: %v", startErr)
			return
		}
		log.Println("[Server] Restart scheduled, exiting now...")
		daemon.RemovePID(s.config.HomeDir)
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	})
	s.bridgeManager = bridge.NewManager(s.db, dispatcher)
	s.bridgeManager.StartAll()
	log.Println("[Server] Bridge Manager started")
	defer s.bridgeManager.StopAll()

	// Check for pending restart notification
	s.sendRestartNotification()

	mux := http.NewServeMux()

	// CORS 中间件包装器
	corsHandler := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 设置 CORS 头
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Max-Age", "3600")

			// 处理预检请求
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}

			next.ServeHTTP(w, r)
		})
	}

	// 公开路由
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/stats", s.handleStats) // 工作池统计
	mux.HandleFunc("GET /api/v1/auth/setup_status", s.handleSetupStatus)
	mux.HandleFunc("POST /api/v1/auth/setup", s.handleSetupAdmin)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)

	// Webhook 触发端点（公开，不需要认证）
	mux.HandleFunc("POST /api/v1/hooks/{token}", s.handleWebhookTrigger)
	mux.HandleFunc("GET /api/v1/hooks/{token}", s.handleWebhookTrigger) // 支持 GET 触发

	// 受保护的 API 路由 (包裹 AuthMiddleware)
	mux.HandleFunc("GET /api/v1/auth/me", s.AuthMiddleware(s.handleGetMe))
	mux.HandleFunc("POST /api/v1/run", s.AuthMiddleware(s.handleRunWorkflow))
	mux.HandleFunc("GET /api/v1/executions", s.AuthMiddleware(s.handleListExecutions))
	mux.HandleFunc("GET /api/v1/executions/{id}", s.AuthMiddleware(s.handleGetExecution))
	mux.HandleFunc("GET /api/v1/executions/{id}/logs", s.AuthMiddleware(s.handleGetExecutionLogs))
	mux.HandleFunc("GET /api/v1/executions/{id}/artifacts", s.AuthMiddleware(s.handleListArtifacts))
	mux.HandleFunc("GET /api/v1/executions/{id}/artifacts/{filename}", s.AuthMiddleware(s.handleDownloadArtifact))

	// Global Artifacts
	mux.HandleFunc("GET /api/v1/artifacts", s.AuthMiddleware(s.handleListAllArtifacts))

	// Global File Library
	mux.HandleFunc("GET /api/v1/files", s.AuthMiddleware(s.handleListFilesGlobal))
	mux.HandleFunc("POST /api/v1/files", s.AuthMiddleware(s.handleUploadFileGlobal))
	mux.HandleFunc("GET /api/v1/files/{id}/preview", s.AuthMiddleware(s.handlePreviewFileGlobal))
	mux.HandleFunc("DELETE /api/v1/files/{id}", s.AuthMiddleware(s.handleDeleteFileGlobal))

	mux.HandleFunc("POST /api/v1/executions/{id}/nodes/{node_id}/approve", s.AuthMiddleware(s.handleApproveExecution))
	mux.HandleFunc("POST /api/v1/executions/{id}/cancel", s.AuthMiddleware(s.handleCancelExecution))

	// Workflow 管理路由
	mux.HandleFunc("GET /api/v1/workflows", s.AuthMiddleware(s.handleListWorkflows))
	mux.HandleFunc("GET /api/v1/workflows/{id}/schema", s.AuthMiddleware(s.handleGetWorkflowSchema))
	mux.HandleFunc("GET /api/v1/workflows/{name}", s.AuthMiddleware(s.handleGetWorkflow))
	mux.HandleFunc("POST /api/v1/workflows", s.AuthMiddleware(s.handleSaveWorkflow))
	mux.HandleFunc("POST /api/v1/workflows/validate", s.AuthMiddleware(s.handleValidateWorkflow))
	mux.HandleFunc("DELETE /api/v1/workflows/{name}", s.AuthMiddleware(s.handleDeleteWorkflow))

	// Workflow File Links (Symlink-based for CLI, Upload for Web)
	mux.HandleFunc("POST /api/v1/workflows/{id}/files", s.AuthMiddleware(s.handleCreateWorkflowFileLink))
	mux.HandleFunc("POST /api/v1/workflows/{id}/files/upload", s.AuthMiddleware(s.handleUploadWorkflowFile))
	mux.HandleFunc("DELETE /api/v1/workflows/{id}/files/{filename}", s.AuthMiddleware(s.handleDeleteWorkflowFileLink))

	// Webhook 管理路由（受保护）
	mux.HandleFunc("POST /api/v1/webhooks", s.AuthMiddleware(s.handleCreateWebhook))
	mux.HandleFunc("GET /api/v1/webhooks", s.AuthMiddleware(s.handleListWebhooks))
	mux.HandleFunc("DELETE /api/v1/webhooks/{id}", s.AuthMiddleware(s.handleDeleteWebhook))
	mux.HandleFunc("PUT /api/v1/webhooks/{id}", s.AuthMiddleware(s.handleToggleWebhook))

	// Cron 管理路由（受保护）
	mux.HandleFunc("POST /api/v1/crons", s.AuthMiddleware(s.handleCreateCronTrigger))
	mux.HandleFunc("GET /api/v1/crons", s.AuthMiddleware(s.handleListCronTriggers))
	mux.HandleFunc("PUT /api/v1/crons/{id}", s.AuthMiddleware(s.handleUpdateCronTrigger))
	mux.HandleFunc("DELETE /api/v1/crons/{id}", s.AuthMiddleware(s.handleDeleteCronTrigger))

	// Secret 管理路由
	mux.HandleFunc("POST /api/v1/secrets", s.AuthMiddleware(s.handleCreateSecret))
	mux.HandleFunc("GET /api/v1/secrets", s.AuthMiddleware(s.handleListSecrets))
	mux.HandleFunc("GET /api/v1/secrets/{name}", s.AuthMiddleware(s.handleGetSecret))
	mux.HandleFunc("DELETE /api/v1/secrets/{name}", s.AuthMiddleware(s.handleDeleteSecret))

	// Skills 管理路由
	mux.HandleFunc("GET /api/v1/skills/system", s.AuthMiddleware(s.handleListSystemSkills))
	mux.HandleFunc("GET /api/v1/skills/collection", s.AuthMiddleware(s.handleGetCollection))
	mux.HandleFunc("DELETE /api/v1/skills/collection", s.AuthMiddleware(s.handleDeleteCollection))
	mux.HandleFunc("GET /api/v1/skills", s.AuthMiddleware(s.handleListSkills))
	mux.HandleFunc("GET /api/v1/skills/{id}", s.AuthMiddleware(s.handleGetSkill))
	mux.HandleFunc("POST /api/v1/skills/create", s.AuthMiddleware(s.handleCreateSkill))
	mux.HandleFunc("PUT /api/v1/skills/{id}", s.AuthMiddleware(s.handleUpdateSkill))
	mux.HandleFunc("POST /api/v1/skills/install", s.AuthMiddleware(s.handleInstallSkill))
	mux.HandleFunc("POST /api/v1/skills/install-zip", s.AuthMiddleware(s.handleInstallSkillZip))
	mux.HandleFunc("POST /api/v1/skills/{id}/run", s.AuthMiddleware(s.handleRunSkill))
	mux.HandleFunc("POST /api/v1/skills/{id}/test", s.AuthMiddleware(s.handleTestSkill))
	mux.HandleFunc("POST /api/v1/skills/{id}/export", s.AuthMiddleware(s.handleExportSkill))
	mux.HandleFunc("DELETE /api/v1/skills/{id}", s.AuthMiddleware(s.handleDeleteSkill))
	mux.HandleFunc("GET /api/v1/skills/{id}/resources", s.AuthMiddleware(s.handleGetSkillResources))
	mux.HandleFunc("PUT /api/v1/skills/{id}/resources", s.AuthMiddleware(s.handlePutSkillResource))
	mux.HandleFunc("DELETE /api/v1/skills/{id}/resources", s.AuthMiddleware(s.handleDeleteSkillResource))

	// Skills Registry (搜索 skills.sh 生态)
	mux.HandleFunc("GET /api/v1/registry/search", s.AuthMiddleware(s.handleRegistrySearch))

	// Chat Sessions

	mux.HandleFunc("POST /api/v1/chat/sessions", s.AuthMiddleware(s.handleCreateChatSession))
	mux.HandleFunc("GET /api/v1/chat/sessions", s.AuthMiddleware(s.handleListChatSessions))
	mux.HandleFunc("GET /api/v1/chat/sessions/{id}", s.AuthMiddleware(s.handleGetChatSession))
	mux.HandleFunc("DELETE /api/v1/chat/sessions/{id}", s.AuthMiddleware(s.handleDeleteChatSession))
	mux.HandleFunc("PATCH /api/v1/chat/sessions/{id}", s.AuthMiddleware(s.handleUpdateChatSession))
	mux.HandleFunc("POST /api/v1/chat/sessions/{id}/messages", s.AuthMiddleware(s.handleChatMessage))
	mux.HandleFunc("POST /api/v1/chat/sessions/{id}/continue", s.AuthMiddleware(s.handleChatSessionContinue))
	mux.HandleFunc("POST /api/v1/chat/sessions/{id}/abort", s.AuthMiddleware(s.handleChatSessionAbort))

	// Settings / AI Key 管理
	mux.HandleFunc("GET /api/v1/settings/ai-keys", s.AuthMiddleware(s.handleListAIKeys))
	mux.HandleFunc("POST /api/v1/settings/ai-keys", s.AuthMiddleware(s.handleSetAIKey))
	mux.HandleFunc("DELETE /api/v1/settings/ai-keys/{provider}", s.AuthMiddleware(s.handleDeleteAIKey))
	mux.HandleFunc("GET /api/v1/settings/preferred-model", s.AuthMiddleware(s.handleGetPreferredModel))
	mux.HandleFunc("POST /api/v1/settings/preferred-model", s.AuthMiddleware(s.handleSetPreferredModel))
	mux.HandleFunc("GET /api/v1/settings/enabled-models", s.AuthMiddleware(s.handleGetEnabledModels))
	mux.HandleFunc("POST /api/v1/settings/enabled-models", s.AuthMiddleware(s.handleSetEnabledModels))
	mux.HandleFunc("GET /api/v1/models", s.AuthMiddleware(s.handleListModels))

	// Connectors — 统一多渠道 API
	mux.HandleFunc("GET /api/v1/connectors", s.AuthMiddleware(s.handleListConnectors))
	mux.HandleFunc("POST /api/v1/connectors", s.AuthMiddleware(s.handleCreateConnector))
	mux.HandleFunc("GET /api/v1/connectors/{id}", s.AuthMiddleware(s.handleGetConnector))
	mux.HandleFunc("DELETE /api/v1/connectors/{id}", s.AuthMiddleware(s.handleDeleteConnector))
	mux.HandleFunc("PUT /api/v1/connectors/{id}/toggle", s.AuthMiddleware(s.handleToggleConnector))
	mux.HandleFunc("POST /api/v1/connectors/{id}/verify", s.AuthMiddleware(s.handleConnectorVerify))
	mux.HandleFunc("GET /api/v1/connectors/{id}/verify-status", s.AuthMiddleware(s.handleConnectorVerifyStatus))
	mux.HandleFunc("GET /api/v1/connectors/{id}/receivers", s.AuthMiddleware(s.handleConnectorReceivers))
	mux.HandleFunc("DELETE /api/v1/connectors/{id}/receivers/{rid}", s.AuthMiddleware(s.handleDeleteConnectorReceiver))
	mux.HandleFunc("POST /api/v1/connectors/{id}/test", s.AuthMiddleware(s.handleConnectorTest))

	// App-Connector linking
	mux.HandleFunc("GET /api/v1/apps/{id}/connectors", s.AuthMiddleware(s.handleListAppConnectors))
	mux.HandleFunc("POST /api/v1/apps/{id}/connectors", s.AuthMiddleware(s.handleLinkAppConnector))
	mux.HandleFunc("DELETE /api/v1/apps/{id}/connectors/{cid}", s.AuthMiddleware(s.handleUnlinkAppConnector))

	// App 管理路由
	mux.HandleFunc("POST /api/v1/apps/parse-schedule", s.AuthMiddleware(s.handleParseSchedule))
	mux.HandleFunc("POST /api/v1/apps/manager/chat", s.AuthMiddleware(s.handleManagerChat))
	mux.HandleFunc("GET /api/v1/apps", s.AuthMiddleware(s.handleListApps))
	mux.HandleFunc("POST /api/v1/apps", s.AuthMiddleware(s.handleCreateApp))
	mux.HandleFunc("GET /api/v1/apps/{id}", s.AuthMiddleware(s.handleGetApp))
	mux.HandleFunc("PUT /api/v1/apps/{id}", s.AuthMiddleware(s.handleUpdateApp))
	mux.HandleFunc("DELETE /api/v1/apps/{id}", s.AuthMiddleware(s.handleDeleteApp))
	mux.HandleFunc("POST /api/v1/apps/{id}/activate", s.AuthMiddleware(s.handleActivateApp))
	mux.HandleFunc("POST /api/v1/apps/{id}/deactivate", s.AuthMiddleware(s.handleDeactivateApp))
	mux.HandleFunc("POST /api/v1/apps/{id}/run", s.AuthMiddleware(s.handleRunAppNow))
	mux.HandleFunc("GET /api/v1/apps/{id}/runs", s.AuthMiddleware(s.handleListAppRuns))
	mux.HandleFunc("GET /api/v1/apps/{id}/runs/{runId}", s.AuthMiddleware(s.handleGetAppRun))

	// Schedules
	mux.HandleFunc("GET /api/v1/schedules/upcoming", s.AuthMiddleware(s.handleGetUpcomingRuns))
	mux.HandleFunc("POST /api/v1/schedules/{runId}/skip", s.AuthMiddleware(s.handleSkipRun))

	// Admin 管理路由 (需要 admin 权限)
	mux.HandleFunc("GET /api/v1/admin/stats", s.AdminMiddleware(s.handleAdminGetStats))
	mux.HandleFunc("GET /api/v1/admin/users", s.AdminMiddleware(s.handleAdminListUsers))
	mux.HandleFunc("POST /api/v1/admin/users", s.AdminMiddleware(s.handleAdminCreateUser))
	mux.HandleFunc("DELETE /api/v1/admin/users/{id}", s.AdminMiddleware(s.handleAdminDeleteUser))
	mux.HandleFunc("GET /api/v1/admin/executions", s.AdminMiddleware(s.handleAdminListExecutions))
	mux.HandleFunc("GET /api/v1/admin/workflows", s.AdminMiddleware(s.handleAdminListWorkflows))
	mux.HandleFunc("GET /api/v1/admin/secrets", s.AdminMiddleware(s.handleAdminListSecrets))
	mux.HandleFunc("DELETE /api/v1/admin/secrets/{id}", s.AdminMiddleware(s.handleAdminDeleteSecret))
	mux.HandleFunc("GET /api/v1/admin/usage", s.AdminMiddleware(s.handleAdminGetUsage))

	// API Documentation
	s.registerDocsRoutes(mux)

	// 配置 Server（应用 CORS 中间件）
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", s.config.Port),
		Handler:      corsHandler(mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	serverStartedAt = time.Now()
	log.Printf("🚀 Tofi Server listening on port %d", s.config.Port)
	return srv.ListenAndServe()
}

// Build info — set by main package at startup via SetBuildInfo().
var (
	buildVersion   = "dev"
	buildTime      = "unknown"
	buildCommit    = "unknown"
	serverStartedAt time.Time
)

// SetBuildInfo sets version metadata from ldflags (called once at startup).
func SetBuildInfo(version, commit, built string) {
	buildVersion = version
	buildCommit = commit
	buildTime = built
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	uptime := time.Since(serverStartedAt).Truncate(time.Second)

	// Worker pool stats
	s.workerPool.mu.RLock()
	running := s.workerPool.runningCount
	queued := s.workerPool.queuedCount
	s.workerPool.mu.RUnlock()

	// Count apps
	totalApps, _ := s.db.CountAllApps()
	activeApps, _ := s.db.CountActiveApps()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"version": buildVersion,
		"commit":  buildCommit,
		"uptime":  uptime.String(),
		"workers": map[string]any{
			"max":     s.config.MaxConcurrentWorkflows,
			"running": running,
			"queued":  queued,
		},
		"apps": map[string]int{
			"total":  totalApps,
			"active": activeApps,
		},
	})
}

// sendRestartNotification checks for a .restart-notify file left by the previous
// process and sends a "restart complete" message to the Telegram user who requested it.
func (s *Server) sendRestartNotification() {
	notifyFile := filepath.Join(s.config.HomeDir, ".restart-notify")
	data, err := os.ReadFile(notifyFile)
	if err != nil {
		return // no pending notification
	}
	os.Remove(notifyFile)

	parts := strings.SplitN(string(data), "\n", 2)
	if len(parts) != 2 {
		return
	}
	botToken, chatID := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	if botToken == "" || chatID == "" {
		return
	}

	sender := &bridge.TelegramSender{BotToken: botToken}
	_ = sender.SendMessage(chatID, "✅ Tofi 服务已重启完成")
	log.Printf("[Server] Sent restart confirmation to Telegram chat %s", chatID)
}

// handleStats 返回工作池的统计信息
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats := s.workerPool.GetStats()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

