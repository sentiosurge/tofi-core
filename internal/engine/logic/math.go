package logic

import (
	"fmt"
	"strconv"
	"tofi-core/internal/models"
)

type Math struct{}

func (m *Math) Execute(n *models.Node, ctx *models.ExecutionContext) (string, error) {
	leftVal := ctx.ReplaceParams(fmt.Sprint(n.Input["left"]))
	rightVal := ctx.ReplaceParams(fmt.Sprint(n.Input["right"]))
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

func (m *Math) Validate(n *models.Node) error {
	if _, ok := n.Input["left"]; !ok {
		return fmt.Errorf("input.left is required")
	}
	if _, ok := n.Input["right"]; !ok {
		return fmt.Errorf("input.right is required")
	}
	op := n.Config["operator"]
	switch op {
	case ">", "<", "==", ">=", "<=", "!=":
		return nil
	default:
		return fmt.Errorf("invalid config.operator: %s", op)
	}
}
