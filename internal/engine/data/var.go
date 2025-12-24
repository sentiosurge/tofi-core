package data

import (
	"encoding/json"
	"fmt"
	"tofi-core/internal/models"
)

type Var struct{}

func (v *Var) Execute(n *models.Node, ctx *models.ExecutionContext) (string, error) {
	// 如果用户只传了一个简单的 value
	if val, ok := n.Config["value"]; ok && len(n.Config) == 1 {
		return ctx.ReplaceParams(val), nil
	}

	// 如果是一个配置字典（如你 YAML 里的写法）
	// 将整个 Config map 序列化为 JSON 字符串存入结果集
	jsonData, err := json.Marshal(n.Config)
	if err != nil {
		return "", fmt.Errorf("变量节点序列化失败: %v", err)
	}

	return string(jsonData), nil
}
