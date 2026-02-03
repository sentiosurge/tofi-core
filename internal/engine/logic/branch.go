package logic

import (
	"fmt"
	"strings"
	"tofi-core/internal/models"
)

// Branch 是分流器节点，根据 condition 的值决定走 on_true 还是 on_false
// 它本身不做计算，只读取上游的布尔值并决定分支
type Branch struct{}

// BranchTrue 和 BranchFalse 是特殊的返回值前缀，用于告诉 engine 走哪条分支
const (
	BranchTruePrefix  = "BRANCH_TRUE:"
	BranchFalsePrefix = "BRANCH_FALSE:"
)

func (b *Branch) Execute(config map[string]interface{}, ctx *models.ExecutionContext) (string, error) {
	condition := strings.TrimSpace(fmt.Sprint(config["condition"]))

	// 判断 condition 是否为 truthy
	isTruthy := isTruthyValue(condition)

	if isTruthy {
		return BranchTruePrefix + "true", nil
	}
	return BranchFalsePrefix + "false", nil
}

// isTruthyValue 判断一个值是否为"真"
// 支持: "true", "1", "yes", 非空字符串（除了 "false", "0", "no", ""）
func isTruthyValue(val string) bool {
	lower := strings.ToLower(val)

	// 明确的 false 值
	if lower == "false" || lower == "0" || lower == "no" || lower == "" {
		return false
	}

	// 明确的 true 值
	if lower == "true" || lower == "1" || lower == "yes" {
		return true
	}

	// 其他非空值视为 true（宽松模式）
	// 如果你想严格模式，可以在这里返回 error
	return len(strings.TrimSpace(val)) > 0
}

func (b *Branch) Validate(n *models.Node) error {
	if _, ok := n.Config["condition"]; !ok {
		return fmt.Errorf("config.condition is required")
	}

	// 检查是否定义了分支 (从 Config 中读取)
	onTrue := getStringSlice(n.Config, "on_true")
	onFalse := getStringSlice(n.Config, "on_false")
	if len(onTrue) == 0 && len(onFalse) == 0 {
		return fmt.Errorf("at least one of config.on_true or config.on_false must be defined")
	}

	return nil
}

// getStringSlice 从 map 中提取字符串数组
func getStringSlice(config map[string]interface{}, key string) []string {
	val, ok := config[key]
	if !ok {
		return nil
	}

	switch v := val.(type) {
	case []string:
		return v
	case []interface{}:
		result := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	default:
		return nil
	}
}
