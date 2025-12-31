package engine

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
	"tofi-core/internal/engine/base"
	"tofi-core/internal/engine/data"
	"tofi-core/internal/engine/logic"
	"tofi-core/internal/engine/tasks"
	"tofi-core/internal/models"

	"github.com/Knetic/govaluate"
)

// getLogPrefix 根据 ExecutionID 返回日志前缀符号
// 主工作流使用空前缀，子工作流使用 "  └─" 缩进符号
func getLogPrefix(executionID string) string {
	if strings.Contains(executionID, "/") {
		// 计算嵌套层级
		depth := strings.Count(executionID, "/")
		indent := strings.Repeat("  ", depth)
		return indent + "└─ "
	}
	return ""
}

// init 包初始化函数，注入依赖
func init() {
	// 为 Loop 注入 GetAction 函数，解决循环依赖
	logic.SetActionGetter(func(nodeType string) logic.Action {
		return GetAction(nodeType)
	})

	// 为 Handoff 注入 Start 函数，解决循环依赖
	tasks.SetWorkflowStarter(Start)
}

// GetAction 工厂函数：将节点类型映射到对应的子包实现
func GetAction(nodeType string) Action {
	switch nodeType {
	case "shell":
		return &tasks.Shell{}
	case "ai":
		return &tasks.AI{}
	case "api":
		return &tasks.API{}
	case "workflow":
		return &tasks.Handoff{}
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
	case "loop":
		return &logic.Loop{}
	case "var", "const":
		return &data.Var{}
	case "secret":
		return &data.Secret{}
	default:
		return &base.Virtual{}
	}
}

// ValidateAll 验证整个工作流的所有节点
func ValidateAll(wf *models.Workflow) error {
	var errs []string
	for id, node := range wf.Nodes {
		action := GetAction(node.Type)
		if err := action.Validate(node); err != nil {
			errs = append(errs, fmt.Sprintf("Node '%s' (%s): %v", id, node.Type, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("workflow validation failed:\n%s", strings.Join(errs, "\n"))
	}
	return nil
}

// RunNode 核心辐射引擎：包含并发控制、依赖检查、错误传播和执行记录
func RunNode(wf *models.Workflow, nodeID string, ctx *models.ExecutionContext) {
	// 1. 生命周期管理：无论如何退出，计数器都要减 1
	defer ctx.Wg.Done()

	node, exists := wf.Nodes[nodeID]
	if !exists {
		return
	}

	// 0. Resume 检查：如果我已经有结果了（从磁盘恢复），直接跳过
	if res, completed := ctx.GetResult(nodeID); completed {
		log.Printf("[%s] [RESUME]  [%s] 已从状态恢复，跳过执行 (结果: %s)", ctx.ExecutionID, node.ID, res)
		// 仍然需要触发后续节点！因为后续节点可能还没跑
		for _, nextID := range node.Next {
			ctx.Wg.Add(1)
			go RunNode(wf, nextID, ctx)
		}
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

		// 【传播逻辑】：如果父节点是"失败"或"跳过"
		if strings.HasPrefix(res, "ERR_PROPAGATION:") || strings.HasPrefix(res, "SKIPPED_BY:") {
			// 🆕 关键修复：run_if 导致的 SKIP 不传播
			// 这允许分支汇聚：F 可以依赖 [C, D]，即使 D 被 run_if 跳过
			if res == "SKIPPED_BY: run_if" {
				log.Printf("[%s] [DEBUG]   [%s] 忽略依赖 %s 的 run_if SKIP（条件性跳过不传播）",
					ctx.ExecutionID, node.ID, depID)
				continue // 忽略这个依赖的条件性跳过
			}

			// 关键修复：检查我是不是父节点的"救生员"（OnFailure分支）
			// 如果是，那么父节点的失败正是我启动的原因，不能跳过！
			parentNode, ok := wf.Nodes[depID]
			isRescue := false
			if ok {
				for _, failID := range parentNode.OnFailure {
					if failID == node.ID {
						isRescue = true
						break
					}
				}
			}

			// 如果不是救生员，才执行跳过逻辑
			if !isRescue {
				// 只有第一个到达这里的协程负责标记 SKIP 并传播
				if ctx.CheckAndSetStarted(nodeID) {
					return
				}

				skipMsg := fmt.Sprintf("SKIPPED_BY: %s", depID)
				ctx.RecordStat(models.NodeStat{
					NodeID: node.ID, Type: node.Type, Status: "SKIP", Duration: 0, StartTime: time.Now(),
				})
				ctx.SetResult(node.ID, skipMsg)
				SaveState(ctx) // 持久化 Skip 状态
				log.Printf("[%s] [SKIP]    [%s] 由于上游失败自动跳过", ctx.ExecutionID, node.ID)

				for _, nextID := range node.Next {
					ctx.Wg.Add(1)
					go RunNode(wf, nextID, ctx)
				}
				return
			}
			// 如果是 Rescue，继续执行（就像收到 Success 一样）
		}
	}

	// 3. 核心修正点：只有依赖全部满足后，才尝试抢占执行权
	if ctx.CheckAndSetStarted(nodeID) {
		return
	}

	// 3.5 run_if 条件检查
	if node.RunIf != "" {
		// 先做模板替换，支持 {{variable}} 语法（保持与其他字段一致）
		expandedRunIf := ctx.ReplaceParams(node.RunIf)

		// 构造参数集 (尝试将 "true"/"false" 字符串转为 bool 以方便计算)
		params := make(map[string]interface{})
		resultsSnap, _ := ctx.Snapshot()
		for k, v := range resultsSnap {
			if v == "true" {
				params[k] = true
			} else if v == "false" {
				params[k] = false
			} else {
				params[k] = v
			}
		}

		expr, err := govaluate.NewEvaluableExpression(expandedRunIf)
		if err != nil {
			// 语法错误 -> Fail Closed (SKIP 节点)
			stat := models.NodeStat{
				NodeID: node.ID, Type: node.Type, Status: "SKIP", Duration: 0, StartTime: time.Now(),
			}
			ctx.RecordStat(stat)
			ctx.SetResult(node.ID, fmt.Sprintf("SKIPPED_BY: run_if 语法错误 (%v)", err))
			SaveState(ctx)
			log.Printf("[%s] [SKIP]    [%s] run_if 语法错误: %v (原始: %s, 展开: %s)",
				ctx.ExecutionID, node.ID, err, node.RunIf, expandedRunIf)

			// 传播 Skip 信号
			for _, nextID := range node.Next {
				ctx.Wg.Add(1)
				go RunNode(wf, nextID, ctx)
			}
			return
		}

		result, err := expr.Evaluate(params)
		if err != nil {
			// 计算出错 -> Fail Closed (SKIP 节点)
			stat := models.NodeStat{
				NodeID: node.ID, Type: node.Type, Status: "SKIP", Duration: 0, StartTime: time.Now(),
			}
			ctx.RecordStat(stat)
			ctx.SetResult(node.ID, fmt.Sprintf("SKIPPED_BY: run_if 计算出错 (%v)", err))
			SaveState(ctx)
			log.Printf("[%s] [SKIP]    [%s] run_if 计算出错: %v (表达式: %s)",
				ctx.ExecutionID, node.ID, err, expandedRunIf)

			// 传播 Skip 信号
			for _, nextID := range node.Next {
				ctx.Wg.Add(1)
				go RunNode(wf, nextID, ctx)
			}
			return
		}

		if shouldRun, ok := result.(bool); ok && !shouldRun {
			// 条件不满足 -> SKIP
			stat := models.NodeStat{
				NodeID: node.ID, Type: node.Type, Status: "SKIP", Duration: 0, StartTime: time.Now(),
			}
			ctx.RecordStat(stat)
			ctx.SetResult(node.ID, "SKIPPED_BY: run_if")
			SaveState(ctx)
			log.Printf("[%s] [SKIP]    [%s] run_if 条件不满足 (%s)", ctx.ExecutionID, node.ID, expandedRunIf)

			// 传播 Skip 信号
			for _, nextID := range node.Next {
				ctx.Wg.Add(1)
				go RunNode(wf, nextID, ctx)
			}
			return
		}
	}

	// 4. 执行准备
	action := GetAction(node.Type)
	prefix := getLogPrefix(ctx.ExecutionID)
	runtimeID := node.GetRuntimeID()
	log.Printf("%s[%s] [START]   [%s] 类型: %s", prefix, ctx.ExecutionID, runtimeID, node.Type)

	// --- 🆕 核心规范重构：解析局部作用域 ---
	var resolvedConfig map[string]interface{}
	var err error

	if node.Type == "var" || node.Type == "const" {
		// Var 节点特殊处理：直接对 Config 进行全局变量替换
		// 构造临时 Config 以包含 Value 字段
		effectiveConfig := make(map[string]interface{})
		if node.Config != nil {
			for k, v := range node.Config {
				effectiveConfig[k] = v
			}
		}
		if node.Value != nil {
			effectiveConfig["value"] = node.Value
		}

		resolvedVal := ctx.ReplaceParamsAny(effectiveConfig)
		if v, ok := resolvedVal.(map[string]interface{}); ok {
			resolvedConfig = v
		} else {
			resolvedConfig = effectiveConfig
		}
	} else {
		// 第一阶段：Global -> Local Context
		var localContext map[string]interface{}
		localContext, err = models.ResolveLocalContext(node, ctx)
		if err != nil {
			log.Printf("%s[%s] [ERROR]   [%s] Input 解析失败: %v", prefix, ctx.ExecutionID, runtimeID, err)
			ctx.SetResult(nodeID, fmt.Sprintf("ERR_PROPAGATION: Input resolution failed: %v", err))
			ctx.RecordStat(models.NodeStat{NodeID: runtimeID, Type: node.Type, Status: "ERROR", StartTime: time.Now()})
			SaveState(ctx)
			// 触发失败逻辑
			triggerNext(wf, node, ctx)
			return
		}

		// 第二阶段：Local Context -> Config
		resolvedConfig, err = models.ResolveConfig(node.Config, localContext, ctx)
		if err != nil {
			log.Printf("%s[%s] [ERROR]   [%s] Config 解析失败: %v", prefix, ctx.ExecutionID, runtimeID, err)
			ctx.SetResult(nodeID, fmt.Sprintf("ERR_PROPAGATION: Config resolution failed: %v", err))
			ctx.RecordStat(models.NodeStat{NodeID: runtimeID, Type: node.Type, Status: "ERROR", StartTime: time.Now()})
			SaveState(ctx)
			triggerNext(wf, node, ctx)
			return
		}
	}
	// ------------------------------------

	startTime := time.Now()
	var res string

	// 5. 执行阶段 (包含重试)
	for i := 0; i <= node.RetryCount; i++ {
		if i > 0 {
			log.Printf("%s[%s] [RETRY]   [%s] 第 %d 次重试...", prefix, ctx.ExecutionID, runtimeID, i)
		}
		res, err = action.Execute(resolvedConfig, ctx)
		if err == nil {
			break
		}
	}
	duration := time.Since(startTime)

	// 6. 结果处理与统计
	stat := models.NodeStat{
		NodeID:    runtimeID,
		Type:      node.Type,
		StartTime: startTime,
		Duration:  duration,
	}

	if err != nil {
		if err.Error() == "CONDITION_NOT_MET" {
			stat.Status = "SKIP"
			ctx.RecordStat(stat)
			log.Printf("%s[%s] [SKIP]    [%s] 条件不满足", prefix, ctx.ExecutionID, runtimeID)
			ctx.SetResult(nodeID, "SKIPPED_BY_LOGIC")
		} else {
			stat.Status = "ERROR"
			ctx.RecordStat(stat)
			log.Printf("%s[%s] [ERROR]   [%s] 执行失败: %v", prefix, ctx.ExecutionID, runtimeID, err)
			ctx.SetResult(nodeID, fmt.Sprintf("ERR_PROPAGATION: %v", err))
		}

		// 状态持久化
		SaveState(ctx)
		triggerNext(wf, node, ctx)
		return
	}

	// 7. 成功逻辑
	stat.Status = "SUCCESS"
	ctx.RecordStat(stat)
	ctx.SetResult(nodeID, res)

	// 状态持久化
	SaveState(ctx)

	log.Printf("%s[%s] [SUCCESS] [%s] 输出: %s",
		prefix,
		ctx.ExecutionID,
		runtimeID,
		ctx.MaskLog(res),
	)
	for _, nextID := range node.Next {
		ctx.Wg.Add(1)
		go RunNode(wf, nextID, ctx)
	}
}

// 辅助函数：触发后续节点
func triggerNext(wf *models.Workflow, node *models.Node, ctx *models.ExecutionContext) {
	nextQueue := node.Next
	if len(node.OnFailure) > 0 {
		nextQueue = node.OnFailure
	}
	for _, nextID := range nextQueue {
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

// InitializeGlobals 解析顶层的 data 和 secrets 并注入 Context
// 它会根据 wf 的声明，从 inputs (结构化) 中提取值并覆盖默认配置
func InitializeGlobals(wf *models.Workflow, ctx *models.ExecutionContext, inputs map[string]interface{}) {
	// 提取输入的 data 和 secrets 部分
	inputData := make(map[string]interface{})
	if d, ok := inputs["data"].(map[string]interface{}); ok {
		inputData = d
	}
	
	inputSecrets := make(map[string]interface{})
	if s, ok := inputs["secrets"].(map[string]interface{}); ok {
		inputSecrets = s
	}

	// 1. 处理 Data (聚合到 data 命名空间)
	if len(wf.Data) > 0 {
		dataMap := make(map[string]interface{})
		for k, defaultVal := range wf.Data {
			var finalVal interface{} = defaultVal
			
			// 覆盖逻辑
			if override, ok := inputData[k]; ok {
				finalVal = override
			}
			dataMap[k] = finalVal
		}
		
		// 序列化并存入名为 "data" 的虚拟节点结果中
		jb, _ := json.Marshal(dataMap)
		ctx.SetResult("data", string(jb))
	}

	// 2. 处理 Secrets (聚合到 secrets 命名空间)
	if len(wf.Secrets) > 0 {
		secretsMap := make(map[string]string)
		for k, source := range wf.Secrets {
			var realValue string

			// 2.1 优先检查外部注入
			if override, ok := inputSecrets[k].(string); ok && override != "" {
				realValue = override
			} else {
				// 2.2 按照声明的 source 加载
				if len(source) > 7 && source[:6] == "{{env." && source[len(source)-2:] == "}}" {
					realValue = os.Getenv(source[6 : len(source)-2])
				} else {
					realValue = source
				}
			}

			secretsMap[k] = realValue
			if realValue != "" {
				ctx.AddSecretValue(realValue)
			}
		}
		
		jb, _ := json.Marshal(secretsMap)
		ctx.SetResult("secrets", string(jb))
	}
}

// Start 封装了工作流的合法起点启动逻辑
func Start(wf *models.Workflow, ctx *models.ExecutionContext, inputs map[string]interface{}) {
	// 0. 预加载全局数据 (强契约模式)
	InitializeGlobals(wf, ctx, inputs)

	for id, node := range wf.Nodes {
		// 只有 0 依赖，且不是任何节点的“失败分支”，才是合法的起点
		if len(node.Dependencies) == 0 && !isFailureBranch(wf, id) {
			ctx.Wg.Add(1)
			go RunNode(wf, id, ctx)
		}
	}
}
