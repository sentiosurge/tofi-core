package logic

import (
	"encoding/json"
	"fmt"
	"strconv"
	"tofi-core/internal/models"
)

type List struct{}

func (l *List) Execute(n *models.Node, ctx *models.ExecutionContext) (string, error) {
	rawList := n.Input["list"]
	mode := n.Config["mode"] // "length_is", "contains"

	var list []interface{}
	
	// Case 1: List Object (YAML array)
	if listObj, ok := rawList.([]interface{}); ok {
		// 递归替换变量
		replaced := ctx.ReplaceParamsAny(listObj)
		list = replaced.([]interface{})
	} else if listStr, ok := rawList.(string); ok {
		// Case 2: JSON String
		replacedStr := ctx.ReplaceParams(listStr)
		if err := json.Unmarshal([]byte(replacedStr), &list); err != nil {
			return "", fmt.Errorf("列表解析失败，请确保输入是 JSON 格式: %v", err)
		}
	} else {
		return "", fmt.Errorf("list 输入无效，必须是 JSON 字符串或数组")
	}

	targetVal := ctx.ReplaceParams(fmt.Sprint(n.Input["value"]))

	switch mode {
	case "length_is":
		expectedLen, _ := strconv.Atoi(targetVal)
		if len(list) != expectedLen {
			return "", fmt.Errorf("CONDITION_NOT_MET")
		}
	case "contains":
		found := false
		for _, v := range list {
			// 将元素转为字符串比较，确保类型兼容性
			if fmt.Sprint(v) == targetVal {
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("CONDITION_NOT_MET")
		}
	}

	return "LIST_OK", nil
}

func (l *List) Validate(n *models.Node) error {
	if _, ok := n.Input["list"]; !ok {
		return fmt.Errorf("input.list is required")
	}
	// input.value 根据 mode 可能是必需的，这里暂不强求，因为 mode 决定
	mode := n.Config["mode"]
	if mode != "length_is" && mode != "contains" {
		return fmt.Errorf("invalid config.mode: %s", mode)
	}
	return nil
}