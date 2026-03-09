package server

import (
	"container/heap"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"tofi-core/internal/storage"

	"github.com/google/uuid"
)

// ── Schedule Rule Types ──

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
	AgentID     string
	UserID      string
	ScheduledAt time.Time
}

type RunHeap []RunEntry

func (h RunHeap) Len() int            { return len(h) }
func (h RunHeap) Less(i, j int) bool  { return h[i].ScheduledAt.Before(h[j].ScheduledAt) }
func (h RunHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *RunHeap) Push(x interface{}) { *h = append(*h, x.(RunEntry)) }
func (h *RunHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}
func (h RunHeap) Peek() RunEntry { return h[0] }

// RemoveByAgent removes all entries for a given agent
func (h *RunHeap) RemoveByAgent(agentID string) {
	n := 0
	for _, entry := range *h {
		if entry.AgentID != agentID {
			(*h)[n] = entry
			n++
		}
	}
	*h = (*h)[:n]
	heap.Init(h)
}

// ── Agent Scheduler ──

type AgentScheduler struct {
	server    *Server
	mu        sync.Mutex
	h         RunHeap
	timer     *time.Timer
	renewalCh chan string
	stopCh    chan struct{}
	stopped   bool
}

func NewAgentScheduler(server *Server) *AgentScheduler {
	return &AgentScheduler{
		server:    server,
		h:         RunHeap{},
		renewalCh: make(chan string, 100),
		stopCh:    make(chan struct{}),
	}
}

func (as *AgentScheduler) Start() error {
	// Load all pending runs from DB into heap
	runs, err := as.server.db.GetAllPendingRuns()
	if err != nil {
		return fmt.Errorf("failed to load pending runs: %w", err)
	}

	heap.Init(&as.h)
	for _, r := range runs {
		t, err := time.Parse("2006-01-02 15:04:05", r.ScheduledAt)
		if err != nil {
			t, err = time.Parse(time.RFC3339, r.ScheduledAt)
			if err != nil {
				log.Printf("⚠️ Skipping run %s: invalid scheduled_at: %s", r.ID, r.ScheduledAt)
				continue
			}
		}
		heap.Push(&as.h, RunEntry{
			RunID:       r.ID,
			AgentID:     r.AgentID,
			UserID:      r.UserID,
			ScheduledAt: t,
		})
	}

	// Initialize timer
	as.timer = time.NewTimer(as.nextDelay())

	// Start event loops
	go as.mainLoop()

	log.Printf("⏰ Agent Scheduler started with %d pending runs", len(runs))
	return nil
}

func (as *AgentScheduler) Stop() {
	if as.stopped {
		return
	}
	as.stopped = true
	close(as.stopCh)
	if as.timer != nil {
		as.timer.Stop()
	}
	log.Println("⏰ Agent Scheduler stopped")
}

func (as *AgentScheduler) nextDelay() time.Duration {
	if as.h.Len() == 0 {
		return 24 * time.Hour
	}
	delay := time.Until(as.h.Peek().ScheduledAt)
	if delay < 0 {
		return 0
	}
	return delay
}

func (as *AgentScheduler) resetTimer() {
	if as.timer != nil {
		as.timer.Reset(as.nextDelay())
	}
}

// mainLoop is the event-driven main loop
func (as *AgentScheduler) mainLoop() {
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

		case agentID := <-as.renewalCh:
			as.doRenewal(agentID)

		case <-as.stopCh:
			return
		}
	}
}

// dispatchRun executes a single scheduled run
func (as *AgentScheduler) dispatchRun(entry RunEntry) {
	log.Printf("⏰ [agent-run:%s] Dispatching scheduled run for agent %s", entry.RunID[:8], entry.AgentID[:8])

	// 1. Mark running in DB
	if err := as.server.db.UpdateAgentRunStatus(entry.RunID, "running", ""); err != nil {
		log.Printf("❌ [agent-run:%s] Failed to mark running: %v", entry.RunID[:8], err)
		return
	}

	// 2. Load agent config
	agent, err := as.server.db.GetAgent(entry.AgentID)
	if err != nil {
		log.Printf("❌ [agent-run:%s] Agent %s not found: %v", entry.RunID[:8], entry.AgentID[:8], err)
		as.server.db.UpdateAgentRunStatus(entry.RunID, "failed", "")
		return
	}

	// 3. Create KanbanCard
	card := &storage.KanbanCardRecord{
		ID:          uuid.New().String(),
		Title:       agent.Prompt,
		Description: fmt.Sprintf("[Agent: %s] %s", agent.Name, agent.Description),
		Status:      "todo",
		AgentID:     agent.ID,
		UserID:      entry.UserID,
	}
	if err := as.server.db.CreateKanbanCard(card); err != nil {
		log.Printf("❌ [agent-run:%s] Failed to create kanban card: %v", entry.RunID[:8], err)
		as.server.db.UpdateAgentRunStatus(entry.RunID, "failed", "")
		return
	}

	created, _ := as.server.db.GetKanbanCard(card.ID)
	if created == nil {
		created = card
	}

	// 4. Execute using wish pipeline
	log.Printf("🤖 [agent-run:%s] Executing with card %s", entry.RunID[:8], card.ID[:8])
	as.server.executeWish(created, entry.UserID, agent.Model)

	// 5. Update run status
	finalCard, _ := as.server.db.GetKanbanCard(card.ID)
	status := "done"
	if finalCard != nil && finalCard.Status == "failed" {
		status = "failed"
	}
	as.server.db.UpdateAgentRunStatus(entry.RunID, status, card.ID)

	// 6. Check if renewal needed
	count, err := as.server.db.CountPendingRuns(entry.AgentID)
	if err == nil {
		agent, err := as.server.db.GetAgent(entry.AgentID)
		if err == nil && agent.IsActive && count < agent.RenewalThreshold {
			select {
			case as.renewalCh <- entry.AgentID:
			default:
				// Channel full, skip this renewal cycle
			}
		}
	}
}

// doRenewal generates new runs for an agent when buffer is low
func (as *AgentScheduler) doRenewal(agentID string) {
	agent, err := as.server.db.GetAgent(agentID)
	if err != nil || !agent.IsActive {
		return
	}

	count, err := as.server.db.CountPendingRuns(agentID)
	if err != nil {
		return
	}
	need := agent.BufferSize - count
	if need <= 0 {
		return
	}

	log.Printf("🔄 [agent:%s] Renewal: %d pending, need %d more", agentID[:8], count, need)

	// Get last scheduled time as start point
	lastTime, _ := as.server.db.GetLastScheduledTime(agentID)

	// Generate new time points from rules
	times := ExpandSchedule(agent.ScheduleRules, lastTime, need)
	if len(times) == 0 {
		return
	}

	as.mu.Lock()
	defer as.mu.Unlock()

	for _, t := range times {
		run := &storage.AgentRunRecord{
			ID:          uuid.New().String(),
			AgentID:     agentID,
			ScheduledAt: t.UTC().Format("2006-01-02 15:04:05"),
			Status:      "pending",
			UserID:      agent.UserID,
		}
		if err := as.server.db.CreateAgentRun(run); err != nil {
			log.Printf("⚠️ Failed to create agent run: %v", err)
			continue
		}
		heap.Push(&as.h, RunEntry{
			RunID:       run.ID,
			AgentID:     agentID,
			UserID:      agent.UserID,
			ScheduledAt: t,
		})
	}
	as.resetTimer()

	log.Printf("✅ [agent:%s] Renewal complete: added %d runs", agentID[:8], len(times))
}

// ── Public methods for handlers ──

// ActivateAgent generates initial runs and adds them to the heap
func (as *AgentScheduler) ActivateAgent(agent *storage.AgentRecord) error {
	// Generate initial batch of runs
	startFrom := time.Now()
	times := ExpandSchedule(agent.ScheduleRules, startFrom, agent.BufferSize)
	if len(times) == 0 {
		return fmt.Errorf("schedule rules produced no future runs")
	}

	as.mu.Lock()
	defer as.mu.Unlock()

	for _, t := range times {
		run := &storage.AgentRunRecord{
			ID:          uuid.New().String(),
			AgentID:     agent.ID,
			ScheduledAt: t.UTC().Format("2006-01-02 15:04:05"),
			Status:      "pending",
			UserID:      agent.UserID,
		}
		if err := as.server.db.CreateAgentRun(run); err != nil {
			continue
		}
		heap.Push(&as.h, RunEntry{
			RunID:       run.ID,
			AgentID:     agent.ID,
			UserID:      agent.UserID,
			ScheduledAt: t,
		})
	}
	as.resetTimer()

	log.Printf("✅ Agent %s activated with %d scheduled runs", agent.ID[:8], len(times))
	return nil
}

// RemoveAgent removes all runs for an agent from the heap
func (as *AgentScheduler) RemoveAgent(agentID string) {
	as.mu.Lock()
	defer as.mu.Unlock()
	as.h.RemoveByAgent(agentID)
	as.resetTimer()
}

// AddRun adds a single run to the heap (used by RunNow)
func (as *AgentScheduler) AddRun(entry RunEntry) {
	as.mu.Lock()
	defer as.mu.Unlock()
	heap.Push(&as.h, entry)
	as.resetTimer()
}

// ── Schedule Expansion ──

// ExpandSchedule generates concrete time points from schedule rules
func ExpandSchedule(rulesJSON string, startFrom time.Time, count int) []time.Time {
	if count <= 0 {
		return nil
	}

	var schedule ScheduleRule
	if err := json.Unmarshal([]byte(rulesJSON), &schedule); err != nil {
		log.Printf("⚠️ Failed to parse schedule rules: %v", err)
		return nil
	}
	if len(schedule.Rules) == 0 {
		return nil
	}

	// Load timezone
	loc := time.UTC
	if schedule.Timezone != "" {
		if l, err := time.LoadLocation(schedule.Timezone); err == nil {
			loc = l
		}
	}

	// Build day set for each rule
	dayMap := map[string]time.Weekday{
		"mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
		"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday, "sun": time.Sunday,
	}

	var results []time.Time

	// Start from the beginning of the next minute after startFrom
	cursor := startFrom.In(loc).Truncate(time.Minute).Add(time.Minute)

	// Scan up to 365 days into the future
	maxDate := cursor.Add(365 * 24 * time.Hour)

	for cursor.Before(maxDate) && len(results) < count {
		weekday := cursor.Weekday()

		for _, rule := range schedule.Rules {
			// Check if this day matches
			if !dayMatches(rule.Days, weekday, dayMap) {
				continue
			}

			// Check each window
			for _, win := range rule.Windows {
				startH, startM := parseHHMM(win.Start)
				endH, endM := parseHHMM(win.End)

				startTime := time.Date(cursor.Year(), cursor.Month(), cursor.Day(), startH, startM, 0, 0, loc)
				endTime := time.Date(cursor.Year(), cursor.Month(), cursor.Day(), endH, endM, 0, 0, loc)

				if endTime.Before(startTime) {
					endTime = startTime // single run
				}

				interval := time.Duration(win.IntervalMin) * time.Minute
				if interval <= 0 {
					// Single run at start time
					if startTime.After(startFrom) && len(results) < count {
						results = append(results, startTime)
					}
				} else {
					// Multiple runs within window
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

		// Move to next day
		cursor = time.Date(cursor.Year(), cursor.Month(), cursor.Day()+1, 0, 0, 0, 0, loc)
	}

	return results
}

// dayMatches checks if a weekday matches the rule's day list
func dayMatches(days []string, weekday time.Weekday, dayMap map[string]time.Weekday) bool {
	if len(days) == 0 {
		return true // empty = every day
	}
	for _, d := range days {
		if mapped, ok := dayMap[strings.ToLower(d)]; ok && mapped == weekday {
			return true
		}
	}
	return false
}

// parseHHMM parses "09:30" → (9, 30)
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
