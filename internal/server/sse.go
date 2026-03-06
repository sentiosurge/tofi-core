package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// CardEvent represents an SSE event for a kanban card
type CardEvent struct {
	Type   string         `json:"type"` // step_added | step_updated | card_updated | card_done
	CardID string         `json:"card_id"`
	Data   map[string]any `json:"data"`
}

// SSEHub manages SSE subscribers per card
type SSEHub struct {
	mu          sync.RWMutex
	subscribers map[string]map[chan CardEvent]struct{} // cardID → set of channels
}

// NewSSEHub creates a new SSE hub
func NewSSEHub() *SSEHub {
	return &SSEHub{
		subscribers: make(map[string]map[chan CardEvent]struct{}),
	}
}

// Subscribe registers a channel to receive events for a card
func (h *SSEHub) Subscribe(cardID string) chan CardEvent {
	h.mu.Lock()
	defer h.mu.Unlock()

	ch := make(chan CardEvent, 50)
	if h.subscribers[cardID] == nil {
		h.subscribers[cardID] = make(map[chan CardEvent]struct{})
	}
	h.subscribers[cardID][ch] = struct{}{}
	return ch
}

// Unsubscribe removes a channel from a card's subscribers
func (h *SSEHub) Unsubscribe(cardID string, ch chan CardEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if subs, ok := h.subscribers[cardID]; ok {
		delete(subs, ch)
		if len(subs) == 0 {
			delete(h.subscribers, cardID)
		}
	}
}

// Publish sends an event to all subscribers of a card (non-blocking)
func (h *SSEHub) Publish(event CardEvent) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	subs, ok := h.subscribers[event.CardID]
	if !ok {
		return
	}
	for ch := range subs {
		select {
		case ch <- event:
		default:
			// Channel full, skip (consumer too slow)
		}
	}
}

// CleanupCard closes all subscriber channels for a card
func (h *SSEHub) CleanupCard(cardID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if subs, ok := h.subscribers[cardID]; ok {
		for ch := range subs {
			close(ch)
		}
		delete(h.subscribers, cardID)
	}
}

// KanbanUpdaterWithSSE wraps a KanbanUpdater and publishes SSE events
type KanbanUpdaterWithSSE struct {
	inner interface {
		UpdateKanbanCardBySystem(id string, status string, progress int, result string) error
		AppendKanbanStep(id string, step map[string]any) error
		UpdateKanbanStep(id string, toolName string, status string, result string, durationMs int64) error
	}
	hub *SSEHub
}

func (u *KanbanUpdaterWithSSE) UpdateKanbanCardBySystem(id string, status string, progress int, result string) error {
	err := u.inner.UpdateKanbanCardBySystem(id, status, progress, result)
	if err != nil {
		return err
	}

	eventType := "card_updated"
	if status == "done" || status == "failed" {
		eventType = "card_done"
	}
	u.hub.Publish(CardEvent{
		Type:   eventType,
		CardID: id,
		Data: map[string]any{
			"status":   status,
			"progress": progress,
			"result":   result,
		},
	})
	return nil
}

func (u *KanbanUpdaterWithSSE) AppendKanbanStep(id string, step map[string]any) error {
	err := u.inner.AppendKanbanStep(id, step)
	if err != nil {
		return err
	}

	u.hub.Publish(CardEvent{
		Type:   "step_added",
		CardID: id,
		Data:   step,
	})
	return nil
}

func (u *KanbanUpdaterWithSSE) UpdateKanbanStep(id string, toolName string, status string, result string, durationMs int64) error {
	err := u.inner.UpdateKanbanStep(id, toolName, status, result, durationMs)
	if err != nil {
		return err
	}

	u.hub.Publish(CardEvent{
		Type:   "step_updated",
		CardID: id,
		Data: map[string]any{
			"tool_name":   toolName,
			"status":      status,
			"result":      result,
			"duration_ms": durationMs,
		},
	})
	return nil
}

// handleCardStream handles GET /api/v1/kanban/{id}/stream?token=xxx
func (s *Server) handleCardStream(w http.ResponseWriter, r *http.Request) {
	// Auth via query param (SSE can't set custom headers)
	tokenStr := r.URL.Query().Get("token")
	if tokenStr == "" {
		http.Error(w, "missing token query parameter", http.StatusUnauthorized)
		return
	}

	userID, err := parseJWT(tokenStr)
	if err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	cardID := r.PathValue("id")
	if cardID == "" {
		http.Error(w, "card id required", http.StatusBadRequest)
		return
	}

	// Verify card ownership
	card, err := s.db.GetKanbanCard(cardID)
	if err != nil || card.UserID != userID {
		http.Error(w, "card not found", http.StatusNotFound)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Disable write timeout for this connection
	rc := http.NewResponseController(w)
	rc.SetWriteDeadline(time.Time{})

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Subscribe to events
	ch := s.sseHub.Subscribe(cardID)
	defer s.sseHub.Unsubscribe(cardID, ch)

	// Send initial connected event
	fmt.Fprintf(w, "event: connected\ndata: {\"card_id\":\"%s\"}\n\n", cardID)
	flusher.Flush()

	// Keepalive ticker
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-ch:
			if !ok {
				// Channel closed (card done/cleanup)
				fmt.Fprintf(w, "event: %s\ndata: {\"card_id\":\"%s\"}\n\n", "card_done", cardID)
				flusher.Flush()
				return
			}
			data, err := json.Marshal(event)
			if err != nil {
				log.Printf("SSE marshal error: %v", err)
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}
