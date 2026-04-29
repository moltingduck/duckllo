package workspace

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
)

func TestHostExecutor_ReadWriteRoundTrip(t *testing.T) {
	h := NewHost(t.TempDir())
	ctx := context.Background()

	if err := h.WriteFile(ctx, "hello.txt", []byte("hi")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := h.ReadFile(ctx, "hello.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hi" {
		t.Errorf("ReadFile: got %q want %q", got, "hi")
	}
}

func TestHostExecutor_PathContainment(t *testing.T) {
	h := NewHost(t.TempDir())
	ctx := context.Background()
	cases := []string{"../../etc/passwd", "/etc/passwd", "..\\windows\\system32"}
	for _, p := range cases {
		if _, err := h.ReadFile(ctx, p); !errors.Is(err, ErrPathEscapesWorkspace) {
			t.Errorf("ReadFile(%q): expected ErrPathEscapesWorkspace, got %v", p, err)
		}
		if err := h.WriteFile(ctx, p, []byte("x")); !errors.Is(err, ErrPathEscapesWorkspace) {
			t.Errorf("WriteFile(%q): expected ErrPathEscapesWorkspace, got %v", p, err)
		}
	}
}

func TestHostExecutor_WriteCreatesParentDirs(t *testing.T) {
	h := NewHost(t.TempDir())
	ctx := context.Background()
	if err := h.WriteFile(ctx, "a/b/c.txt", []byte("nested")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := h.ReadFile(ctx, "a/b/c.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "nested" {
		t.Errorf("got %q", got)
	}
}

func TestHostExecutor_ListDirSortedWithSlashes(t *testing.T) {
	h := NewHost(t.TempDir())
	ctx := context.Background()
	if err := h.WriteFile(ctx, "z.txt", []byte{}); err != nil {
		t.Fatal(err)
	}
	if err := h.WriteFile(ctx, "a/inner.txt", []byte{}); err != nil {
		t.Fatal(err)
	}
	if err := h.WriteFile(ctx, "m.txt", []byte{}); err != nil {
		t.Fatal(err)
	}
	entries, err := h.ListDir(ctx, ".")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	want := []string{"a/", "m.txt", "z.txt"}
	if len(entries) != len(want) {
		t.Fatalf("ListDir: got %v want %v", entries, want)
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Errorf("ListDir[%d]: got %q want %q", i, entries[i], want[i])
		}
	}
}

func TestHostExecutor_ExecAllowList(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on Unix shell utilities")
	}
	h := NewHost(t.TempDir())
	ctx := context.Background()

	// `echo` is on the allow-list and on PATH everywhere we test.
	out, err := h.Exec(ctx, []string{"echo", "hi"})
	if err != nil {
		t.Fatalf("Exec echo: %v", err)
	}
	if !strings.Contains(string(out), "hi") {
		t.Errorf("echo output: %q", out)
	}

	if _, err := h.Exec(ctx, []string{"rm", "-rf", "/"}); err == nil {
		t.Error("rm should be blocked by the allow-list")
	}
}

func TestHostExecutor_KindAndCloseAreInert(t *testing.T) {
	h := NewHost(t.TempDir())
	if h.Kind() != "host" {
		t.Errorf("Kind: got %q want host", h.Kind())
	}
	if err := h.Close(context.Background()); err != nil {
		t.Errorf("Close: %v", err)
	}
}
