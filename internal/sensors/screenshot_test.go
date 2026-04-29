package sensors

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// requireChrome skips when chromedp can't reach a Chrome binary in the
// allocator's default search path. Avoids leaving a broken test session
// on CI workers that don't ship Chromium.
func requireChrome(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, chromedp.DefaultExecAllocatorOptions[:]...)
	defer cancelAlloc()
	bctx, cancelB := chromedp.NewContext(allocCtx)
	defer cancelB()
	if err := chromedp.Run(bctx, chromedp.Tasks{}); err != nil {
		t.Skipf("chromedp not usable on this host: %v", err)
	}
}

func staticHTML(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestScreenshot_PassesAndProducesPNG(t *testing.T) {
	requireChrome(t)
	srv := staticHTML(t, `<!doctype html><html><body style="background:#222;color:#fff"><h1 id="hi">Hello</h1></body></html>`)

	s := NewScreenshotSensor()
	c := Criterion{
		ID: "c1", Text: "renders", SensorKind: "screenshot",
		SensorSpec: map[string]any{"url": "/", "selector": "#hi"},
	}
	env := Env{DevURL: srv.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := s.Run(ctx, c, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "pass" {
		t.Errorf("status: got %s want pass — summary=%s", res.Status, res.Summary)
	}
	if len(res.ArtifactBytes) == 0 {
		t.Fatal("artifact empty — chromedp produced no PNG")
	}
	if _, err := png.Decode(bytes.NewReader(res.ArtifactBytes)); err != nil {
		t.Fatalf("artifact is not a valid PNG: %v", err)
	}
	if res.ContentType != "image/png" || res.FileName != "screenshot.png" {
		t.Errorf("artifact metadata: got %+v", res)
	}
}

func TestScreenshot_VisualDiff_WhenBaselineMatches(t *testing.T) {
	requireChrome(t)
	// Two pages that render identically. The current capture's first run
	// becomes the baseline; the second run should compute zero diff.
	srv := staticHTML(t, `<!doctype html><html><body><div style="width:200px;height:200px;background:#3fb950"></div></body></html>`)
	s := NewScreenshotSensor()

	first, err := s.Run(context.Background(), Criterion{
		ID: "c", Text: "diff",
		SensorKind: "screenshot",
		SensorSpec: map[string]any{},
	}, Env{DevURL: srv.URL})
	if err != nil {
		t.Fatalf("first capture: %v", err)
	}
	if len(first.ArtifactBytes) == 0 {
		t.Fatal("first capture empty")
	}
	baseline := append([]byte(nil), first.ArtifactBytes...)

	// Now use the same baseline but a slightly different page. The diff
	// should fire.
	srvDifferent := staticHTML(t, `<!doctype html><html><body><div style="width:200px;height:200px;background:#f85149"></div></body></html>`)
	c := Criterion{
		ID: "c", Text: "diff",
		SensorKind: "screenshot",
		SensorSpec: map[string]any{
			"baseline_url":   "memory://baseline",
			"diff_threshold": 0.5,
		},
	}
	env := Env{
		DevURL: srvDifferent.URL,
		Fetch: func(_ context.Context, _ string) ([]byte, error) {
			return baseline, nil
		},
	}
	res, err := s.Run(context.Background(), c, env)
	if err != nil {
		t.Fatalf("diff Run: %v", err)
	}
	if res.Status != "fail" {
		t.Errorf("status: got %s want fail (page changed colour) — summary=%q", res.Status, res.Summary)
	}
	// Details should include a non-zero percent_diff.
	pd, _ := res.Details["percent_diff"].(float64)
	if pd <= 0.0 {
		t.Errorf("percent_diff: got %v want >0", res.Details["percent_diff"])
	}
}

func TestScreenshot_FetchErrorFallsBackToScreenshot(t *testing.T) {
	requireChrome(t)
	srv := staticHTML(t, `<!doctype html><html><body>fallback</body></html>`)
	s := NewScreenshotSensor()
	c := Criterion{
		ID: "c", SensorKind: "screenshot",
		SensorSpec: map[string]any{"baseline_url": "anything"},
	}
	env := Env{
		DevURL: srv.URL,
		Fetch:  func(_ context.Context, _ string) ([]byte, error) { return nil, http.ErrServerClosed },
	}
	res, err := s.Run(context.Background(), c, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "warn" {
		t.Errorf("status: got %s want warn — summary=%q", res.Status, res.Summary)
	}
	if len(res.ArtifactBytes) == 0 {
		t.Error("fallback should still attach the captured screenshot")
	}
}

func TestScreenshot_SkipsWithoutURL(t *testing.T) {
	s := NewScreenshotSensor()
	res, err := s.Run(context.Background(),
		Criterion{ID: "c", SensorKind: "screenshot"},
		Env{},
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "skipped" {
		t.Errorf("status: got %s want skipped", res.Status)
	}
}

// keep the image package referenced — used implicitly by png.Decode but
// the linter sometimes flags it on minimal-test builds.
var _ image.Image = (*image.RGBA)(nil)
var _ = color.RGBA{}
