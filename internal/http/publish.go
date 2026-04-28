package http

import (
	"net/http"

	"github.com/google/uuid"
)

// publish fans an event out on the SSE bus for the project on the request
// context. Failures are silent — events are best-effort and the canonical
// state always lives in Postgres.
func (s *Server) publish(r *http.Request, topic string, body any) {
	p := projectFromCtx(r)
	if p == nil {
		return
	}
	s.publishTo(p.ID, topic, body)
}

func (s *Server) publishTo(projectID uuid.UUID, topic string, body any) {
	if s.events == nil {
		return
	}
	s.events.Publish(projectID, topic, body)
}
