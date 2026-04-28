package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/google/uuid"
)

// EventBus is a thin per-project SSE fan-out. Handlers call Publish after
// any state change; subscribers (the Web UI) connect to /api/projects/{pid}/events
// and receive newline-delimited SSE messages.
type EventBus struct {
	mu          sync.Mutex
	subscribers map[uuid.UUID]map[*subscriber]struct{}
}

type subscriber struct {
	ch     chan event
	closed chan struct{}
}

type event struct {
	Topic string `json:"topic"`
	Body  any    `json:"body"`
}

func NewEventBus() *EventBus {
	return &EventBus{subscribers: map[uuid.UUID]map[*subscriber]struct{}{}}
}

func (b *EventBus) Publish(projectID uuid.UUID, topic string, body any) {
	b.mu.Lock()
	subs := b.subscribers[projectID]
	clones := make([]*subscriber, 0, len(subs))
	for sub := range subs {
		clones = append(clones, sub)
	}
	b.mu.Unlock()

	ev := event{Topic: topic, Body: body}
	for _, sub := range clones {
		select {
		case sub.ch <- ev:
		case <-sub.closed:
		default:
			// Drop on full channel rather than block other subscribers; the
			// client can refresh state via REST after a reconnect.
		}
	}
}

func (b *EventBus) subscribe(projectID uuid.UUID) *subscriber {
	sub := &subscriber{ch: make(chan event, 32), closed: make(chan struct{})}
	b.mu.Lock()
	if b.subscribers[projectID] == nil {
		b.subscribers[projectID] = map[*subscriber]struct{}{}
	}
	b.subscribers[projectID][sub] = struct{}{}
	b.mu.Unlock()
	return sub
}

func (b *EventBus) unsubscribe(projectID uuid.UUID, sub *subscriber) {
	b.mu.Lock()
	if subs, ok := b.subscribers[projectID]; ok {
		delete(subs, sub)
		if len(subs) == 0 {
			delete(b.subscribers, projectID)
		}
	}
	b.mu.Unlock()
	close(sub.closed)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	p := projectFromCtx(r)
	if p == nil {
		writeError(w, http.StatusNotFound, "project not loaded")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sub := s.events.subscribe(p.ID)
	defer s.events.unsubscribe(p.ID, sub)

	if _, err := fmt.Fprintf(w, "event: connected\ndata: {\"project\":%q}\n\n", p.ID.String()); err != nil {
		return
	}
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-sub.ch:
			if !ok {
				return
			}
			payload, err := json.Marshal(ev.Body)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Topic, payload); err != nil {
				if errors.Is(err, http.ErrAbortHandler) {
					return
				}
				return
			}
			flusher.Flush()
		}
	}
}
