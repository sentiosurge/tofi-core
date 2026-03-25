package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// checkSpendCap checks if user has exceeded daily or monthly spend limits.
// Returns nil if within limits or no limits are set.
func (s *Server) checkSpendCap(userID string) error {
	now := time.Now().UTC()

	// Check daily cap
	if capStr, err := s.db.GetSetting("spend_cap_daily", userID); err == nil && capStr != "" {
		cap, err := strconv.ParseFloat(capStr, 64)
		if err == nil && cap > 0 {
			todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
			spent, err := s.db.GetUserSpend(userID, todayStart)
			if err == nil && spent >= cap {
				return fmt.Errorf("daily spend cap exceeded: $%.2f / $%.2f", spent, cap)
			}
		}
	}

	// Check monthly cap
	if capStr, err := s.db.GetSetting("spend_cap_monthly", userID); err == nil && capStr != "" {
		cap, err := strconv.ParseFloat(capStr, 64)
		if err == nil && cap > 0 {
			monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
			spent, err := s.db.GetUserSpend(userID, monthStart)
			if err == nil && spent >= cap {
				return fmt.Errorf("monthly spend cap exceeded: $%.2f / $%.2f", spent, cap)
			}
		}
	}

	return nil
}

// handleSetUserSpendCap sets the user's own spend cap.
// PUT /api/v1/user/settings/spend-cap
func (s *Server) handleSetUserSpendCap(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)

	var req struct {
		Daily   *float64 `json:"daily"`
		Monthly *float64 `json:"monthly"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "Invalid request body", "")
		return
	}

	if req.Daily != nil {
		if *req.Daily <= 0 {
			s.db.DeleteSetting("spend_cap_daily", userID)
		} else {
			s.db.SetSetting("spend_cap_daily", userID, fmt.Sprintf("%.2f", *req.Daily))
		}
	}
	if req.Monthly != nil {
		if *req.Monthly <= 0 {
			s.db.DeleteSetting("spend_cap_monthly", userID)
		} else {
			s.db.SetSetting("spend_cap_monthly", userID, fmt.Sprintf("%.2f", *req.Monthly))
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

// handleGetUserSpendCap returns the user's spend cap and current spend.
// GET /api/v1/user/settings/spend-cap
func (s *Server) handleGetUserSpendCap(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value(UserContextKey).(string)
	now := time.Now().UTC()

	resp := map[string]interface{}{}

	// Daily
	if capStr, err := s.db.GetSetting("spend_cap_daily", userID); err == nil && capStr != "" {
		if cap, err := strconv.ParseFloat(capStr, 64); err == nil {
			todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
			spent, _ := s.db.GetUserSpend(userID, todayStart)
			resp["daily"] = map[string]interface{}{"cap": cap, "spent": spent}
		}
	}

	// Monthly
	if capStr, err := s.db.GetSetting("spend_cap_monthly", userID); err == nil && capStr != "" {
		if cap, err := strconv.ParseFloat(capStr, 64); err == nil {
			monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
			spent, _ := s.db.GetUserSpend(userID, monthStart)
			resp["monthly"] = map[string]interface{}{"cap": cap, "spent": spent}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleSetSystemSpendCap sets the system-wide default spend cap (admin only).
// PUT /api/v1/admin/settings/spend-cap
func (s *Server) handleSetSystemSpendCap(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Daily   *float64 `json:"daily"`
		Monthly *float64 `json:"monthly"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, ErrBadRequest, "Invalid request body", "")
		return
	}

	if req.Daily != nil {
		if *req.Daily <= 0 {
			s.db.DeleteSetting("spend_cap_daily", "system")
		} else {
			s.db.SetSetting("spend_cap_daily", "system", fmt.Sprintf("%.2f", *req.Daily))
		}
	}
	if req.Monthly != nil {
		if *req.Monthly <= 0 {
			s.db.DeleteSetting("spend_cap_monthly", "system")
		} else {
			s.db.SetSetting("spend_cap_monthly", "system", fmt.Sprintf("%.2f", *req.Monthly))
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}
