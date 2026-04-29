package sensors

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

// ScreenshotSensor drives a headless Chrome via CDP to capture a PNG of
// the dev server's UI. sensor_spec keys the sensor accepts:
//
//	url       string        absolute URL or path appended to env.DevURL
//	selector  string        optional CSS selector to wait for + screenshot
//	viewport  {w:int,h:int} optional, default 1280x800
//	full_page bool          if true, capture full scrollable page
//
// Phase 1 produces the screenshot only; baseline comparison + visual diff
// land alongside the annotator (a follow-up commit). Status is reported
// as 'pass' when capture succeeds and 'fail' when chromedp errors out.
type ScreenshotSensor struct {
	timeout time.Duration
}

func NewScreenshotSensor() *ScreenshotSensor {
	return &ScreenshotSensor{timeout: 90 * time.Second}
}

func (s *ScreenshotSensor) Kind() string { return "screenshot" }

func (s *ScreenshotSensor) Run(ctx context.Context, c Criterion, env Env) (*Result, error) {
	url := s.urlFor(c, env)
	if url == "" {
		return &Result{Status: "skipped", Class: "computational",
			Summary: "no URL configured for screenshot sensor"}, nil
	}

	w, h := s.viewport(c)
	selector, _ := c.SensorSpec["selector"].(string)
	fullPage, _ := c.SensorSpec["full_page"].(bool)

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.WindowSize(w, h),
		chromedp.NoSandbox,
	)
	if env.ChromePath != "" {
		opts = append(opts, chromedp.ExecPath(env.ChromePath))
	}

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()

	tctx, cancel := context.WithTimeout(browserCtx, s.timeout)
	defer cancel()

	var pngBytes []byte
	tasks := chromedp.Tasks{chromedp.Navigate(url)}
	if selector != "" {
		tasks = append(tasks, chromedp.WaitVisible(selector, chromedp.ByQuery))
	}
	if fullPage {
		tasks = append(tasks, chromedp.FullScreenshot(&pngBytes, 90))
	} else if selector != "" {
		tasks = append(tasks, chromedp.Screenshot(selector, &pngBytes, chromedp.NodeVisible, chromedp.ByQuery))
	} else {
		tasks = append(tasks, chromedp.CaptureScreenshot(&pngBytes))
	}

	if err := chromedp.Run(tctx, tasks); err != nil {
		return &Result{
			Status:  "fail",
			Class:   "computational",
			Summary: "chromedp: " + oneLine(err.Error()),
			Details: map[string]any{"url": url, "selector": selector},
		}, nil
	}

	return &Result{
		Status: "pass", Class: "computational",
		Summary: fmt.Sprintf("captured %dx%d screenshot of %s", w, h, url),
		ArtifactBytes: pngBytes, ContentType: "image/png", FileName: "screenshot.png",
		Details: map[string]any{
			"url": url, "selector": selector, "viewport": map[string]any{"w": w, "h": h},
		},
	}, nil
}

func (s *ScreenshotSensor) urlFor(c Criterion, env Env) string {
	if u, ok := c.SensorSpec["url"].(string); ok && u != "" {
		if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
			return u
		}
		base := strings.TrimRight(env.DevURL, "/")
		if base == "" {
			return ""
		}
		if !strings.HasPrefix(u, "/") {
			u = "/" + u
		}
		return base + u
	}
	if env.DevURL != "" {
		return env.DevURL
	}
	return ""
}

func (s *ScreenshotSensor) viewport(c Criterion) (int, int) {
	w, h := 1280, 800
	if vp, ok := c.SensorSpec["viewport"].(map[string]any); ok {
		if x, ok := numAsInt(vp["w"]); ok {
			w = x
		}
		if y, ok := numAsInt(vp["h"]); ok {
			h = y
		}
	}
	return w, h
}

func numAsInt(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case int:
		return x, true
	}
	return 0, false
}
