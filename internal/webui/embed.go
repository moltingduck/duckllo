// Package webui exposes the static Web UI assets as an embedded fs.FS.
// The Go binary is single-file; there is no Node toolchain in the build
// pipeline. Plain ES2022 modules and CSS are served as-is.
package webui

import (
	"embed"
	"io/fs"
	"log"
	"os"
	"path/filepath"
)

//go:embed all:web
var rawFS embed.FS

// Assets returns the embedded fs rooted at "web/" so callers can mount it
// directly at "/" without an extra prefix.
func Assets() fs.FS {
	sub, err := fs.Sub(rawFS, "web")
	if err != nil {
		panic("webui: subfs failed: " + err.Error())
	}
	return sub
}

// ResolveDir picks the effective web-dir path given a flag value and an
// environment-loaded value. The flag wins when non-empty; otherwise the
// env value is used. Both empty means "serve from embed".
//
// Kept as a tiny pure helper so the precedence rule is unit-testable
// without spinning up the whole serve subcommand.
func ResolveDir(flagVal, envVal string) string {
	if flagVal != "" {
		return flagVal
	}
	return envVal
}

// Resolve returns the fs.FS the server should mount for the Web UI, a
// human-readable label for the startup log, and whether dev mode is
// active (i.e. assets are coming from disk, not embed).
//
// webDir == "" → embed.FS. webDir non-empty and stat'able as a directory
// → os.DirFS(absPath). webDir non-empty but missing / not a directory →
// log a warn line and fall back to embed (never fatal: a typoed path
// shouldn't take the server down).
func Resolve(webDir string) (assets fs.FS, label string, dev bool) {
	if webDir == "" {
		return Assets(), "embed", false
	}
	abs, err := filepath.Abs(webDir)
	if err != nil {
		log.Printf("warn: web-dir %q: %v — falling back to embed", webDir, err)
		return Assets(), "embed", false
	}
	info, err := os.Stat(abs)
	if err != nil {
		log.Printf("warn: web-dir %q not accessible (%v) — falling back to embed", abs, err)
		return Assets(), "embed", false
	}
	if !info.IsDir() {
		log.Printf("warn: web-dir %q is not a directory — falling back to embed", abs)
		return Assets(), "embed", false
	}
	return os.DirFS(abs), abs, true
}
