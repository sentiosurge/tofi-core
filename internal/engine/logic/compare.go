package logic

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"tofi-core/internal/models"
)

type Compare struct{}

func (c *Compare) Execute(config map[string]interface{}, ctx *models.ExecutionContext) (string, error) {
	left := fmt.Sprint(config["left"])
	right := fmt.Sprint(config["right"])
	operator := fmt.Sprint(config["operator"])

	var result bool
	var err error

	switch operator {
	// 数字运算符：必须能转成数字
	case ">", "<", ">=", "<=":
		result, err = compareNumbers(left, right, operator)
	case "between":
		result, err = compareBetween(left, right)

	// 文本运算符：转字符串
	case "contains":
		result = strings.Contains(left, right)
	case "not_contains":
		result = !strings.Contains(left, right)
	case "starts_with":
		result = strings.HasPrefix(left, right)
	case "ends_with":
		result = strings.HasSuffix(left, right)
	case "matches":
		re, regexErr := regexp.Compile(right)
		if regexErr != nil {
			return "", fmt.Errorf("invalid regex pattern '%s': %v", right, regexErr)
		}
		result = re.MatchString(left)

	// 列表运算符：right 必须是数组
	case "in":
		result, err = compareInList(left, right, false)
	case "not_in":
		result, err = compareInList(left, right, true)

	// 通用运算符：先尝试数字，不行就字符串
	case "==":
		result = compareEqual(left, right)
	case "!=":
		result = !compareEqual(left, right)

	default:
		return "", fmt.Errorf("unsupported operator: %s", operator)
	}

	if err != nil {
		return "", err
	}

	if result {
		return "true", nil
	}
	return "false", nil
}

// compareNumbers 比较两个数字
func compareNumbers(left, right, operator string) (bool, error) {
	l, errL := strconv.ParseFloat(left, 64)
	if errL != nil {
		return false, fmt.Errorf("left operand '%s' is not a valid number for operator '%s'", left, operator)
	}

	r, errR := strconv.ParseFloat(right, 64)
	if errR != nil {
		return false, fmt.Errorf("right operand '%s' is not a valid number for operator '%s'", right, operator)
	}

	switch operator {
	case ">":
		return l > r, nil
	case "<":
		return l < r, nil
	case ">=":
		return l >= r, nil
	case "<=":
		return l <= r, nil
	}
	return false, nil
}

// compareBetween 检查 left 是否在 [min, max] 范围内
// right 应该是 JSON 数组格式 "[min, max]"
func compareBetween(left, right string) (bool, error) {
	l, errL := strconv.ParseFloat(left, 64)
	if errL != nil {
		return false, fmt.Errorf("left operand '%s' is not a valid number for 'between'", left)
	}

	var bounds []float64
	if err := json.Unmarshal([]byte(right), &bounds); err != nil {
		return false, fmt.Errorf("right operand for 'between' must be a JSON array [min, max], got '%s'", right)
	}

	if len(bounds) != 2 {
		return false, fmt.Errorf("right operand for 'between' must have exactly 2 elements [min, max], got %d", len(bounds))
	}

	min, max := bounds[0], bounds[1]
	return l >= min && l <= max, nil
}

// compareInList 检查 left 是否在 right 列表中
func compareInList(left, right string, negate bool) (bool, error) {
	var list []interface{}
	if err := json.Unmarshal([]byte(right), &list); err != nil {
		return false, fmt.Errorf("right operand for '%s' must be a JSON array, got '%s'",
			map[bool]string{true: "not_in", false: "in"}[negate], right)
	}

	found := false
	for _, item := range list {
		if fmt.Sprint(item) == left {
			found = true
			break
		}
	}

	if negate {
		return !found, nil
	}
	return found, nil
}

// compareEqual 先尝试数字比较，不行就字符串比较
func compareEqual(left, right string) bool {
	// 先尝试数字比较
	l, errL := strconv.ParseFloat(left, 64)
	r, errR := strconv.ParseFloat(right, 64)
	if errL == nil && errR == nil {
		return l == r
	}

	// 回退到字符串比较
	return left == right
}

func (c *Compare) Validate(n *models.Node) error {
	op, ok := n.Config["operator"]
	if !ok {
		return fmt.Errorf("config.operator is required")
	}

	validOps := map[string]bool{
		">": true, "<": true, ">=": true, "<=": true, "between": true,
		"contains": true, "not_contains": true, "starts_with": true, "ends_with": true, "matches": true,
		"in": true, "not_in": true,
		"==": true, "!=": true,
	}

	if !validOps[fmt.Sprint(op)] {
		return fmt.Errorf("invalid operator: %v", op)
	}

	if _, ok := n.Config["left"]; !ok {
		return fmt.Errorf("config.left is required")
	}

	if _, ok := n.Config["right"]; !ok {
		return fmt.Errorf("config.right is required")
	}

	return nil
}
