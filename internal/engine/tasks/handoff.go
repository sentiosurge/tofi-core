package tasks

import (
	"encoding/json"
	"fmt"
	"strings"
	actionlib "tofi-core/action_library"
	"tofi-core/internal/models"
	"tofi-core/internal/parser"
)

type Handoff struct{}

var workflowStarter func(*models.Workflow, *models.ExecutionContext)

func SetWorkflowStarter(starter func(*models.Workflow, *models.ExecutionContext)) {
	workflowStarter = starter
}

func (h *Handoff) Execute(config map[string]interface{}, ctx *models.ExecutionContext) (string, error) {
	var childWf *models.Workflow
	var err error

	var actionName string
	if v, ok := config["action"]; ok && v != nil {
		actionName = fmt.Sprint(v)
	}

	var filePath string
	if v, ok := config["file"]; ok && v != nil {
		filePath = fmt.Sprint(v)
	}

	if actionName != "" {
		if strings.HasPrefix(actionName, "tofi/") {
			name := strings.TrimPrefix(actionName, "tofi/")
			data, err := actionlib.ReadAction(name)
			if err != nil {
				return "", err
			}
			childWf, err = parser.ParseWorkflowFromBytes(data, "yaml")
			if err != nil {
				return "", err
			}
		} else {
			return "", fmt.Errorf("action must start with 'tofi/'")
		}
	} else if filePath != "" {
		childWf, err = parser.LoadWorkflow(filePath)
		if err != nil {
			return "", err
		}
	} else {
		return "", fmt.Errorf("either action or file must be specified")
	}

	// 创建子上下文
	// 注意：Handoff 的特殊之处在于它将自己的整个 config 传给子工作流作为 inputs
	childCtx := models.NewExecutionContext(ctx.ExecutionID+"/handoff", ctx.Paths.Home)

	// 继承结果 (可选，根据需求决定子工作流是否能看到父工作流的结果)
	// 这里我们保持隔离，只传递输入

	inputsJSON, _ := json.Marshal(config)
	childCtx.SetResult("inputs", string(inputsJSON))

	if workflowStarter == nil {
		return "", fmt.Errorf("workflowStarter not initialized")
	}
	workflowStarter(childWf, childCtx)
	childCtx.Wg.Wait()

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
