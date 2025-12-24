package data

import (
	"encoding/json"
	"fmt"
	"tofi-core/internal/models"
)

type Secret struct{}

func (s *Secret) Execute(n *models.Node, ctx *models.ExecutionContext) (string, error) {
	// 如果是单值模式
	if val, ok := n.Config["value"]; ok && len(n.Config) == 1 {
		return ctx.ReplaceParams(val), nil
	}

	// 如果是多值模式，序列化为 JSON
	jsonData, err := json.Marshal(n.Config)
	if err != nil {
		return "", fmt.Errorf("Secret 节点数据处理失败: %v", err)
	}

	return string(jsonData), nil
}
