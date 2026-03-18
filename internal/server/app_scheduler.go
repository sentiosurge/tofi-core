package server

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"tofi-core/internal/apps"
	"tofi-core/internal/chat"
	"tofi-core/internal/storage"

	"github.com/google/uuid"
)

// ── Schedule Types (v2: entry-based alarm model) ──

type Schedule struct {
	Entries  []ScheduleEntry `json:"entries"`
	Timezone string          `json:"timezone"`
}

type ScheduleEntry struct {
	Time        string        `json:"time"`                   // "08:00"
	EndTime     string        `json:"end_time,omitempty"`     // "17:00" (only if interval)
	IntervalMin int           `json:"interval_min,omitempty"` // 0 = once at time
	Repeat      RepeatPattern `json:"repeat"`
	Enabled     bool          `json:"enabled"`
	Label       string        `json:"label,omitempty"`
}

type RepeatPattern struct {
	Type  string   `json:"type"`            // "daily", "weekly", "monthly", "once"
	Days  []string `json:"days,omitempty"`  // for weekly: ["mon","tue",...]
	Dates []int    `json:"dates,omitempty"` // for monthly: [1, 15]
	Date  string   `json:"date,omitempty"`  // for once: "2026-03-15"
}

// ── Legacy Schedule Types (v1: rules-based, kept for backward compat) ──

type ScheduleRule struct {
	Rules    []RuleEntry `json:"rules"`
	Timezone string      `json:"timezone"`
}

type RuleEntry struct {
	Days    []string     `json:"days"`    // ["mon","tue",...] empty = every day
	Windows []TimeWindow `json:"windows"`
}

type TimeWindow struct {
	Start       string `json:"start"`        // "09:00"
	End         string `json:"end"`          // "09:30"
	IntervalMin int    `json:"interval_min"` // 0 = run once at start
}

// ── App Scheduler (DB-poll based) ──

type AppScheduler struct {
	server  *Server
	mu      sync.Mutex // guards dispatch to prevent double-dispatch
	stopCh  chan struct{}
	stopped bool
}

func NewAppScheduler(server *Server) *AppScheduler {
	return &AppScheduler{
		server: server,
		stopCh: make(chan struct{}),
	}
}

func (as *AppScheduler) Start() error {
	go as.pollLoop()
	log.Println("⏰ App Scheduler started (DB-poll, 30s interval)")
	return nil
}

func (as *AppScheduler) Stop() {
	if as.stopped {
		return
	}
	as.stopped = true
	close(as.stopCh)
	log.Println("⏰ App Scheduler stopped")
}

func (as *AppScheduler) pollLoop() {
	// Run immediately on start
	as.pollAndDispatch()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			as.pollAndDispatch()
		case <-as.stopCh:
			return
		}
	}
}

func (as *AppScheduler) pollAndDispatch() {
	as.mu.Lock()
	defer as.mu.Unlock()

	// 1. Dispatch overdue pending runs (only for active apps)
	runs, err := as.server.db.GetPendingAppRunsDue(time.Now())
	if err != nil {
		log.Printf("[app-scheduler] Failed to query due runs: %v", err)
	} else {
		for _, r := range runs {
			// Mark running immediately to prevent double-dispatch on next poll
			if err := as.server.db.UpdateAppRunStatus(r.ID, "running", ""); err != nil {
				log.Printf("[app-scheduler] Failed to mark run %s as running: %v", r.ID[:8], err)
				continue
			}
			go as.dispatchRun(r)
		}
	}

	// 2. Check renewals for active apps
	as.checkRenewals()
}

// DispatchManualRun creates an app_run record with trigger=manual and dispatches it immediately.
func (as *AppScheduler) DispatchManualRun(app *storage.AppRecord, userID string) (*storage.AppRunRecord, error) {
	run := &storage.AppRunRecord{
		ID:          uuid.New().String(),
		AppID:       app.ID,
		ScheduledAt: time.Now().UTC().Format("2006-01-02 15:04:05"),
		Status:      "running",
		Trigger:     "manual",
		UserID:      userID,
	}
	if err := as.server.db.CreateAppRun(run); err != nil {
		return nil, fmt.Errorf("create app_run: %w", err)
	}
	// Mark running (started_at)
	as.server.db.UpdateAppRunStatus(run.ID, "running", "")

	go as.dispatchRun(run)
	return run, nil
}

func (as *AppScheduler) dispatchRun(run *storage.AppRunRecord) {
	log.Printf("[app-run:%s] Dispatching %s run for app %s", run.ID[:8], run.Trigger, run.AppID[:8])

	app, err := as.server.db.GetApp(run.AppID)
	if err != nil {
		log.Printf("[app-run:%s] App %s not found: %v", run.ID[:8], run.AppID[:8], err)
		as.server.db.UpdateAppRunStatus(run.ID, "failed", "")
		return
	}

	prompt := apps.ResolveFromJSON(app.Prompt, app.Parameters, app.ParameterDefs)

	// Create a Chat Session for this app run
	scope := chat.AgentScope("app-" + app.ID[:8])
	sessionID := "s_" + uuid.New().String()[:12]

	// Build skills string from app config
	var skillNames []string
	json.Unmarshal([]byte(app.Skills), &skillNames)
	skillsStr := strings.Join(skillNames, ",")

	session := chat.NewSession(sessionID, app.Model, skillsStr)
	session.Title = fmt.Sprintf("[App: %s] %s", app.Name, app.Description)

	if err := as.server.chatStore.Save(run.UserID, scope, session); err != nil {
		log.Printf("[app-run:%s] Failed to create chat session: %v", run.ID[:8], err)
		as.server.db.UpdateAppRunStatus(run.ID, "failed", "")
		return
	}

	// Link run to session
	as.server.db.UpdateAppRunStatusWithSession(run.ID, "running", sessionID)

	log.Printf("[app-run:%s] Executing with chat session %s", run.ID[:8], sessionID[:8])
	result, err := as.server.executeChatSession(run.UserID, scope, session, prompt, nil)

	status := "done"
	if err != nil {
		log.Printf("[app-run:%s] Chat session execution failed: %v", run.ID[:8], err)
		status = "failed"
	} else {
		log.Printf("[app-run:%s] Completed (tokens: %d in / %d out, cost: $%.4f)",
			run.ID[:8], result.TotalUsage.InputTokens, result.TotalUsage.OutputTokens, result.TotalCost)
	}
	as.server.db.UpdateAppRunStatusWithSession(run.ID, status, sessionID)
}

func (as *AppScheduler) checkRenewals() {
	activeApps, err := as.server.db.ListActiveApps()
	if err != nil {
		log.Printf("[app-scheduler] Failed to list active apps: %v", err)
		return
	}

	for _, app := range activeApps {
		count, err := as.server.db.CountPendingAppRuns(app.ID)
		if err != nil {
			continue
		}
		if count < app.RenewalThreshold {
			as.doRenewal(app)
		}
	}
}

func (as *AppScheduler) doRenewal(app *storage.AppRecord) {
	count, err := as.server.db.CountPendingAppRuns(app.ID)
	if err != nil {
		return
	}
	need := app.BufferSize - count
	if need <= 0 {
		return
	}

	log.Printf("[app:%s] Renewal: %d pending, need %d more", app.ID[:8], count, need)

	lastTime, _ := as.server.db.GetLastAppScheduledTime(app.ID)
	times := ExpandSchedule(app.ScheduleRules, lastTime, need)
	if len(times) == 0 {
		return
	}

	added := 0
	for _, t := range times {
		run := &storage.AppRunRecord{
			ID:          uuid.New().String(),
			AppID:       app.ID,
			ScheduledAt: t.UTC().Format("2006-01-02 15:04:05"),
			Status:      "pending",
			UserID:      app.UserID,
		}
		if err := as.server.db.CreateAppRun(run); err != nil {
			// Stop at first failure to prevent gaps: if we continue past a failed
			// time slot, GetLastAppScheduledTime will skip over it permanently.
			log.Printf("[app:%s] Renewal stopped at %s: %v", app.ID[:8], t.Format("15:04"), err)
			break
		}
		added++
	}

	if added > 0 {
		log.Printf("[app:%s] Renewal complete: added %d runs", app.ID[:8], added)
	}
}

// ── Public methods ──

func (as *AppScheduler) ActivateApp(app *storage.AppRecord) error {
	startFrom := time.Now()
	times := ExpandSchedule(app.ScheduleRules, startFrom, app.BufferSize)
	if len(times) == 0 {
		return fmt.Errorf("schedule rules produced no future runs")
	}

	for _, t := range times {
		run := &storage.AppRunRecord{
			ID:          uuid.New().String(),
			AppID:       app.ID,
			ScheduledAt: t.UTC().Format("2006-01-02 15:04:05"),
			Status:      "pending",
			UserID:      app.UserID,
		}
		if err := as.server.db.CreateAppRun(run); err != nil {
			continue
		}
	}

	log.Printf("App %s activated with %d scheduled runs", app.ID[:8], len(times))
	return nil
}

func (as *AppScheduler) RemoveApp(appID string) {
	// No-op: deactivation already calls CancelPendingAppRuns via handler.
	// DB-poll model doesn't need in-memory cleanup.
}

// ── Schedule Expansion ──

// ExpandSchedule detects v2 (entries) or v1 (rules) format and dispatches accordingly.
func ExpandSchedule(rulesJSON string, startFrom time.Time, count int) []time.Time {
	if count <= 0 {
		return nil
	}

	// Try v2 format first (has "entries" key)
	var v2 Schedule
	if err := json.Unmarshal([]byte(rulesJSON), &v2); err == nil && len(v2.Entries) > 0 {
		return expandEntries(v2, startFrom, count)
	}

	// Fall back to v1 format (has "rules" key)
	return expandLegacyRules(rulesJSON, startFrom, count)
}

// expandEntries handles v2 entry-based alarm model
func expandEntries(schedule Schedule, startFrom time.Time, count int) []time.Time {
	loc := time.UTC
	if schedule.Timezone != "" {
		if l, err := time.LoadLocation(schedule.Timezone); err == nil {
			loc = l
		}
	}

	var results []time.Time
	cursor := startFrom.In(loc).Truncate(time.Minute).Add(time.Minute)
	maxDate := cursor.Add(365 * 24 * time.Hour)

	for cursor.Before(maxDate) && len(results) < count {
		for _, entry := range schedule.Entries {
			if !entry.Enabled {
				continue
			}
			if !entryMatchesDate(entry, cursor) {
				continue
			}

			startH, startM := parseHHMM(entry.Time)
			startTime := time.Date(cursor.Year(), cursor.Month(), cursor.Day(), startH, startM, 0, 0, loc)

			if entry.IntervalMin > 0 && entry.EndTime != "" {
				// Interval window
				endH, endM := parseHHMM(entry.EndTime)
				endTime := time.Date(cursor.Year(), cursor.Month(), cursor.Day(), endH, endM, 0, 0, loc)
				if endTime.Before(startTime) {
					endTime = startTime
				}
				interval := time.Duration(entry.IntervalMin) * time.Minute
				t := startTime
				for !t.After(endTime) && len(results) < count {
					if t.After(startFrom) {
						results = append(results, t)
					}
					t = t.Add(interval)
				}
			} else {
				// Single run at time
				if startTime.After(startFrom) && len(results) < count {
					results = append(results, startTime)
				}
			}
		}
		cursor = time.Date(cursor.Year(), cursor.Month(), cursor.Day()+1, 0, 0, 0, 0, loc)
	}

	return results
}

func entryMatchesDate(entry ScheduleEntry, date time.Time) bool {
	switch entry.Repeat.Type {
	case "daily":
		return true
	case "weekly":
		return weekdayMatches(entry.Repeat.Days, date.Weekday())
	case "monthly":
		day := date.Day()
		for _, d := range entry.Repeat.Dates {
			if d == day {
				return true
			}
		}
		return false
	case "once":
		if entry.Repeat.Date == "" {
			return false
		}
		target, err := time.Parse("2006-01-02", entry.Repeat.Date)
		if err != nil {
			return false
		}
		return date.Year() == target.Year() && date.Month() == target.Month() && date.Day() == target.Day()
	default:
		return false
	}
}

var dayMap = map[string]time.Weekday{
	"mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday, "sun": time.Sunday,
}

func weekdayMatches(days []string, weekday time.Weekday) bool {
	if len(days) == 0 {
		return true
	}
	for _, d := range days {
		if mapped, ok := dayMap[strings.ToLower(d)]; ok && mapped == weekday {
			return true
		}
	}
	return false
}

// expandLegacyRules handles v1 rules-based format
func expandLegacyRules(rulesJSON string, startFrom time.Time, count int) []time.Time {
	var schedule ScheduleRule
	if err := json.Unmarshal([]byte(rulesJSON), &schedule); err != nil {
		log.Printf("Failed to parse schedule rules: %v", err)
		return nil
	}
	if len(schedule.Rules) == 0 {
		return nil
	}

	loc := time.UTC
	if schedule.Timezone != "" {
		if l, err := time.LoadLocation(schedule.Timezone); err == nil {
			loc = l
		}
	}

	var results []time.Time
	cursor := startFrom.In(loc).Truncate(time.Minute).Add(time.Minute)
	maxDate := cursor.Add(365 * 24 * time.Hour)

	for cursor.Before(maxDate) && len(results) < count {
		weekday := cursor.Weekday()

		for _, rule := range schedule.Rules {
			if !weekdayMatches(rule.Days, weekday) {
				continue
			}

			for _, win := range rule.Windows {
				startH, startM := parseHHMM(win.Start)
				endH, endM := parseHHMM(win.End)

				startTime := time.Date(cursor.Year(), cursor.Month(), cursor.Day(), startH, startM, 0, 0, loc)
				endTime := time.Date(cursor.Year(), cursor.Month(), cursor.Day(), endH, endM, 0, 0, loc)

				if endTime.Before(startTime) {
					endTime = startTime
				}

				interval := time.Duration(win.IntervalMin) * time.Minute
				if interval <= 0 {
					if startTime.After(startFrom) && len(results) < count {
						results = append(results, startTime)
					}
				} else {
					t := startTime
					for !t.After(endTime) && len(results) < count {
						if t.After(startFrom) {
							results = append(results, t)
						}
						t = t.Add(interval)
					}
				}
			}
		}

		cursor = time.Date(cursor.Year(), cursor.Month(), cursor.Day()+1, 0, 0, 0, 0, loc)
	}

	return results
}

func parseHHMM(s string) (int, int) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0
	}
	h, m := 0, 0
	fmt.Sscanf(parts[0], "%d", &h)
	fmt.Sscanf(parts[1], "%d", &m)
	return h, m
}
