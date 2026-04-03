package server

import (
	"encoding/json"
	"fmt"

	"tofi-core/internal/agent"
	"tofi-core/internal/provider"
	"tofi-core/internal/storage"
)

// buildScheduleTools creates the tofi_schedule tool that lets the AI manage
// App schedules directly (view, set, activate, deactivate).
func (s *Server) buildScheduleTools(userID string) []agent.ExtraBuiltinTool {
	return []agent.ExtraBuiltinTool{
		{
			Schema: provider.Tool{
				Name: "tofi_schedule",
				Description: "Manage App schedules. Actions: " +
					"'get' — view current schedule for an app. " +
					"'set' — set schedule rules (daily/weekly/monthly with time and timezone). " +
					"'activate' — start the scheduler for an app. " +
					"'deactivate' — stop the scheduler and cancel pending runs.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"action": map[string]interface{}{
							"type":        "string",
							"enum":        []string{"get", "set", "activate", "deactivate"},
							"description": "Action to perform",
						},
						"app_id": map[string]interface{}{
							"type":        "string",
							"description": "App ID to manage schedule for",
						},
						"schedule": map[string]interface{}{
							"type":        "string",
							"description": "Schedule rules JSON (only for 'set' action). Format: {\"entries\":[{\"time\":\"09:00\",\"repeat\":{\"type\":\"daily\"},\"enabled\":true}],\"timezone\":\"America/New_York\"}",
						},
					},
					"required": []string{"action", "app_id"},
				},
			},
			Handler: func(args map[string]interface{}) (string, error) {
				action, _ := args["action"].(string)
				appID, _ := args["app_id"].(string)

				if action == "" || appID == "" {
					return "Error: action and app_id are required", nil
				}

				// Verify app exists and belongs to user
				app, err := s.db.GetApp(appID)
				if err != nil || app.UserID != userID {
					return fmt.Sprintf("Error: app '%s' not found", appID), nil
				}

				switch action {
				case "get":
					return s.scheduleGet(app)
				case "set":
					scheduleJSON, _ := args["schedule"].(string)
					return s.scheduleSet(app, userID, scheduleJSON)
				case "activate":
					return s.scheduleActivate(app, userID)
				case "deactivate":
					return s.scheduleDeactivate(app, userID)
				default:
					return fmt.Sprintf("Error: unknown action '%s'. Use: get, set, activate, deactivate", action), nil
				}
			},
		},
	}
}

func (s *Server) scheduleGet(app *storageAppRecord) (string, error) {
	if app.ScheduleRules == "" || app.ScheduleRules == "[]" {
		return fmt.Sprintf("App '%s' has no schedule configured.\nStatus: %s",
			app.Name, boolStr(app.IsActive, "active", "inactive")), nil
	}

	return fmt.Sprintf("App: %s\nStatus: %s\nSchedule: %s",
		app.Name, boolStr(app.IsActive, "active", "inactive"), app.ScheduleRules), nil
}

func (s *Server) scheduleSet(app *storageAppRecord, userID, scheduleJSON string) (string, error) {
	if scheduleJSON == "" {
		return "Error: 'schedule' parameter is required for 'set' action", nil
	}

	// Validate JSON
	var parsed interface{}
	if err := json.Unmarshal([]byte(scheduleJSON), &parsed); err != nil {
		return fmt.Sprintf("Error: invalid schedule JSON: %v", err), nil
	}

	app.ScheduleRules = scheduleJSON
	if err := s.db.UpdateApp(app); err != nil {
		return fmt.Sprintf("Error: failed to update schedule: %v", err), nil
	}

	// If app is active, reschedule
	if app.IsActive && s.appScheduler != nil {
		s.appScheduler.RemoveApp(app.ID)
		s.appScheduler.ActivateApp(app)
	}

	return fmt.Sprintf("Schedule updated for '%s'.\nNew schedule: %s\nStatus: %s",
		app.Name, scheduleJSON, boolStr(app.IsActive, "active (rescheduled)", "inactive (use 'activate' to start)")), nil
}

func (s *Server) scheduleActivate(app *storageAppRecord, userID string) (string, error) {
	if app.ScheduleRules == "" || app.ScheduleRules == "[]" {
		return "Error: app has no schedule rules configured. Use 'set' action first.", nil
	}

	if err := s.db.SetAppActive(app.ID, userID, true); err != nil {
		return fmt.Sprintf("Error: failed to activate: %v", err), nil
	}

	if s.appScheduler != nil {
		app.IsActive = true
		s.appScheduler.ActivateApp(app)
	}

	return fmt.Sprintf("Schedule activated for '%s'. Runs will execute according to: %s", app.Name, app.ScheduleRules), nil
}

func (s *Server) scheduleDeactivate(app *storageAppRecord, userID string) (string, error) {
	if err := s.db.SetAppActive(app.ID, userID, false); err != nil {
		return fmt.Sprintf("Error: failed to deactivate: %v", err), nil
	}

	cancelled, _ := s.db.CancelPendingAppRuns(app.ID)

	if s.appScheduler != nil {
		s.appScheduler.RemoveApp(app.ID)
	}

	return fmt.Sprintf("Schedule deactivated for '%s'. %d pending run(s) cancelled.", app.Name, cancelled), nil
}

// storageAppRecord is a type alias to avoid import cycle with storage package
type storageAppRecord = storage.AppRecord

func boolStr(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}
