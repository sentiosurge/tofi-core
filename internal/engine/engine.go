package engine

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
	"tofi-core/internal/engine/base"
	"tofi-core/internal/engine/data"
	"tofi-core/internal/engine/logic"
	"tofi-core/internal/engine/tasks"
	"tofi-core/internal/models"
)

// GetAction 工厂函数：将节点类型映射到对应的子包实现
func GetAction(nodeType string) Action {
	switch nodeType {
	case "shell":
		return &tasks.Shell{}
	case "ai":
		return &tasks.AI{}
	case "api":
		return &tasks.API{}
	case "if":
		return &logic.If{}
	case "var", "const":
		return &data.Var{}
	case "secret":
		return &data.Secret{}
	default:
		return &base.Virtual{}
	}
}

// RunNode 核心辐射引擎：包含并发控制、依赖检查、错误传播和执行记录
func RunNode(wf *models.Workflow, nodeID string, ctx *models.ExecutionContext) {
	// 1. 生命周期管理：无论如何退出，计数器都要减 1
	defer ctx.Wg.Done()

	node, exists := wf.Nodes[nodeID]
	// 防重拦截
	if !exists || ctx.CheckAndSetStarted(nodeID) {
		return
	}

	// 2. 依赖检查与状态传播 (Skip Logic)
	for _, depID := range node.Dependencies {
		res, completed := ctx.GetResult(depID)
		if !completed {
			log.Printf("[%s] [WAIT]    [%s] Waiting for: %s", ctx.ExecutionID, node.ID, depID)
			return
		}

		// 【传播逻辑】：如果父节点是“失败”或“跳过”
		if strings.HasPrefix(res, "ERR_PROPAGATION:") || strings.HasPrefix(res, "SKIPPED_BY:") {
			skipMsg := fmt.Sprintf("SKIPPED_BY: %s", depID)

			ctx.RecordStat(models.NodeStat{
				NodeID: node.ID, Type: node.Type, Status: "SKIP", Duration: 0, StartTime: time.Now(),
			})

			ctx.SetResult(node.ID, skipMsg)
			log.Printf("[%s] [SKIP]    [%s] 由于上游失败自动跳过", ctx.ExecutionID, node.ID)

			// 递归跳过后续
			for _, nextID := range node.Next {
				ctx.Wg.Add(1)
				go RunNode(wf, nextID, ctx)
			}
			return
		}
	}

	// 3. 执行准备
	action := GetAction(node.Type)
	log.Printf("[%s] [START]   [%s] 类型: %s", ctx.ExecutionID, node.ID, node.Type)

	startTime := time.Now()
	var res string
	var err error

	// 4. 执行阶段 (包含重试)
	for i := 0; i <= node.RetryCount; i++ {
		if i > 0 {
			log.Printf("[%s] [RETRY]   [%s] 第 %d 次重试...", ctx.ExecutionID, node.ID, i)
		}
		res, err = action.Execute(node, ctx)
		if err == nil {
			break
		}
	}
	duration := time.Since(startTime)

	// 5. 结果处理与统计
	stat := models.NodeStat{
		NodeID:    node.ID,
		Type:      node.Type,
		StartTime: startTime,
		Duration:  duration,
	}

	if err != nil {
		// 区分“逻辑不通过”和“真正的执行错误”
		if err.Error() == "CONDITION_NOT_MET" {
			stat.Status = "SKIP"
			ctx.RecordStat(stat)
			log.Printf("[%s] [SKIP]    [%s] 条件不满足", ctx.ExecutionID, node.ID)
			// 条件不满足也需要向下传递 SKIP 信号
			ctx.SetResult(node.ID, "SKIPPED_BY_LOGIC")
		} else {
			stat.Status = "ERROR"
			ctx.RecordStat(stat)
			log.Printf("[%s] [ERROR]   [%s] 执行失败: %v", ctx.ExecutionID, node.ID, err)
			ctx.SetResult(node.ID, fmt.Sprintf("ERR_PROPAGATION: %v", err))
		}

		// 触发失败分支或后续跳转
		nextQueue := node.Next
		if len(node.OnFailure) > 0 {
			nextQueue = node.OnFailure
		}
		for _, nextID := range nextQueue {
			ctx.Wg.Add(1)
			go RunNode(wf, nextID, ctx)
		}
		return
	}

	// 成功逻辑
	// 成功路径
	stat.Status = "SUCCESS"
	ctx.RecordStat(stat)
	ctx.SetResult(node.ID, res) // 内部依然存入真实值，保证后续节点能正常用

	// --- 核心改动：日志脱敏 ---
	displayRes := res
	if node.Type == "secret" {
		var secretData map[string]interface{}
		// 尝试解析存入的 JSON 结果
		if err := json.Unmarshal([]byte(res), &secretData); err == nil {
			var keys []string
			for k := range secretData {
				// 按照你的要求，格式化为 **nodeID.key**
				keys = append(keys, fmt.Sprintf("**%s.%s**", node.ID, k))
			}
			// 用逗号连接多个 key
			displayRes = strings.Join(keys, ", ")
		} else {
			// 如果不是 JSON（单值模式），直接显示节点 ID
			displayRes = fmt.Sprintf("**%s**", node.ID)
		}
	}

	log.Printf("[%s] [SUCCESS] [%s] 输出: %s", ctx.ExecutionID, node.ID, displayRes)

	for _, nextID := range node.Next {
		ctx.Wg.Add(1)
		go RunNode(wf, nextID, ctx)
	}
}
