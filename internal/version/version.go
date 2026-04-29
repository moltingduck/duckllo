// Package version exposes the binary version. The value is set by ldflags
// at build time, e.g. `-ldflags="-X .../version.Version=0.1.0"`. The default
// "dev" value is what `go run` and `go build` without ldflags produce.
package version

import "time"

var Version = "dev"

// StartedAt records process boot so /api/status can report uptime.
var StartedAt = time.Now()
