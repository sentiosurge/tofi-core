package logic

import (
	"fmt"
	"regexp"
	"strings"
	"tofi-core/internal/models"
)

type Text struct{}

func (t *Text) Execute(n *models.Node, ctx *models.ExecutionContext) (string, error) {
	target := ctx.ReplaceParams(fmt.Sprint(n.Input["target"])) // 待检查的文本
	pattern := ctx.ReplaceParams(fmt.Sprint(n.Input["value"])) // 匹配的内容
	mode := n.Config["mode"]                       // "contains", "starts_with", "matches"

	var result bool
	switch mode {
	case "contains":
		result = strings.Contains(target, pattern)
	case "starts_with":
		result = strings.HasPrefix(target, pattern)
	case "matches": // 正则匹配
		re, err := regexp.Compile(pattern)
		if err != nil {
			return "", fmt.Errorf("正则语法错误: %v", err)
		}
		result = re.MatchString(target)
	default:
		return "", fmt.Errorf("不支持的文本判定: %s", mode)
	}

	if !result {
		return "TEXT_NOT_MATCH", fmt.Errorf("CONDITION_NOT_MET")
	}
	return "TEXT_MATCHED", nil
}

func (t *Text) Validate(n *models.Node) error {
	if _, ok := n.Input["target"]; !ok {
		return fmt.Errorf("input.target is required")
	}
	if _, ok := n.Input["value"]; !ok {
		return fmt.Errorf("input.value is required")
	}
	mode := n.Config["mode"]
	if mode != "contains" && mode != "starts_with" && mode != "matches" {
		return fmt.Errorf("invalid config.mode: %s", mode)
	}
	if mode == "matches" {
		if val, ok := n.Input["value"].(string); ok {
			if _, err := regexp.Compile(val); err != nil {
				return fmt.Errorf("invalid regex pattern: %v", err)
			}
		}
	}
	return nil
}
