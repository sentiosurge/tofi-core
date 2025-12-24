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
	case "check":
		return &logic.Check{}
	case "text":
		return &logic.Text{}
	case "math":
		return &logic.Math{}
	case "list":
		return &logic.List{}
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
	if !exists {
		return
	}

	// 2. 依赖检查与状态传播 (Skip Logic)
	// 注意：这里先不标记 CheckAndSetStarted，允许多个父节点协程进来检查
	for _, depID := range node.Dependencies {
		res, completed := ctx.GetResult(depID)
		if !completed {
			log.Printf("[%s] [WAIT]    [%s] Waiting for: %s", ctx.ExecutionID, node.ID, depID)
			return
		}

		// 【传播逻辑】：如果父节点是“失败”或“跳过”
		if strings.HasPrefix(res, "ERR_PROPAGATION:") || strings.HasPrefix(res, "SKIPPED_BY:") {
			// 只有第一个到达这里的协程负责标记 SKIP 并传播
			if ctx.CheckAndSetStarted(nodeID) {
				return
			}

			skipMsg := fmt.Sprintf("SKIPPED_BY: %s", depID)
			ctx.RecordStat(models.NodeStat{
				NodeID: node.ID, Type: node.Type, Status: "SKIP", Duration: 0, StartTime: time.Now(),
			})
			ctx.SetResult(node.ID, skipMsg)
			log.Printf("[%s] [SKIP]    [%s] 由于上游失败自动跳过", ctx.ExecutionID, node.ID)

			for _, nextID := range node.Next {
				ctx.Wg.Add(1)
				go RunNode(wf, nextID, ctx)
			}
			return
		}
	}

	// 3. 核心修正点：只有依赖全部满足后，才尝试抢占执行权
	if ctx.CheckAndSetStarted(nodeID) {
		return
	}

	// 4. 执行准备
	action := GetAction(node.Type)
	log.Printf("[%s] [START]   [%s] 类型: %s", ctx.ExecutionID, node.ID, node.Type)

	startTime := time.Now()
	var res string
	var err error

	// 5. 执行阶段 (包含重试)
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

	// 6. 结果处理与统计
	stat := models.NodeStat{
		NodeID:    node.ID,
		Type:      node.Type,
		StartTime: startTime,
		Duration:  duration,
	}

	if err != nil {
		if err.Error() == "CONDITION_NOT_MET" {
			stat.Status = "SKIP"
			ctx.RecordStat(stat)
			log.Printf("[%s] [SKIP]    [%s] 条件不满足", ctx.ExecutionID, node.ID)
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

	// 7. 成功逻辑
	stat.Status = "SUCCESS"
	ctx.RecordStat(stat)
	ctx.SetResult(node.ID, res)

	// 日志脱敏处理
	displayRes := res
	if node.Type == "secret" {
		var secretData map[string]interface{}
		if err := json.Unmarshal([]byte(res), &secretData); err == nil {
			for _, v := range secretData {
				ctx.AddSecretValue(fmt.Sprint(v)) // 👈 存入黑名单
			}
		}
	}

	log.Printf("[%s] [SUCCESS] [%s] 输出: %s",
		ctx.ExecutionID,
		node.ID,
		ctx.MaskLog(displayRes),
	)
	for _, nextID := range node.Next {
		ctx.Wg.Add(1)
		go RunNode(wf, nextID, ctx)
	}
}

// isFailureBranch 检查某个节点是否被任何其他节点定义为 OnFailure 分支
func isFailureBranch(wf *models.Workflow, targetID string) bool {
	for _, node := range wf.Nodes {
		for _, failID := range node.OnFailure {
			if failID == targetID {
				return true
			}
		}
	}
	return false
}

// Start 封装了工作流的合法起点启动逻辑
func Start(wf *models.Workflow, ctx *models.ExecutionContext) {
	for id, node := range wf.Nodes {
		// 只有 0 依赖，且不是任何节点的“失败分支”，才是合法的起点
		if len(node.Dependencies) == 0 && !isFailureBranch(wf, id) {
			ctx.Wg.Add(1)
			go RunNode(wf, id, ctx)
		}
	}
}
