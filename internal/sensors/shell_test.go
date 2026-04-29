package sensors

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestShellSensor_PassWithCustomCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on Unix shell")
	}
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "ok.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := NewShellSensor("unit_test", []string{"go", "version"})
	c := Criterion{
		ID:   "c1",
		Text: "ls works",
		SensorSpec: map[string]any{
			"command": []any{"ls", "ok.txt"},
		},
	}
	res, err := s.Run(context.Background(), c, Env{WorkspaceDir: tmp})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "pass" {
		t.Errorf("status: got %s want pass", res.Status)
	}
	if !strings.Contains(string(res.ArtifactBytes), "ok.txt") {
		t.Errorf("artifact missing filename: %s", res.ArtifactBytes)
	}
	if res.Class != "computational" {
		t.Errorf("class: got %s want computational", res.Class)
	}
}

func TestShellSensor_FailExitCodePropagates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on Unix shell")
	}
	s := NewShellSensor("unit_test", []string{"ls"})
	c := Criterion{
		ID: "c1",
		SensorSpec: map[string]any{
			"command": []any{"ls", "/no-such-path-please-trust-me-123456"},
		},
	}
	res, err := s.Run(context.Background(), c, Env{WorkspaceDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "fail" {
		t.Errorf("status: got %s want fail", res.Status)
	}
	if !strings.Contains(res.Summary, "fail") {
		t.Errorf("summary should mention failure: %q", res.Summary)
	}
}

func TestShellSensor_FallbackToDefaultCommand(t *testing.T) {
	// The criterion has no `command`, so the default kicks in.
	s := NewShellSensor("typecheck", []string{"true"})
	c := Criterion{ID: "c", SensorKind: "typecheck"}
	res, err := s.Run(context.Background(), c, Env{WorkspaceDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "pass" {
		t.Errorf("status: got %s want pass", res.Status)
	}
}

func TestShellSensor_SkippedWhenNoCommand(t *testing.T) {
	s := NewShellSensor("e2e_test", nil)
	res, err := s.Run(context.Background(), Criterion{ID: "c"}, Env{WorkspaceDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "skipped" {
		t.Errorf("status: got %s want skipped", res.Status)
	}
}

func TestShellSensor_StringCommandSpecIsSplit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on Unix shell")
	}
	s := NewShellSensor("unit_test", nil)
	c := Criterion{
		ID:         "c",
		SensorSpec: map[string]any{"command": "echo hello world"},
	}
	res, err := s.Run(context.Background(), c, Env{WorkspaceDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "pass" {
		t.Errorf("status: got %s want pass — summary=%s", res.Status, res.Summary)
	}
	if !strings.Contains(string(res.ArtifactBytes), "hello world") {
		t.Errorf("output: %s", res.ArtifactBytes)
	}
}
