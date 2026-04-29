// Package webui exposes the static Web UI assets as an embedded fs.FS.
// The Go binary is single-file; there is no Node toolchain in the build
// pipeline. Plain ES2022 modules and CSS are served as-is.
package webui

import (
	"embed"
	"io/fs"
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
