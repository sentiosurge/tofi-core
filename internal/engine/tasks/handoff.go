package tasks

import (
	"encoding/json"
	"fmt"
	"tofi-core/internal/models"
	"tofi-core/internal/parser"
)

type Handoff struct{}

var workflowStarter func(*models.Workflow, *models.ExecutionContext, map[string]interface{})

func SetWorkflowStarter(starter func(*models.Workflow, *models.ExecutionContext, map[string]interface{})) {
	workflowStarter = starter
}

func (h *Handoff) Execute(config map[string]interface{}, ctx *models.ExecutionContext) (string, error) {
	var childWf *models.Workflow
	var err error

	// 1. 检查递归深度 (防止死循环)
	const MaxDepth = 10
	if ctx.Depth >= MaxDepth {
		return "", fmt.Errorf("exceeded maximum workflow recursion depth (%d)", MaxDepth)
	}

	// 2. 提取寻址参数
	usesID, _ := config["uses"].(string)
	workflowID, _ := config["workflow"].(string) // 兼容旧逻辑
	actionName, _ := config["action"].(string)   // 兼容旧逻辑
	filePath, _ := config["file"].(string)       // 兼容旧逻辑

	// 3. 智能解析
	if usesID != "" {
		childWf, err = parser.ResolveWorkflow(usesID, "workflows")
	} else if workflowID != "" {
		childWf, err = parser.ResolveWorkflow(workflowID, "workflows")
	} else if actionName != "" {
		childWf, err = parser.ResolveWorkflow(actionName, "workflows")
	} else if filePath != "" {
		childWf, err = parser.LoadWorkflow(filePath)
	} else {
		return "", fmt.Errorf("missing 'uses' (or workflow/action/file) in handoff task")
	}

	if err != nil || childWf == nil {
		return "", fmt.Errorf("failed to resolve workflow: %v", err)
	}

	// 4. 创建隔离的子上下文
	childCtx := models.NewExecutionContext(ctx.ExecutionID+"/handoff", ctx.Paths.Home)
	childCtx.Depth = ctx.Depth + 1 
	childCtx.WorkflowName = childWf.Name

	// 5. 准备输入载荷 (Payload) - 结构化传递
	payload := make(map[string]interface{})
	
	if d, ok := config["data"].(map[string]interface{}); ok {
		payload["data"] = d
	}
	if s, ok := config["secrets"].(map[string]interface{}); ok {
		payload["secrets"] = s
	}
	// 兼容旧的扁平化参数 (可选，为了平滑过渡，如果新字段不存在，尝试从 config 提取非保留字段)
	// 但为了强契约，我们这里严格只取 data 和 secrets

	// 6. 启动子工作流
	if workflowStarter == nil {
		return "", fmt.Errorf("workflowStarter not initialized")
	}
	workflowStarter(childWf, childCtx, payload)
	childCtx.Wg.Wait()

	// 7. 收集并返回结果
	finalOutputs := make(map[string]interface{})
	for k, v := range childCtx.Results {
		var jsonObj interface{}
		if err := json.Unmarshal([]byte(v), &jsonObj); err == nil {
			finalOutputs[k] = jsonObj
		} else {
			finalOutputs[k] = v
		}
	}

	res, _ := json.Marshal(finalOutputs)
	return string(res), nil
}

func (h *Handoff) Validate(n *models.Node) error {
	return nil
}