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
//	url             string        absolute URL or path appended to env.DevURL
//	selector        string        optional CSS selector to wait for + screenshot
//	hover_selector  string        optional — fire mouseover on this element
//	                              before the capture, so a tooltip / popover
//	                              that needs hover to appear is in the shot
//	hover_delay_ms  int           wait this long after the hover (default 250ms)
//	viewport        {w:int,h:int} optional, default 1280x800
//	full_page       bool          if true, capture full scrollable page
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

	// Auth-bootstrap: visit the dev origin once so localStorage is
	// addressable, set the duckllo.token key, and only then drive to
	// the target URL. Without this every screenshot of a protected
	// page captures the login screen instead.
	authTasks := chromedp.Tasks{}
	if env.AuthToken != "" && env.DevURL != "" {
		authTasks = append(authTasks,
			chromedp.Navigate(env.DevURL),
			chromedp.Evaluate(`localStorage.setItem('duckllo.token', `+jsString(env.AuthToken)+`)`, nil),
		)
	}
	var pngBytes []byte
	tasks := append(authTasks, chromedp.Navigate(url))
	if selector != "" {
		tasks = append(tasks, chromedp.WaitVisible(selector, chromedp.ByQuery))
	}
	// hover_selector: dispatch mouseover/mouseenter/mousemove on the
	// target so the popover or tooltip that depends on hover is in the
	// captured frame. JS dispatch is more reliable than CDP mouse-move
	// because pointer-events:none on the popover would otherwise rely
	// on real cursor coordinates that don't survive headless mode.
	if hoverSel, ok := c.SensorSpec["hover_selector"].(string); ok && hoverSel != "" {
		tasks = append(tasks, chromedp.WaitVisible(hoverSel, chromedp.ByQuery))
		tasks = append(tasks, chromedp.Evaluate(`(function(s){var el=document.querySelector(s);`+
			`if(!el) throw new Error('hover_selector not found: '+s);`+
			`var r=el.getBoundingClientRect();`+
			`['mouseover','mouseenter','mousemove'].forEach(function(e){`+
			`el.dispatchEvent(new MouseEvent(e,{bubbles:true,cancelable:true,clientX:r.left+r.width/2,clientY:r.top+r.height/2}));`+
			`});})(`+jsString(hoverSel)+`)`, nil))
		delay := 250 * time.Millisecond
		if v, ok := numAsInt(c.SensorSpec["hover_delay_ms"]); ok && v > 0 {
			delay = time.Duration(v) * time.Millisecond
		}
		tasks = append(tasks, chromedp.Sleep(delay))
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

	// If the criterion has a baseline_url, run a pixel diff and post a
	// visual_diff verification. Falling back to a plain screenshot if the
	// baseline can't be fetched keeps the sensor robust against flaky
	// network conditions or a freshly-rotated baseline reference.
	if baseURL, ok := c.SensorSpec["baseline_url"].(string); ok && baseURL != "" && env.Fetch != nil {
		baselineBytes, err := env.Fetch(ctx, baseURL)
		if err != nil {
			return &Result{
				Status: "warn", Class: "computational",
				Summary: "could not fetch baseline (" + oneLine(err.Error()) + ") — falling back to screenshot",
				ArtifactBytes: pngBytes, ContentType: "image/png", FileName: "screenshot.png",
				Details: map[string]any{"url": url, "baseline_url": baseURL},
			}, nil
		}
		tolerance := 16
		if t, ok := numAsInt(c.SensorSpec["tolerance"]); ok {
			tolerance = t
		}
		diffPNG, diffPx, total, err := PixelDiff(baselineBytes, pngBytes, tolerance)
		if err != nil {
			return &Result{
				Status: "warn", Class: "computational",
				Summary: "pixel diff error: " + oneLine(err.Error()),
				ArtifactBytes: pngBytes, ContentType: "image/png", FileName: "screenshot.png",
			}, nil
		}
		percent := 0.0
		if total > 0 {
			percent = float64(diffPx) * 100 / float64(total)
		}
		threshold := 0.5 // %
		if t, ok := c.SensorSpec["diff_threshold"].(float64); ok {
			threshold = t
		}
		status := "pass"
		if percent > threshold {
			status = "fail"
		}
		return &Result{
			Status: status, Class: "computational",
			Summary: fmt.Sprintf("%.2f%% pixels differ (%d / %d, threshold %.2f%%)",
				percent, diffPx, total, threshold),
			ArtifactBytes: diffPNG, ContentType: "image/png", FileName: "diff.png",
			Details: map[string]any{
				"url": url, "selector": selector, "baseline_url": baseURL,
				"diff_pixels": diffPx, "total_pixels": total,
				"percent_diff": percent, "tolerance": tolerance, "threshold": threshold,
			},
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
