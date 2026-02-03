package logic

import (
	"fmt"
	"strconv"
	"strings"
	"tofi-core/internal/models"
)

type Math struct{}

func (m *Math) Execute(config map[string]interface{}, ctx *models.ExecutionContext) (string, error) {
	leftVal := fmt.Sprint(config["left"])
	rightVal := fmt.Sprint(config["right"])
	operator := fmt.Sprint(config["operator"])

	l, errL := strconv.ParseFloat(leftVal, 64)
	if errL != nil {
		return "", fmt.Errorf("math error: LEFT operand is not a number (got: '%s')", leftVal)
	}

	r, errR := strconv.ParseFloat(rightVal, 64)
	if errR != nil {
		return "", fmt.Errorf("math error: RIGHT operand is not a number (got: '%s')", rightVal)
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
		return "", fmt.Errorf("unsupported operator: %s", operator)
	}

	if !result {
		if strings.ToLower(fmt.Sprint(config["output_bool"])) == "true" {
			return "false", nil
		}
		return "", fmt.Errorf("CONDITION_NOT_MET")
	}

	if strings.ToLower(fmt.Sprint(config["output_bool"])) == "true" {
		return "true", nil
	}
	return "MATH_PASSED", nil
}

func (m *Math) Validate(n *models.Node) error {
	// 验证 Config 结构
	op, ok := n.Config["operator"]
	if !ok {
		return fmt.Errorf("config.operator is required")
	}
	switch op {
	case ">", "<", "==", ">=", "<=", "!=":
		// ok
	default:
		return fmt.Errorf("invalid operator: %v", op)
	}
	return nil
}
