package dotenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reset clears each named env var both before and after the test so
// concurrent tests inside a single package run can't leak state.
func reset(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		_ = os.Unsetenv(k)
	}
	t.Cleanup(func() {
		for _, k := range keys {
			_ = os.Unsetenv(k)
		}
	})
}

func TestParseHappyPaths(t *testing.T) {
	reset(t, "DUCKLLO_T_BASIC", "DUCKLLO_T_QUOTED", "DUCKLLO_T_SINGLE",
		"DUCKLLO_T_EXPORT", "DUCKLLO_T_COMMENT", "DUCKLLO_T_BLANK")

	in := strings.Join([]string{
		`# leading comment`,
		``,
		`DUCKLLO_T_BASIC=plainvalue`,
		`DUCKLLO_T_QUOTED="hello world"`,
		`DUCKLLO_T_SINGLE='value with $special chars'`,
		`export DUCKLLO_T_EXPORT=fromShellSnippet`,
		`# inline comment line`,
		`DUCKLLO_T_COMMENT=keep`,
		`DUCKLLO_T_BLANK=`,
		``,
	}, "\n")

	if err := parse(strings.NewReader(in)); err != nil {
		t.Fatalf("parse: %v", err)
	}

	cases := map[string]string{
		"DUCKLLO_T_BASIC":   "plainvalue",
		"DUCKLLO_T_QUOTED":  "hello world",
		"DUCKLLO_T_SINGLE":  "value with $special chars",
		"DUCKLLO_T_EXPORT":  "fromShellSnippet",
		"DUCKLLO_T_COMMENT": "keep",
		"DUCKLLO_T_BLANK":   "",
	}
	for k, want := range cases {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s: got %q want %q", k, got, want)
		}
	}
}

func TestProcessEnvWinsOverFile(t *testing.T) {
	reset(t, "DUCKLLO_T_OVERRIDE")
	if err := os.Setenv("DUCKLLO_T_OVERRIDE", "from-process"); err != nil {
		t.Fatal(err)
	}
	if err := parse(strings.NewReader("DUCKLLO_T_OVERRIDE=from-file\n")); err != nil {
		t.Fatal(err)
	}
	if v := os.Getenv("DUCKLLO_T_OVERRIDE"); v != "from-process" {
		t.Errorf("file overrode process env: got %q want from-process", v)
	}
}

func TestParseSkipsMalformed(t *testing.T) {
	reset(t, "DUCKLLO_T_GOOD")
	in := strings.Join([]string{
		"=onlyvalue",   // no key
		"justakey",     // no equals
		"DUCKLLO_T_GOOD=ok",
	}, "\n")
	if err := parse(strings.NewReader(in)); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v := os.Getenv("DUCKLLO_T_GOOD"); v != "ok" {
		t.Errorf("good line was dropped alongside malformed siblings: got %q", v)
	}
}

func TestLoadDefault(t *testing.T) {
	reset(t, "DUCKLLO_T_FROM_FILE")

	dir := t.TempDir()
	path := filepath.Join(dir, ".duckllo.env")
	if err := os.WriteFile(path, []byte("DUCKLLO_T_FROM_FILE=present\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// LoadDefault uses the cwd, so step into the tempdir.
	prev, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(prev) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	if loaded == "" {
		t.Fatal("LoadDefault returned empty path; expected the tempdir's .duckllo.env")
	}
	if v := os.Getenv("DUCKLLO_T_FROM_FILE"); v != "present" {
		t.Errorf("env not set: got %q", v)
	}
}

func TestLoadDefaultNoFile(t *testing.T) {
	dir := t.TempDir()
	prev, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(prev) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	if loaded != "" {
		t.Errorf("expected empty path when no file present; got %q", loaded)
	}
}
