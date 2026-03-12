package server

import (
	"container/heap"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"tofi-core/internal/apps"
	"tofi-core/internal/storage"

	"github.com/google/uuid"
)

// ── Schedule Types (v2: entry-based alarm model) ──

type Schedule struct {
	Entries  []ScheduleEntry `json:"entries"`
	Timezone string          `json:"timezone"`
}

type ScheduleEntry struct {
	Time        string        `json:"time"`                    // "08:00"
	EndTime     string        `json:"end_time,omitempty"`      // "17:00" (only if interval)
	IntervalMin int           `json:"interval_min,omitempty"`  // 0 = once at time
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

// ── Min-Heap for scheduled runs ──

type RunEntry struct {
	RunID       string
	AppID       string
	UserID      string
	ScheduledAt time.Time
}

type RunHeap []RunEntry

func (h RunHeap) Len() int            { return len(h) }
func (h RunHeap) Less(i, j int) bool  { return h[i].ScheduledAt.Before(h[j].ScheduledAt) }
func (h RunHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *RunHeap) Push(x any)         { *h = append(*h, x.(RunEntry)) }
func (h *RunHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}
func (h RunHeap) Peek() RunEntry { return h[0] }

// RemoveByApp removes all entries for a given app
func (h *RunHeap) RemoveByApp(appID string) {
	n := 0
	for _, entry := range *h {
		if entry.AppID != appID {
			(*h)[n] = entry
			n++
		}
	}
	*h = (*h)[:n]
	heap.Init(h)
}

// ── App Scheduler ──

type AppScheduler struct {
	server    *Server
	mu        sync.Mutex
	h         RunHeap
	timer     *time.Timer
	renewalCh chan string
	stopCh    chan struct{}
	stopped   bool
}

func NewAppScheduler(server *Server) *AppScheduler {
	return &AppScheduler{
		server:    server,
		h:         RunHeap{},
		renewalCh: make(chan string, 100),
		stopCh:    make(chan struct{}),
	}
}

func (as *AppScheduler) Start() error {
	// Recover zombie runs: reset "running" app_runs to "pending" (server restart killed goroutines)
	recovered, err := as.server.db.RecoverRunningAppRuns()
	if err != nil {
		log.Printf("⚠️ Failed to recover running app_runs: %v", err)
	} else if recovered > 0 {
		log.Printf("♻️ Recovered %d zombie app_runs (running → pending)", recovered)
	}

	// Load all pending runs from DB into heap
	runs, err := as.server.db.GetAllPendingAppRuns()
	if err != nil {
		return fmt.Errorf("failed to load pending runs: %w", err)
	}

	heap.Init(&as.h)
	for _, r := range runs {
		t, err := time.Parse("2006-01-02 15:04:05", r.ScheduledAt)
		if err != nil {
			t, err = time.Parse(time.RFC3339, r.ScheduledAt)
			if err != nil {
				log.Printf("Skipping run %s: invalid scheduled_at: %s", r.ID, r.ScheduledAt)
				continue
			}
		}
		heap.Push(&as.h, RunEntry{
			RunID:       r.ID,
			AppID:       r.AppID,
			UserID:      r.UserID,
			ScheduledAt: t,
		})
	}

	as.timer = time.NewTimer(as.nextDelay())
	go as.mainLoop()
	go as.overdueSweep()

	log.Printf("App Scheduler started with %d pending runs", len(runs))
	return nil
}

func (as *AppScheduler) Stop() {
	if as.stopped {
		return
	}
	as.stopped = true
	close(as.stopCh)
	if as.timer != nil {
		as.timer.Stop()
	}
	log.Println("App Scheduler stopped")
}

func (as *AppScheduler) nextDelay() time.Duration {
	if as.h.Len() == 0 {
		return 24 * time.Hour
	}
	delay := time.Until(as.h.Peek().ScheduledAt)
	if delay < 0 {
		return 0
	}
	return delay
}

func (as *AppScheduler) resetTimer() {
	if as.timer != nil {
		as.timer.Reset(as.nextDelay())
	}
}

func (as *AppScheduler) mainLoop() {
	for {
		select {
		case <-as.timer.C:
			as.mu.Lock()
			now := time.Now()
			for as.h.Len() > 0 && !as.h.Peek().ScheduledAt.After(now) {
				entry := heap.Pop(&as.h).(RunEntry)
				go as.dispatchRun(entry)
			}
			as.resetTimer()
			as.mu.Unlock()

		case appID := <-as.renewalCh:
			as.doRenewal(appID)

		case <-as.stopCh:
			return
		}
	}
}

// overdueSweep periodically checks for overdue pending runs that the heap missed.
// This is a safety net against heap/DB desync (e.g., from activate/deactivate races).
func (as *AppScheduler) overdueSweep() {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			runs, err := as.server.db.GetOverdueAppRuns(time.Now().UTC())
			if err != nil || len(runs) == 0 {
				continue
			}
			log.Printf("🔍 Overdue sweep: found %d missed runs, re-adding to heap", len(runs))
			as.mu.Lock()
			for _, r := range runs {
				t, err := time.Parse("2006-01-02 15:04:05", r.ScheduledAt)
				if err != nil {
					t, _ = time.Parse(time.RFC3339, r.ScheduledAt)
				}
				heap.Push(&as.h, RunEntry{
					RunID:       r.ID,
					AppID:       r.AppID,
					UserID:      r.UserID,
					ScheduledAt: t,
				})
			}
			as.resetTimer()
			as.mu.Unlock()
		case <-as.stopCh:
			return
		}
	}
}

func (as *AppScheduler) dispatchRun(entry RunEntry) {
	log.Printf("[app-run:%s] Dispatching scheduled run for app %s", entry.RunID[:8], entry.AppID[:8])

	if err := as.server.db.UpdateAppRunStatus(entry.RunID, "running", ""); err != nil {
		log.Printf("[app-run:%s] Failed to mark running: %v", entry.RunID[:8], err)
		return
	}

	app, err := as.server.db.GetApp(entry.AppID)
	if err != nil {
		log.Printf("[app-run:%s] App %s not found: %v", entry.RunID[:8], entry.AppID[:8], err)
		as.server.db.UpdateAppRunStatus(entry.RunID, "failed", "")
		return
	}

	prompt := apps.ResolveFromJSON(app.Prompt, app.Parameters, app.ParameterDefs)

	card := &storage.KanbanCardRecord{
		ID:          uuid.New().String(),
		Title:       prompt,
		Description: fmt.Sprintf("[App: %s] %s", app.Name, app.Description),
		Status:      "todo",
		AppID:       app.ID,
		AgentID:     app.ID, // backward compat
		UserID:      entry.UserID,
	}
	if err := as.server.db.CreateKanbanCard(card); err != nil {
		log.Printf("[app-run:%s] Failed to create kanban card: %v", entry.RunID[:8], err)
		as.server.db.UpdateAppRunStatus(entry.RunID, "failed", "")
		return
	}

	created, _ := as.server.db.GetKanbanCard(card.ID)
	if created == nil {
		created = card
	}

	log.Printf("[app-run:%s] Executing with card %s", entry.RunID[:8], card.ID[:8])
	as.server.executeWish(created, entry.UserID, app.Model)

	finalCard, _ := as.server.db.GetKanbanCard(card.ID)
	status := "done"
	if finalCard != nil && finalCard.Status == "failed" {
		status = "failed"
	}
	as.server.db.UpdateAppRunStatus(entry.RunID, status, card.ID)

	count, err := as.server.db.CountPendingAppRuns(entry.AppID)
	if err == nil {
		app, err := as.server.db.GetApp(entry.AppID)
		if err == nil && app.IsActive && count < app.RenewalThreshold {
			select {
			case as.renewalCh <- entry.AppID:
			default:
			}
		}
	}
}

func (as *AppScheduler) doRenewal(appID string) {
	app, err := as.server.db.GetApp(appID)
	if err != nil || !app.IsActive {
		return
	}

	count, err := as.server.db.CountPendingAppRuns(appID)
	if err != nil {
		return
	}
	need := app.BufferSize - count
	if need <= 0 {
		return
	}

	log.Printf("[app:%s] Renewal: %d pending, need %d more", appID[:8], count, need)

	lastTime, _ := as.server.db.GetLastAppScheduledTime(appID)
	times := ExpandSchedule(app.ScheduleRules, lastTime, need)
	if len(times) == 0 {
		return
	}

	as.mu.Lock()
	defer as.mu.Unlock()

	for _, t := range times {
		run := &storage.AppRunRecord{
			ID:          uuid.New().String(),
			AppID:       appID,
			ScheduledAt: t.UTC().Format("2006-01-02 15:04:05"),
			Status:      "pending",
			UserID:      app.UserID,
		}
		if err := as.server.db.CreateAppRun(run); err != nil {
			log.Printf("Failed to create app run: %v", err)
			continue
		}
		heap.Push(&as.h, RunEntry{
			RunID:       run.ID,
			AppID:       appID,
			UserID:      app.UserID,
			ScheduledAt: t,
		})
	}
	as.resetTimer()

	log.Printf("[app:%s] Renewal complete: added %d runs", appID[:8], len(times))
}

// ── Public methods ──

func (as *AppScheduler) ActivateApp(app *storage.AppRecord) error {
	startFrom := time.Now()
	times := ExpandSchedule(app.ScheduleRules, startFrom, app.BufferSize)
	if len(times) == 0 {
		return fmt.Errorf("schedule rules produced no future runs")
	}

	as.mu.Lock()
	defer as.mu.Unlock()

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
		heap.Push(&as.h, RunEntry{
			RunID:       run.ID,
			AppID:       app.ID,
			UserID:      app.UserID,
			ScheduledAt: t,
		})
	}
	as.resetTimer()

	log.Printf("App %s activated with %d scheduled runs", app.ID[:8], len(times))
	return nil
}

func (as *AppScheduler) RemoveApp(appID string) {
	as.mu.Lock()
	defer as.mu.Unlock()
	as.h.RemoveByApp(appID)
	as.resetTimer()
}

func (as *AppScheduler) AddRun(entry RunEntry) {
	as.mu.Lock()
	defer as.mu.Unlock()
	heap.Push(&as.h, entry)
	as.resetTimer()
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
