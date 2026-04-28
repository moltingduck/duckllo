// Package http exposes the duckllo coordination plane over REST + SSE.
package http

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/moltingduck/duckllo/internal/config"
	"github.com/moltingduck/duckllo/internal/uploads"
)

type Server struct {
	cfg     *config.Config
	pool    *pgxpool.Pool
	uploads *uploads.Store
	events  *EventBus
}

func NewServer(cfg *config.Config, pool *pgxpool.Pool) *Server {
	up, err := uploads.New(cfg.UploadsDir, cfg.MaxUploadBytes)
	if err != nil {
		panic("uploads init: " + err.Error())
	}
	return &Server{
		cfg:     cfg,
		pool:    pool,
		uploads: up,
		events:  NewEventBus(),
	}
}

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
