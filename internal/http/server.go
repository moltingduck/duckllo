// Package http exposes the duckllo coordination plane over REST + SSE.
package http

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/exec"
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
	p := selectSuggestProvider(cfg)
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

// selectSuggestProvider picks the agent.Provider for the spec composer's
// suggest affordance. Honoured in this order:
//
//  1. cfg.SuggestProvider when explicitly set (anthropic | claude-code).
//  2. claude-code if the `claude` CLI (or a custom path in
//     cfg.ClaudeBinary) is on PATH — pipe stdin → stdout, no API key
//     needed because the CLI handles auth on the local host.
//  3. anthropic if cfg.AnthropicAPIKey is set.
//  4. nil — suggest endpoint returns 503 with a clear message.
//
// Choice (2) is the right default for a developer running duckllo on
// the same machine as Claude Code: harness the existing local CLI
// instead of another API key.
func selectSuggestProvider(cfg *config.Config) agent.Provider {
	switch cfg.SuggestProvider {
	case "anthropic":
		if cfg.AnthropicAPIKey == "" {
			log.Printf("suggest: DUCKLLO_SUGGEST_PROVIDER=anthropic but ANTHROPIC_API_KEY is empty — suggest will 503")
			return nil
		}
		return agent.NewAnthropic(cfg.AnthropicAPIKey, "")
	case "claude-code":
		bin := cfg.ClaudeBinary
		if bin == "" {
			bin = "claude"
		}
		if _, err := exec.LookPath(bin); err != nil {
			log.Printf("suggest: DUCKLLO_SUGGEST_PROVIDER=claude-code but %q not on PATH — suggest will 503", bin)
			return nil
		}
		log.Printf("suggest: provider=claude-code binary=%s", bin)
		return newSuggestClaudeCode(bin, cfg.ClaudeCwd)
	case "":
		// auto-detect.
	default:
		log.Printf("suggest: unknown DUCKLLO_SUGGEST_PROVIDER=%q (want anthropic|claude-code) — suggest will 503", cfg.SuggestProvider)
		return nil
	}

	bin := cfg.ClaudeBinary
	if bin == "" {
		bin = "claude"
	}
	if _, err := exec.LookPath(bin); err == nil {
		log.Printf("suggest: provider=claude-code (auto-detected %q on PATH)", bin)
		return newSuggestClaudeCode(bin, cfg.ClaudeCwd)
	}
	if cfg.AnthropicAPIKey != "" {
		log.Printf("suggest: provider=anthropic (no claude CLI on PATH)")
		return agent.NewAnthropic(cfg.AnthropicAPIKey, "")
	}
	log.Printf("suggest: no provider available — set ANTHROPIC_API_KEY or install the claude CLI")
	return nil
}

// newSuggestClaudeCode constructs a Claude Code provider tuned for the
// suggest endpoint: a tighter timeout because the user is waiting on
// the button. The 30-minute default in NewClaudeCode is sized for the
// runner's executor phase (which can do real work); for proposing six
// criteria, anything past a minute is almost certainly wedged.
func newSuggestClaudeCode(bin, cwd string) *agent.ClaudeCode {
	cc := agent.NewClaudeCode(bin, "", cwd)
	cc.Timeout = 90 * time.Second
	return cc
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
