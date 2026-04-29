package selfhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/moltingduck/duckllo/internal/auth"
	"github.com/moltingduck/duckllo/internal/db"
	"github.com/moltingduck/duckllo/internal/store"
)

// requireTestDB skips when TEST_DATABASE_URL is unset; the selfhost
// flow has nothing to assert against without real tables.
func requireTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(pool.Close)
	wipeAll(t, ctx, pool)
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Seed gin (the real bootstrap routine does this; we replicate the
	// minimum needed so the selfhost call has somewhere to attach).
	hash, err := auth.HashPassword("changeme")
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(pool)
	if _, err := st.CreateUser(ctx, "gin", hash, "gin steward", "admin"); err != nil {
		t.Fatalf("seed gin: %v", err)
	}
	return pool
}

func wipeAll(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	tables := []string{
		"comments", "annotations", "verifications",
		"work_queue", "agent_sessions", "iterations", "runs",
		"plans", "specs", "harness_rules", "topologies",
		"recovery_codes", "sessions", "api_keys",
		"project_members", "projects", "users", "schema_migrations",
	}
	for _, tbl := range tables {
		_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS "+tbl+" CASCADE")
	}
}

func TestSelfhost_FreshAndIdempotent(t *testing.T) {
	pool := requireTestDB(t)
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".duckllo.env")

	// First run — fresh DB.
	res1, err := Run(context.Background(), pool, Options{
		ProjectName: "duckllo",
		EnvFile:     envPath,
		BaseURL:     "http://test:3000",
	})
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if res1.APIKey == "" {
		t.Error("expected plaintext key on first run")
	}
	if !strings.HasPrefix(res1.APIKey, "duckllo_") {
		t.Errorf("plain key shape: %q", res1.APIKey)
	}
	if res1.RulesAdded != len(SelfHostRules) {
		t.Errorf("rules added: got %d want %d", res1.RulesAdded, len(SelfHostRules))
	}
	if res1.RulesSkipped != 0 {
		t.Errorf("rules skipped on fresh run: got %d", res1.RulesSkipped)
	}
	if !res1.EnvWritten {
		t.Error("env file should be written on first run")
	}
	body, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	if !strings.Contains(string(body), "DUCKLLO_KEY="+res1.APIKey) {
		t.Errorf("env file missing the minted key: %s", body)
	}
	if !strings.Contains(string(body), "DUCKLLO_PROJECT="+res1.ProjectID.String()) {
		t.Errorf("env file missing project id: %s", body)
	}

	// Second run — same DB, same env file. Plaintext is unrecoverable;
	// the function should refuse to overwrite the env file.
	res2, err := Run(context.Background(), pool, Options{
		ProjectName: "duckllo",
		EnvFile:     envPath,
		BaseURL:     "http://test:3000",
	})
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if res2.APIKey != "" {
		t.Errorf("second run shouldn't return a plaintext key: %q", res2.APIKey)
	}
	if res2.RulesAdded != 0 || res2.RulesSkipped != len(SelfHostRules) {
		t.Errorf("second run rules: added=%d skipped=%d, want 0/%d", res2.RulesAdded, res2.RulesSkipped, len(SelfHostRules))
	}
	if res2.ProjectID != res1.ProjectID {
		t.Errorf("project ID drifted between runs: %s vs %s", res2.ProjectID, res1.ProjectID)
	}
	if !res2.EnvSkipped {
		t.Error("env file should not be touched on second run")
	}
	body2, _ := os.ReadFile(envPath)
	if string(body) != string(body2) {
		t.Error("env file contents drifted between runs")
	}
}

func TestSelfhost_FailsWithoutGin(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	wipeAll(t, ctx, pool)
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	// Note: gin intentionally NOT seeded.
	_, err = Run(ctx, pool, Options{EnvFile: filepath.Join(t.TempDir(), ".duckllo.env")})
	if err == nil {
		t.Fatal("expected an error when gin is missing")
	}
	if !strings.Contains(err.Error(), "gin") {
		t.Errorf("error should mention gin: %v", err)
	}
}
