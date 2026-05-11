package webui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveDir_FlagWinsOverEnv(t *testing.T) {
	// Both set: --web-dir takes precedence over DUCKLLO_WEB_DIR.
	if got := ResolveDir("/from/flag", "/from/env"); got != "/from/flag" {
		t.Errorf("flag should win when both set, got %q want %q", got, "/from/flag")
	}
	// Only env set: env is used.
	if got := ResolveDir("", "/from/env"); got != "/from/env" {
		t.Errorf("env should be used when flag empty, got %q want %q", got, "/from/env")
	}
	// Neither set: empty → caller will serve from embed.
	if got := ResolveDir("", ""); got != "" {
		t.Errorf("both empty should yield empty, got %q", got)
	}
	// Only flag set: flag is used.
	if got := ResolveDir("/from/flag", ""); got != "/from/flag" {
		t.Errorf("flag should be used when env empty, got %q want %q", got, "/from/flag")
	}
}

func TestResolve_EmptyUsesEmbed(t *testing.T) {
	_, label, dev := Resolve("")
	if label != "embed" {
		t.Errorf("empty webDir should label 'embed', got %q", label)
	}
	if dev {
		t.Errorf("empty webDir should not be dev mode")
	}
}

func TestResolve_MissingPathFallsBackToEmbed(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, label, dev := Resolve(missing)
	if label != "embed" {
		t.Errorf("missing path should fall back to embed, got label %q", label)
	}
	if dev {
		t.Errorf("missing path should not be dev mode")
	}
}

func TestResolve_NotADirFallsBackToEmbed(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "regular.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, label, dev := Resolve(file)
	if label != "embed" {
		t.Errorf("regular file path should fall back to embed, got label %q", label)
	}
	if dev {
		t.Errorf("regular file path should not be dev mode")
	}
}

func TestResolve_ValidDirIsDev(t *testing.T) {
	dir := t.TempDir()
	_, label, dev := Resolve(dir)
	if !dev {
		t.Errorf("valid directory should enable dev mode")
	}
	if label == "embed" {
		t.Errorf("valid directory should not label 'embed', got %q", label)
	}
}
