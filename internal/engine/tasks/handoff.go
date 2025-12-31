package tasks

import (
	"encoding/json"
	"fmt"
	"strings"
	"tofi-core/internal/models"
	"tofi-core/internal/parser"
	"tofi-core/internal/toolbox"
)

type Handoff struct{}

var workflowStarter func(*models.Workflow, *models.ExecutionContext)

func SetWorkflowStarter(starter func(*models.Workflow, *models.ExecutionContext)) {
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

	// 2. 安全提取参数 (避免 fmt.Sprint 产生 "<nil>")
	actionName, _ := config["action"].(string)
	filePath, _ := config["file"].(string)

	// 3. 决定从哪加载子工作流
	if actionName != "" {
		if strings.HasPrefix(actionName, "tofi/") {
			// 官方内置组件
			name := strings.TrimPrefix(actionName, "tofi/")
			data, err := toolbox.ReadAction(name)
			if err != nil {
				return "", err
			}
			childWf, err = parser.ParseWorkflowFromBytes(data, "yaml")
			if err != nil {
				return "", err
			}
		} else {
			// 暂不支持其他类型的 Action，但不再强制报错前缀
			return "", fmt.Errorf("unsupported action type: %s (only tofi/... is supported currently)", actionName)
		}
	} else if filePath != "" {
		// 从本地文件加载
		childWf, err = parser.LoadWorkflow(filePath)
		if err != nil {
			return "", err
		}
	} else {
		return "", fmt.Errorf("either 'action' or 'file' must be specified in workflow/handoff task")
	}

	// 4. 创建隔离的子上下文
	childCtx := models.NewExecutionContext(ctx.ExecutionID+"/handoff", ctx.Paths.Home)
	childCtx.Depth = ctx.Depth + 1 // 递增深度
	childCtx.WorkflowName = childWf.Name

	// 继承脱敏词
	for _, s := range ctx.SecretValues {
		childCtx.AddSecretValue(s)
	}

	// 传递输入
	inputsJSON, _ := json.Marshal(config)
	childCtx.SetResult("inputs", string(inputsJSON))

	// 5. 启动子工作流
	if workflowStarter == nil {
		return "", fmt.Errorf("workflowStarter not initialized")
	}
	workflowStarter(childWf, childCtx)
	childCtx.Wg.Wait() // 同步等待子工作流结束

	// 6. 收集并返回结果
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
