package logic

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"tofi-core/internal/models"
)

type Check struct{}

func (c *Check) Execute(config map[string]interface{}, ctx *models.ExecutionContext) (string, error) {
	val := fmt.Sprint(config["value"])
	operator := fmt.Sprint(config["operator"])

	var result bool

	switch operator {
	case "is_empty":
		result = len(strings.TrimSpace(val)) == 0

	case "not_empty":
		result = len(strings.TrimSpace(val)) > 0

	case "is_true":
		lowerVal := strings.ToLower(val)
		result = lowerVal == "true" || val == "1"

	case "is_false":
		lowerVal := strings.ToLower(val)
		result = lowerVal == "false" || val == "0"

	case "is_number":
		_, err := strconv.ParseFloat(val, 64)
		result = err == nil

	case "is_json":
		var js json.RawMessage
		result = json.Unmarshal([]byte(val), &js) == nil

	default:
		return "", fmt.Errorf("unsupported check operator: %s", operator)
	}

	if result {
		return "true", nil
	}
	return "false", nil
}

func (c *Check) Validate(n *models.Node) error {
	op, ok := n.Config["operator"]
	if !ok {
		return fmt.Errorf("config.operator is required")
	}

	validOps := map[string]bool{
		"is_empty":  true,
		"not_empty": true,
		"is_true":   true,
		"is_false":  true,
		"is_number": true,
		"is_json":   true,
	}

	if !validOps[fmt.Sprint(op)] {
		return fmt.Errorf("invalid check operator: %v", op)
	}

	return nil
}
