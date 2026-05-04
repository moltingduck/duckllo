package sensors

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/png"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

// GIFSensor records a multi-frame GIF by walking through a scripted
// browser scenario. The criterion's sensor_spec carries:
//
//	scenario:        array of {action, ...} objects
//	  action=navigate    url:    relative or absolute
//	  action=click       selector
//	  action=hover       selector  (mouse-over for tooltips/popups)
//	  action=type        selector, text
//	  action=wait        selector  (waits for visible)
//	  action=sleep       sleep_ms
//	viewport:        {w,h}            default 1280x800
//	frame_delay_ms:  int              default 200ms (5fps)
//
// One frame is captured *after* each scenario step. Quantisation uses
// palette.Plan9 (216 web-safe colours) — fine for UI demos, not
// photographic content.
type GIFSensor struct {
	timeout time.Duration
}

func NewGIFSensor() *GIFSensor { return &GIFSensor{timeout: 3 * time.Minute} }

func (g *GIFSensor) Kind() string { return "gif" }

type gifAction struct {
	Action   string `json:"action"`
	URL      string `json:"url,omitempty"`
	Selector string `json:"selector,omitempty"`
	Text     string `json:"text,omitempty"`
	SleepMs  int    `json:"sleep_ms,omitempty"`
}

func (g *GIFSensor) Run(ctx context.Context, c Criterion, env Env) (*Result, error) {
	raw, ok := c.SensorSpec["scenario"].([]any)
	if !ok || len(raw) == 0 {
		return &Result{Status: "skipped", Class: "computational",
			Summary: "no scenario configured for gif sensor"}, nil
	}
	actions := make([]gifAction, 0, len(raw))
	for _, x := range raw {
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		a := gifAction{
			Action:   getStr(m, "action"),
			URL:      getStr(m, "url"),
			Selector: getStr(m, "selector"),
			Text:     getStr(m, "text"),
		}
		if v, ok := numAsInt(m["sleep_ms"]); ok {
			a.SleepMs = v
		}
		actions = append(actions, a)
	}

	w, h := 1280, 800
	if vp, ok := c.SensorSpec["viewport"].(map[string]any); ok {
		if x, ok := numAsInt(vp["w"]); ok {
			w = x
		}
		if y, ok := numAsInt(vp["h"]); ok {
			h = y
		}
	}
	delayMs := 200
	if d, ok := numAsInt(c.SensorSpec["frame_delay_ms"]); ok {
		delayMs = d
	}
	delayCs := delayMs / 10
	if delayCs < 1 {
		delayCs = 1
	}

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
	tctx, cancel := context.WithTimeout(browserCtx, g.timeout)
	defer cancel()

	base := strings.TrimRight(env.DevURL, "/")

	// Auth-bootstrap: prime localStorage with the runner's bearer
	// before the scenario starts so any subsequent navigation to a
	// protected page is logged in. Skipped when there's no AuthToken
	// or DevURL because there's nowhere to attach storage.
	if env.AuthToken != "" && base != "" {
		if err := chromedp.Run(tctx,
			chromedp.Navigate(base),
			chromedp.Evaluate(`localStorage.setItem('duckllo.token', `+jsString(env.AuthToken)+`)`, nil),
		); err != nil {
			return &Result{Status: "fail", Class: "computational",
				Summary: "auth bootstrap failed: " + oneLine(err.Error())}, nil
		}
	}

	var frames []*image.Paletted
	var delays []int

	for i, a := range actions {
		task, err := buildTask(a, base)
		if err != nil {
			return &Result{Status: "fail", Class: "computational",
				Summary: fmt.Sprintf("step %d (%s): %s", i, a.Action, oneLine(err.Error()))}, nil
		}
		if err := chromedp.Run(tctx, task); err != nil {
			return &Result{Status: "fail", Class: "computational",
				Summary: fmt.Sprintf("step %d (%s): %s", i, a.Action, oneLine(err.Error()))}, nil
		}

		var pngBytes []byte
		if err := chromedp.Run(tctx, chromedp.CaptureScreenshot(&pngBytes)); err != nil {
			return &Result{Status: "fail", Class: "computational",
				Summary: "frame capture failed: " + oneLine(err.Error())}, nil
		}
		img, err := png.Decode(bytes.NewReader(pngBytes))
		if err != nil {
			return &Result{Status: "fail", Class: "computational",
				Summary: "frame decode failed: " + oneLine(err.Error())}, nil
		}
		pi := image.NewPaletted(img.Bounds(), palette.Plan9)
		draw.Draw(pi, pi.Bounds(), img, image.Point{}, draw.Src)
		frames = append(frames, pi)
		delays = append(delays, delayCs)
	}

	if len(frames) == 0 {
		return &Result{Status: "skipped", Class: "computational",
			Summary: "scenario produced no frames"}, nil
	}

	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, &gif.GIF{Image: frames, Delay: delays, LoopCount: 0}); err != nil {
		return nil, err
	}

	return &Result{
		Status: "pass", Class: "computational",
		Summary:       fmt.Sprintf("%d frames captured", len(frames)),
		ArtifactBytes: buf.Bytes(),
		ContentType:   "image/gif",
		FileName:      "scenario.gif",
		Details: map[string]any{
			"frames":         len(frames),
			"frame_delay_ms": delayMs,
		},
	}, nil
}

func buildTask(a gifAction, base string) (chromedp.Tasks, error) {
	switch a.Action {
	case "navigate":
		target := a.URL
		if !strings.HasPrefix(target, "http") {
			if !strings.HasPrefix(target, "/") {
				target = "/" + target
			}
			target = base + target
		}
		return chromedp.Tasks{chromedp.Navigate(target)}, nil
	case "click":
		return chromedp.Tasks{chromedp.Click(a.Selector, chromedp.ByQuery)}, nil
	case "hover":
		// chromedp doesn't expose a native Hover() — the closest stable
		// path is dispatching a real mouseover via JS so listeners fire.
		// pointer-events:none elements (like our hover card) ignore JS
		// events anyway, so this is the right level: drive the *thing*
		// the user mouses, not the popup it produces.
		js := `(function(s){var el=document.querySelector(s);` +
			`if(!el) throw new Error('selector not found: '+s);` +
			`var r=el.getBoundingClientRect();` +
			`['mouseover','mouseenter','mousemove'].forEach(function(e){` +
			`el.dispatchEvent(new MouseEvent(e,{bubbles:true,cancelable:true,clientX:r.left+r.width/2,clientY:r.top+r.height/2}));` +
			`});})(` + jsString(a.Selector) + `)`
		return chromedp.Tasks{chromedp.Evaluate(js, nil)}, nil
	case "type":
		return chromedp.Tasks{chromedp.SendKeys(a.Selector, a.Text, chromedp.ByQuery)}, nil
	case "wait":
		return chromedp.Tasks{chromedp.WaitVisible(a.Selector, chromedp.ByQuery)}, nil
	case "sleep":
		d := time.Duration(a.SleepMs) * time.Millisecond
		if d == 0 {
			d = 200 * time.Millisecond
		}
		return chromedp.Tasks{chromedp.Sleep(d)}, nil
	}
	return nil, fmt.Errorf("unknown action %q", a.Action)
}

// jsString quotes a string for safe embedding in a JS evaluate
// expression. Mirrors strconv.Quote's escapes for the characters that
// matter in CSS selectors / DOM strings.
func jsString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`, "\r", `\r`)
	return "'" + r.Replace(s) + "'"
}

func getStr(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}
