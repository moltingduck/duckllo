package sensors

import (
	"bytes"
	"context"
	"image/gif"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGIFSensor_RecordsScenarioFrames(t *testing.T) {
	requireChrome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`<!doctype html><html><body><h1 id="t">page A</h1></body></html>`))
		case "/two":
			_, _ = w.Write([]byte(`<!doctype html><html><body><h1 id="t">page B</h1></body></html>`))
		}
	}))
	t.Cleanup(srv.Close)

	s := NewGIFSensor()
	c := Criterion{
		ID: "c", SensorKind: "gif",
		SensorSpec: map[string]any{
			"scenario": []any{
				map[string]any{"action": "navigate", "url": "/"},
				map[string]any{"action": "wait", "selector": "#t"},
				map[string]any{"action": "navigate", "url": "/two"},
				map[string]any{"action": "wait", "selector": "#t"},
			},
			"viewport":       map[string]any{"w": 320, "h": 200},
			"frame_delay_ms": 100,
		},
	}
	env := Env{DevURL: srv.URL}
	res, err := s.Run(context.Background(), c, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "pass" {
		t.Fatalf("status: got %s want pass — summary=%q", res.Status, res.Summary)
	}
	if res.ContentType != "image/gif" {
		t.Errorf("content type: got %s", res.ContentType)
	}
	if len(res.ArtifactBytes) == 0 {
		t.Fatal("artifact empty")
	}

	// Decode and assert frame count matches scenario length.
	g, err := gif.DecodeAll(bytes.NewReader(res.ArtifactBytes))
	if err != nil {
		t.Fatalf("decode GIF: %v", err)
	}
	if len(g.Image) != 4 {
		t.Errorf("frames: got %d want 4", len(g.Image))
	}
	// Details map is populated by the sensor without going through JSON, so
	// the int stays an int — assert against the concrete type.
	if frames, _ := res.Details["frames"].(int); frames != 4 {
		t.Errorf("details.frames: got %v want 4", res.Details["frames"])
	}
}

func TestGIFSensor_SkipsWithoutScenario(t *testing.T) {
	s := NewGIFSensor()
	res, err := s.Run(context.Background(),
		Criterion{ID: "c", SensorKind: "gif"},
		Env{DevURL: "http://localhost"},
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "skipped" {
		t.Errorf("status: got %s want skipped", res.Status)
	}
	if !strings.Contains(res.Summary, "no scenario") {
		t.Errorf("summary: %q", res.Summary)
	}
}

func TestGIFSensor_FailsOnUnknownAction(t *testing.T) {
	requireChrome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html></html>"))
	}))
	t.Cleanup(srv.Close)

	s := NewGIFSensor()
	c := Criterion{
		ID: "c", SensorKind: "gif",
		SensorSpec: map[string]any{
			"scenario": []any{
				map[string]any{"action": "navigate", "url": "/"},
				map[string]any{"action": "summon-dragons"},
			},
		},
	}
	res, err := s.Run(context.Background(), c, Env{DevURL: srv.URL})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "fail" {
		t.Errorf("status: got %s want fail — summary=%q", res.Status, res.Summary)
	}
}
