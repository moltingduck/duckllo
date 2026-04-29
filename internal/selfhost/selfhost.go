package selfhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/moltingduck/duckllo/internal/auth"
	"github.com/moltingduck/duckllo/internal/store"
)

// Options controls how Run behaves. Most callers leave them at defaults.
type Options struct {
	ProjectName string // "duckllo" by default
	APIKeyLabel string // "selfhost-runner" by default
	EnvFile     string // ".duckllo.env" by default
	BaseURL     string // recorded in the env file; "http://localhost:3000" by default
	Force       bool   // overwrite an existing .duckllo.env
}

// Result is what Run returns to the caller (typically `cmd/duckllo
// selfhost`) so it can print a useful summary.
type Result struct {
	ProjectID    uuid.UUID
	ProjectName  string
	APIKey       string // plaintext; non-empty only when a fresh key was minted
	APIKeyLabel  string
	APIKeyPrefix string // always set, even on idempotent re-runs
	RulesAdded   int
	RulesSkipped int
	EnvWritten   bool
	EnvSkipped   bool
}

// Run is the idempotent dogfood-bootstrap. It:
//   1. ensures the gin steward exists (assumes the regular bootstrap has
//      already run; otherwise nothing to attach project membership to)
//   2. ensures a project with the configured name exists, owned by gin
//   3. mints an API key labelled APIKeyLabel if no live key with that
//      label exists yet (the plaintext is returned so the caller can
//      write it to .duckllo.env)
//   4. seeds the project's harness_rules from SelfHostRules (skipping
//      rules whose name already exists)
//   5. writes EnvFile with DUCKLLO_URL/PROJECT/KEY pre-populated unless
//      the file already exists and Force is false.
func Run(ctx context.Context, pool *pgxpool.Pool, opts Options) (*Result, error) {
	if opts.ProjectName == "" {
		opts.ProjectName = "duckllo"
	}
	if opts.APIKeyLabel == "" {
		opts.APIKeyLabel = "selfhost-runner"
	}
	if opts.EnvFile == "" {
		opts.EnvFile = ".duckllo.env"
	}
	if opts.BaseURL == "" {
		opts.BaseURL = "http://localhost:3000"
	}

	st := store.New(pool)

	gin, err := st.UserByUsername(ctx, "gin")
	if err != nil {
		return nil, fmt.Errorf("selfhost: gin steward missing — set DUCKLLO_GIN_PASSWORD and re-run serve once first: %w", err)
	}

	// 1. ensureProject by name + owner.
	pid, err := findProjectByName(ctx, pool, opts.ProjectName, gin.ID)
	if err != nil {
		return nil, err
	}
	if pid == uuid.Nil {
		p, err := st.CreateProject(ctx, opts.ProjectName,
			"duckllo developing duckllo — see CLAUDE.md", gin.ID)
		if err != nil {
			return nil, fmt.Errorf("create project: %w", err)
		}
		pid = p.ID
	}

	// 2. ensureKey by label.
	plain, prefix, err := ensureAPIKey(ctx, pool, st, gin.ID, pid, opts.APIKeyLabel)
	if err != nil {
		return nil, err
	}

	// 3. seed harness rules.
	added, skipped, err := seedRules(ctx, pool, st, pid)
	if err != nil {
		return nil, err
	}

	res := &Result{
		ProjectID: pid, ProjectName: opts.ProjectName,
		APIKey: plain, APIKeyLabel: opts.APIKeyLabel, APIKeyPrefix: prefix,
		RulesAdded: added, RulesSkipped: skipped,
	}

	// 4. write env file.
	if plain == "" {
		// We only write the env file when we have a plaintext key in hand.
		// If the user re-runs after the key already exists, the plaintext
		// is gone and we leave the env file alone.
		res.EnvSkipped = true
	} else if exists, err := fileExists(opts.EnvFile); err != nil {
		return nil, err
	} else if exists && !opts.Force {
		res.EnvSkipped = true
	} else {
		if err := writeEnvFile(opts.EnvFile, opts.BaseURL, pid, plain, opts.ProjectName); err != nil {
			return nil, err
		}
		res.EnvWritten = true
	}

	return res, nil
}

func findProjectByName(ctx context.Context, pool *pgxpool.Pool, name string, ownerID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := pool.QueryRow(ctx, `SELECT id FROM projects WHERE name = $1 AND owner_id = $2`,
		name, ownerID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil
	}
	return id, err
}

func ensureAPIKey(ctx context.Context, pool *pgxpool.Pool, st *store.Store, ownerID, projectID uuid.UUID, label string) (plaintext, prefix string, err error) {
	var existingPrefix string
	err = pool.QueryRow(ctx, `
		SELECT key_prefix FROM api_keys WHERE project_id = $1 AND label = $2 ORDER BY created_at DESC LIMIT 1
	`, projectID, label).Scan(&existingPrefix)
	if err == nil {
		// A key with this label already exists. We can't recover the
		// plaintext (only the bcrypt hash is stored). Tell the caller via
		// empty plaintext + non-empty prefix.
		return "", existingPrefix, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", "", fmt.Errorf("look up existing key: %w", err)
	}

	plain, p, hash, err := auth.MintAPIKey()
	if err != nil {
		return "", "", err
	}
	perms := []byte(`["read","write"]`)
	if _, err := st.CreateAPIKey(ctx, ownerID, projectID, label, p, hash, perms); err != nil {
		return "", "", fmt.Errorf("create api key: %w", err)
	}
	return plain, p, nil
}

func seedRules(ctx context.Context, pool *pgxpool.Pool, st *store.Store, projectID uuid.UUID) (added, skipped int, err error) {
	for _, r := range SelfHostRules {
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM harness_rules WHERE project_id = $1 AND name = $2
		`, projectID, r.Name).Scan(&n); err != nil {
			return added, skipped, fmt.Errorf("rule lookup: %w", err)
		}
		if n > 0 {
			skipped++
			continue
		}
		if _, err := st.CreateRule(ctx, projectID, nil, r.Kind, r.Name, r.Body); err != nil {
			return added, skipped, fmt.Errorf("create rule %q: %w", r.Name, err)
		}
		added++
	}
	return added, skipped, nil
}

func writeEnvFile(path, baseURL string, projectID uuid.UUID, plainKey, projectName string) error {
	body := strings.Join([]string{
		"# duckllo selfhost — written by `duckllo selfhost`.",
		"# This file is gitignored. Refresh by deleting it and re-running selfhost.",
		"",
		fmt.Sprintf("DUCKLLO_URL=%s", baseURL),
		fmt.Sprintf("DUCKLLO_PROJECT=%s", projectID),
		fmt.Sprintf("DUCKLLO_KEY=%s", plainKey),
		"",
		"# Required by the runner.",
		"# ANTHROPIC_API_KEY=sk-...",
		"",
		fmt.Sprintf("# Project name on the duckllo board: %s", projectName),
		"",
	}, "\n")
	return os.WriteFile(path, []byte(body), 0o600)
}

func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}
