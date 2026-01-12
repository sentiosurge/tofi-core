package tasks

import (
	"encoding/json"
	"fmt"
	"time"
	"tofi-core/internal/models"
)

type Hold struct {}

func (h *Hold) Execute(config map[string]interface{}, ctx *models.ExecutionContext) (string, error) {
	nodeID, ok := config["_node_id"].(string)
	if !ok {
		return "", fmt.Errorf("missing _node_id in config")
	}

	// 1. Get Input Data (Pass-through)
	var inputData string
	if v, ok := config["input"]; ok {
		if s, ok := v.(string); ok {
			inputData = s
		} else {
			jb, _ := json.Marshal(v)
			inputData = string(jb)
		}
	}

	// 2. Log Waiting Status
	ctx.Log("[HOLD] Node '%s' is waiting for approval...", nodeID)

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Ctx.Done():
			return "", ctx.Ctx.Err()
		case <-ticker.C:
			// Check approval
			if action, ok := ctx.GetApproval(nodeID); ok {
				ctx.Log("[HOLD] Approval action received: %s", action)
				if action == "reject" {
					return "", fmt.Errorf("execution rejected by user")
				}
				// Default is approve/resume
				// Return the passed-through data
				return inputData, nil
			}
		}
	}
}

func (h *Hold) Validate(node *models.Node) error {
	return nil
}