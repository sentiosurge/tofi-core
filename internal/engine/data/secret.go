package data

import (
	"encoding/json"
	"fmt"
	"tofi-core/internal/models"
)

type Secret struct{}

func (s *Secret) Execute(n *models.Node, ctx *models.ExecutionContext) (string, error) {
	// 规范：Secret 数据存储在 Data 字段

	// 1. 单值模式
	if len(n.Data) == 1 {
		if val, ok := n.Data["value"]; ok {
			if strVal, isStr := val.(string); isStr {
				return ctx.ReplaceParams(strVal), nil
			}
		}
	}

	// 2. 多值模式
	finalData := make(map[string]interface{})
	for k, v := range n.Data {
		if strVal, ok := v.(string); ok {
			finalData[k] = ctx.ReplaceParams(strVal)
		} else {
			finalData[k] = v
		}
	}

	// 3. 序列化
	jsonData, err := json.Marshal(finalData)
	if err != nil {
		return "", fmt.Errorf("Secret 节点数据处理失败: %v", err)
	}

	return string(jsonData), nil
}

func (s *Secret) Validate(n *models.Node) error {
	if len(n.Data) == 0 {
		return fmt.Errorf("data field is required")
	}
	return nil
}