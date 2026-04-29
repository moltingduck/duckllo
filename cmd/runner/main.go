// Command runner is the duckllo PEVC harness driver. It claims work items
// from a duckllo project, drives the planner / executor / validator /
// corrector phases against the configured model provider, and posts state
// back to the server.
//
// Phase 1 runs locally — the workspace is just a directory on the host
// (passed via --workspace). Phase 2 will spawn per-spec Docker containers
// and a Tailscale sidecar; the orchestrator above is unchanged for that
// transition.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/moltingduck/duckllo/internal/dotenv"
	"github.com/moltingduck/duckllo/internal/runner/agent"
	"github.com/moltingduck/duckllo/internal/runner/client"
	"github.com/moltingduck/duckllo/internal/runner/orchestrator"
	"github.com/moltingduck/duckllo/internal/runner/tools"
	"github.com/moltingduck/duckllo/internal/runner/workspace"
	"github.com/moltingduck/duckllo/internal/sensors"
)

func main() {
	if path, err := dotenv.LoadDefault(); err != nil {
		log.Printf("warning: dotenv load: %v", err)
	} else if path != "" {
		log.Printf("loaded env from %s", path)
	}

	var (
		baseURL     = flag.String("url", env("DUCKLLO_URL", "http://localhost:3000"), "duckllo base URL")
		apiKey      = flag.String("key", env("DUCKLLO_KEY", ""), "duckllo project API key (duckllo_...)")
		projectID   = flag.String("project", env("DUCKLLO_PROJECT", ""), "duckllo project UUID")
		runnerID    = flag.String("runner-id", env("DUCKLLO_RUNNER_ID", defaultRunnerID()), "stable identifier for this runner")
		rolesFlag   = flag.String("roles", env("DUCKLLO_ROLES", "planner,executor,validator,corrector"), "comma-separated phases this runner will claim")
		workspaceDir = flag.String("workspace", env("DUCKLLO_WORKSPACE", "./workspace"), "filesystem path the executor edits (host mode)")
		model       = flag.String("model", env("DUCKLLO_MODEL", ""), "Anthropic model id (default claude-sonnet-4-6)")
		anthropic   = flag.String("anthropic-key", os.Getenv("ANTHROPIC_API_KEY"), "Anthropic API key")
		intervalSec = flag.Int("poll-interval", 5, "seconds between empty-claim retries")
		once        = flag.Bool("once", false, "claim and process exactly one work item then exit")
		devURL      = flag.String("dev-url", env("DUCKLLO_DEV_URL", ""), "base URL of the dev server, used by screenshot sensor")
		chromePath  = flag.String("chrome-path", env("DUCKLLO_CHROME_PATH", ""), "override Chrome/Chromium binary path")
		image          = flag.String("container-image", env("DUCKLLO_CONTAINER_IMAGE", ""), "Docker image for per-run workspaces (empty = run on host)")
		tailscaleKey   = flag.String("tailscale-preauth-key", env("TAILSCALE_PREAUTH_KEY", ""), "Tailscale preauth key for the per-run sidecar (empty = no sidecar)")
		tailscaleImage = flag.String("tailscale-image", env("DUCKLLO_TAILSCALE_IMAGE", "tailscale/tailscale:latest"), "Tailscale sidecar image")
	)
	flag.Parse()

	if *apiKey == "" || *projectID == "" {
		log.Fatal("--key and --project are required (or set DUCKLLO_KEY and DUCKLLO_PROJECT)")
	}
	pid, err := uuid.Parse(*projectID)
	if err != nil {
		log.Fatalf("invalid project id: %v", err)
	}
	if *anthropic == "" {
		log.Fatal("ANTHROPIC_API_KEY required (or --anthropic-key)")
	}
	if err := tools.EnsureRoot(*workspaceDir); err != nil {
		log.Fatalf("workspace: %v", err)
	}

	roles := splitTrim(*rolesFlag, ",")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	c := client.New(*baseURL, *apiKey, pid)
	prov := agent.NewAnthropic(*anthropic, *model)

	o := &orchestrator.Orchestrator{
		Client: c, Provider: prov,
		Sensors:             sensors.DefaultRegistry(),
		RunnerID:            *runnerID,
		MaxTurns:            12,
		DevURL:              *devURL,
		ChromePath:          *chromePath,
		Workspace:           *workspaceDir,
		ContainerImage:      *image,
		TailscalePreauthKey: *tailscaleKey,
		TailscaleImage:      *tailscaleImage,
	}

	mode := "host"
	if *image != "" {
		mode = "docker:" + *image
		if *tailscaleKey != "" {
			mode += " +tailscale"
		}
	}
	log.Printf("runner %s up — url=%s project=%s roles=%v workspace=%s mode=%s model=%s",
		*runnerID, *baseURL, *projectID, roles, *workspaceDir, mode, prov.DefaultModel())
	_ = workspace.Meta{} // keep workspace import while orchestrator wires through

	for {
		select {
		case <-ctx.Done():
			log.Printf("shutting down")
			return
		default:
		}

		claim, err := c.Claim(ctx, *runnerID, roles)
		if errors.Is(err, client.ErrNoWork) {
			if *once {
				log.Printf("no work; exiting")
				return
			}
			sleep(ctx, time.Duration(*intervalSec)*time.Second)
			continue
		}
		if err != nil {
			log.Printf("claim: %v — backing off", err)
			sleep(ctx, time.Duration(*intervalSec)*time.Second)
			continue
		}

		log.Printf("claimed work %s phase=%s run=%s",
			claim.WorkItem.ID, claim.WorkItem.Phase, claim.Run.ID)

		hb := startHeartbeat(ctx, c, claim.Run.ID, *runnerID)
		err = o.Run(ctx, &claim.WorkItem, &claim.Run)
		hb()

		if err != nil {
			log.Printf("orchestrator: %v — marking failed", err)
			_ = c.Advance(ctx, claim.Run.ID, client.AdvanceRequest{
				RunnerID: *runnerID, FromPhase: claim.WorkItem.Phase, FinalStatus: "failed",
			})
		}

		if *once {
			return
		}
	}
}

// startHeartbeat runs a goroutine that pings the server every 30s while the
// orchestrator works on the claimed run. The returned func stops it.
func startHeartbeat(ctx context.Context, c *client.Client, runID uuid.UUID, runnerID string) func() {
	hbCtx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				if err := c.Heartbeat(hbCtx, runID, runnerID); err != nil {
					log.Printf("heartbeat: %v", err)
				}
			}
		}
	}()
	return cancel
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func defaultRunnerID() string {
	host, _ := os.Hostname()
	if host == "" {
		host = "runner"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}

func splitTrim(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
