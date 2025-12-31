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
	workflowID, _ := config["workflow"].(string)
	actionName, _ := config["action"].(string)
	filePath, _ := config["file"].(string)

	// 3. 智能解析
	if workflowID != "" {
		childWf, err = parser.ResolveWorkflow(workflowID, "workflows")
	} else if actionName != "" {
		childWf, err = parser.ResolveWorkflow(actionName, "workflows")
	} else if filePath != "" {
		childWf, err = parser.LoadWorkflow(filePath)
	} else {
		return "", fmt.Errorf("missing 'workflow' ID in handoff task")
	}

	if err != nil || childWf == nil {
		return "", fmt.Errorf("failed to resolve workflow: %v", err)
	}

	// 4. 创建隔离的子上下文
	childCtx := models.NewExecutionContext(ctx.ExecutionID+"/handoff", ctx.Paths.Home)
	childCtx.Depth = ctx.Depth + 1 
	childCtx.WorkflowName = childWf.Name

	// 5. 准备输入载荷 (Payload)
	payload := make(map[string]interface{})
	for k, v := range config {
		if k == "workflow" || k == "action" || k == "file" {
			continue
		}
		payload[k] = v
	}

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