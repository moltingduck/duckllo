package sensors

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ShellSensor is the workhorse for lint, typecheck, unit_test, e2e_test,
// and build criteria. The criterion's sensor_spec.command is an argv array
// like ["go", "test", "./..."]. Pass = exit 0. Output above MaxOutputBytes
// is truncated.
//
// We do *not* re-validate the argv against an allow-list here because the
// runner already enforces that on the executor's exec tool; the validator
// runs project-defined commands by design.
type ShellSensor struct {
	kind            string
	defaultCommand  []string
	maxOutputBytes  int
	timeout         time.Duration
}

func NewShellSensor(kind string, defaultCommand []string) *ShellSensor {
	return &ShellSensor{
		kind:           kind,
		defaultCommand: defaultCommand,
		maxOutputBytes: 256 * 1024,
		timeout:        5 * time.Minute,
	}
}

func (s *ShellSensor) Kind() string { return s.kind }

func (s *ShellSensor) Run(ctx context.Context, c Criterion, env Env) (*Result, error) {
	argv := s.argvFor(c)
	if len(argv) == 0 {
		return &Result{Status: "skipped", Class: "computational",
			Summary: "no command configured for " + s.kind}, nil
	}

	cctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	cmd.Dir = env.WorkspaceDir

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err := cmd.Run()
	out := buf.Bytes()
	if len(out) > s.maxOutputBytes {
		out = append(out[:s.maxOutputBytes], []byte("\n[truncated]")...)
	}

	status := "pass"
	summary := fmt.Sprintf("%s passed", s.kind)
	if cctx.Err() == context.DeadlineExceeded {
		status = "fail"
		summary = fmt.Sprintf("%s timed out after %s", s.kind, s.timeout)
	} else if err != nil {
		status = "fail"
		summary = fmt.Sprintf("%s failed: %s", s.kind, oneLine(err.Error()))
	}

	return &Result{
		Status: status, Class: "computational", Summary: summary,
		ArtifactBytes: out, ContentType: "text/plain", FileName: s.kind + ".log",
		Details: map[string]any{
			"argv": argv, "exit_error": errStr(err),
		},
	}, nil
}

func (s *ShellSensor) argvFor(c Criterion) []string {
	if raw, ok := c.SensorSpec["command"]; ok {
		switch v := raw.(type) {
		case []any:
			out := make([]string, 0, len(v))
			for _, x := range v {
				if str, ok := x.(string); ok {
					out = append(out, str)
				}
			}
			return out
		case string:
			// Allow a single shell line; split on whitespace conservatively.
			return strings.Fields(v)
		}
	}
	return append([]string{}, s.defaultCommand...)
}

func errStr(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
