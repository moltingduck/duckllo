// Package http exposes the duckllo coordination plane over REST + SSE.
package http

import (
	"context"
	"errors"
	"net/http"
	"time"

	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/moltingduck/duckllo/internal/config"
	"github.com/moltingduck/duckllo/internal/runner/agent"
	"github.com/moltingduck/duckllo/internal/uploads"
	"github.com/moltingduck/duckllo/internal/webui"
)

type Server struct {
	cfg      *config.Config
	pool     *pgxpool.Pool
	uploads  *uploads.Store
	events   *EventBus
	webFS    fs.FS
	provider agent.Provider // nil if no API key configured; suggest endpoint 503s
}

func NewServer(cfg *config.Config, pool *pgxpool.Pool) *Server {
	up, err := uploads.New(cfg.UploadsDir, cfg.MaxUploadBytes)
	if err != nil {
		panic("uploads init: " + err.Error())
	}
	// Optional LLM provider for the spec composer's "Suggest criteria"
	// affordance. Only Anthropic is wired up here; if no key is set the
	// suggest endpoint returns 503 with a clear message instead of 500.
	var p agent.Provider
	if cfg.AnthropicAPIKey != "" {
		p = agent.NewAnthropic(cfg.AnthropicAPIKey, "")
	}
	return &Server{
		cfg:      cfg,
		pool:     pool,
		uploads:  up,
		events:   NewEventBus(),
		webFS:    webui.Assets(),
		provider: p,
	}
}

// Handler returns the assembled chi handler. Exposed for tests that want
// to plug the routes into httptest without spinning a real listener.
func (s *Server) Handler() http.Handler { return s.routes() }

func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return ctx.Err()
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
