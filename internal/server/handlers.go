package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
	"tofi-core/internal/engine"
	"tofi-core/internal/models"
	"tofi-core/internal/parser"

	"github.com/google/uuid"
)

type RunResponse struct {
	ExecutionID string `json:"execution_id"`
	Status      string `json:"status"`
	Message     string `json:"message"`
}

func (s *Server) handleRunWorkflow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 1. 读取请求体 (YAML or JSON)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if len(body) == 0 {
		http.Error(w, "Empty body", http.StatusBadRequest)
		return
	}

	// 2. 解析工作流
	// 假设默认格式是 YAML (或者我们可以根据 Content-Type 判断)
	// parser.ParseWorkflowFromBytes 支持自动识别 JSON/YAML
	format := "yaml"
	contentType := r.Header.Get("Content-Type")
	if contentType == "application/json" {
		format = "json"
	}

	wf, err := parser.ParseWorkflowFromBytes(body, format)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid workflow format: %v", err), http.StatusBadRequest)
		return
	}

	// 3. 验证工作流
	if err := engine.ValidateAll(wf); err != nil {
		http.Error(w, fmt.Sprintf("Workflow validation failed: %v", err), http.StatusBadRequest)
		return
	}

	// 4. 初始化 Context
	uuidStr := uuid.New().String()[:4]
	execID := time.Now().Format("102150405") + "-" + uuidStr
	ctx := models.NewExecutionContext(execID, s.config.HomeDir)

	// 5. 准备环境 (Logs 等)
	// Server 模式下，日志可能需要更精细的管理。这里暂时复用 CLI 的文件日志逻辑。
	if err := os.MkdirAll(ctx.Paths.Logs, 0755); err != nil {
		log.Printf("Failed to create log dir for %s: %v", execID, err)
	}
	// 注意：这里我们不重定向 Server 的 stdout，只记录到文件
	// 实际生产中建议使用结构化日志库 (Zap/Logrus)

	// 6. 异步启动执行
	// 我们启动一个 Goroutine 来充当这个 Execution 的"守护进程"
	go func() {
		// 恐慌捕获 (Panic Recovery) - 保护 Server 主进程
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[%s] PANIC RECOVERED: %v", execID, r)
			}
		}()

		log.Printf("[%s] 🚀 Execution Started via API", execID)
		
		engine.Start(wf, ctx)
		ctx.Wg.Wait() // 等待所有节点完成

		// 保存报告
		if err := engine.SaveReport(wf, ctx); err != nil {
			log.Printf("[%s] Failed to save report: %v", execID, err)
		} else {
			log.Printf("[%s] Report saved", execID)
		}
		
		// 打印简要总结到 Server Log
		log.Printf("[%s] 🏁 Execution Finished", execID)
	}()

	// 7. 返回响应
	resp := RunResponse{
		ExecutionID: execID,
		Status:      "started",
		Message:     "Workflow execution initiated successfully",
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted) // 202 Accepted 表示已接受处理但未完成
	json.NewEncoder(w).Encode(resp)
}
