package data

import (
	"encoding/json"
	"fmt"
	"tofi-core/internal/models"
)

type Var struct{}

func (v *Var) Execute(config map[string]interface{}, ctx *models.ExecutionContext) (string, error) {
	// 在新规范下，Var 节点的逻辑变得极其简单：
	// ResolveConfig 已经把 value 字段解析好了（包括全局引用和环境展开）
	val := config["value"]
	if val == nil {
		// 如果没有单值 value，尝试返回整个 config (适配多变量模式)
		if len(config) > 0 {
			res, _ := json.Marshal(config)
			return string(res), nil
		}
		return "", nil
	}

	// 如果是字符串，直接返回
	if s, ok := val.(string); ok {
		return s, nil
	}

	// 否则返回 JSON 序列化结果
	res, err := json.Marshal(val)
	if err != nil {
		return "", fmt.Errorf("var serialization failed: %v", err)
	}
	return string(res), nil
}

func (v *Var) Validate(n *models.Node) error {
	// 如果既没有 value 也没有其他声明，报错
	if n.Value == nil && len(n.Config) == 0 && len(n.Input) == 0 {
		return fmt.Errorf("var node requires either value, config.value or input declarations")
	}
	return nil
}
