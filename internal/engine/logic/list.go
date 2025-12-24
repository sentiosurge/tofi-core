package logic

import (
	"encoding/json"
	"fmt"
	"strconv"
	"tofi-core/internal/models"
)

type List struct{}

func (l *List) Execute(n *models.Node, ctx *models.ExecutionContext) (string, error) {
	listRaw := ctx.ReplaceParams(n.Config["list"]) // 预期是 JSON 数组
	mode := n.Config["mode"]                       // "length_is", "contains"

	var list []interface{}
	if err := json.Unmarshal([]byte(listRaw), &list); err != nil {
		return "", fmt.Errorf("列表解析失败，请确保输入是 JSON 格式")
	}

	switch mode {
	case "length_is":
		expectedLen, _ := strconv.Atoi(n.Config["value"])
		if len(list) != expectedLen {
			return "", fmt.Errorf("CONDITION_NOT_MET")
		}
	case "contains":
		item := ctx.ReplaceParams(n.Config["value"])
		found := false
		for _, v := range list {
			if fmt.Sprint(v) == item {
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
