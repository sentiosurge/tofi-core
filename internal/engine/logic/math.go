package logic

import (
	"fmt"
	"strconv"
	"tofi-core/internal/models"
)

type Math struct{}

func (m *Math) Execute(n *models.Node, ctx *models.ExecutionContext) (string, error) {
	leftVal := ctx.ReplaceParams(n.Config["left"])
	rightVal := ctx.ReplaceParams(n.Config["right"])
	operator := n.Config["operator"] // ">", "<", "==", ">=", "<="

	l, errL := strconv.ParseFloat(leftVal, 64)
	r, errR := strconv.ParseFloat(rightVal, 64)
	if errL != nil || errR != nil {
		return "", fmt.Errorf("数值转换失败: %s 或 %s 不是数字", leftVal, rightVal)
	}

	var result bool
	switch operator {
	case ">":
		result = l > r
	case "<":
		result = l < r
	case "==":
		result = l == r
	case ">=":
		result = l >= r
	case "<=":
		result = l <= r
	case "!=":
		result = l != r
	default:
		return "", fmt.Errorf("不支持的数学操作符: %s", operator)
	}

	if !result {
		return fmt.Sprintf("%f %s %f 不成立", l, operator, r), fmt.Errorf("CONDITION_NOT_MET")
	}
	return "MATH_PASSED", nil
}
