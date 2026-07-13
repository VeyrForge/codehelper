//go:build rod

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// BrowserAvailable reports whether this build includes the browser tier.
func BrowserAvailable() bool { return true }

// EnsureBrowser downloads (once) and returns the path to the managed
// Chromium-for-Testing under BrowserDir. Idempotent and fast when already
// present — Get() only fetches when the revision is missing. The download is a
// self-contained Chromium that runs with a throwaway profile, so it never reads
// or writes any browser profile the user already has.
func EnsureBrowser(_ context.Context) (string, error) {
	dir, err := BrowserDir()
	if err != nil {
		return "", err
	}
	br := launcher.NewBrowser()
	br.RootDir = dir
	return br.Get()
}

// One persistent headless browser process is shared across captures — launching
// Chromium is the slow part, so amortize it. Pages are created and closed per
// capture for a clean console/event state.
var (
	browserOnce   sync.Once
	sharedBrowser *rod.Browser
	browserErr    error
)

func getBrowser(ctx context.Context) (*rod.Browser, error) {
	browserOnce.Do(func() {
		bin, err := EnsureBrowser(ctx)
		if err != nil {
			browserErr = fmt.Errorf("provision browser: %w", err)
			return
		}
		// Use rod's default ephemeral user-data-dir (a fresh temp profile per
		// launch, auto-cleaned) rather than a fixed path: it's still fully isolated
		// from the user's real browsers, and it avoids a stale SingletonLock from an
		// uncleanly-killed browser blocking the next launch. --no-sandbox keeps
		// headless reliable across desktop/CI/container (we only load local/dev URLs,
		// gated by GuardURL).
		ctrl, err := launcher.New().
			Bin(bin).
			Headless(true).
			Set("disable-gpu").
			Set("disable-dev-shm-usage").
			Set("no-sandbox").
			Launch()
		if err != nil {
			browserErr = fmt.Errorf("launch browser: %w", err)
			return
		}
		b := rod.New().ControlURL(ctrl)
		if err := b.Connect(); err != nil {
			browserErr = fmt.Errorf("connect browser: %w", err)
			return
		}
		sharedBrowser = b
	})
	return sharedBrowser, browserErr
}

// defaultHeadedSlowMo paces actions in a visible browser so a human can actually
// follow each click/keystroke. Only applied when Headed and no explicit SlowMoMS.
const defaultHeadedSlowMo = 650 * time.Millisecond

// launchHeaded starts a fresh VISIBLE browser for one capture (not the shared
// headless singleton) so the user can watch the agent drive the page. It is
// closed by the returned func after the capture. SlowMotion inserts a delay
// before every control action so clicks/inputs are perceptible; we deliberately
// do NOT enable rod's Trace(), which logs to stdout and would corrupt the MCP
// JSON-RPC stream — the in-page highlight (injectHighlighter) shows where each
// action lands instead. Headed needs a graphical display; the error is surfaced
// with a hint when there isn't one (SSH/CI → use headless).
func launchHeaded(ctx context.Context, slowMo time.Duration) (*rod.Browser, func(), error) {
	bin, err := EnsureBrowser(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("provision browser: %w", err)
	}
	ctrl, err := launcher.New().
		Bin(bin).
		Headless(false).
		Set("disable-dev-shm-usage").
		Set("no-sandbox").
		Launch()
	if err != nil {
		return nil, nil, fmt.Errorf("launch headed browser (needs a graphical display — over SSH/CI drop headed for headless): %w", err)
	}
	b := rod.New().ControlURL(ctrl)
	if slowMo > 0 {
		b = b.SlowMotion(slowMo)
	}
	if err := b.Connect(); err != nil {
		return nil, nil, fmt.Errorf("connect headed browser: %w", err)
	}
	return b, func() { _ = b.Close() }, nil
}

// CaptureBrowser navigates to opts.URL in headless Chromium and returns a
// screenshot plus the console output, uncaught page errors, failed requests, and
// page metadata observed during load.
func CaptureBrowser(ctx context.Context, opts BrowserOptions) (*BrowserResult, error) {
	timeout := time.Duration(opts.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var b *rod.Browser
	var err error
	if opts.Headed {
		slowMo := time.Duration(opts.SlowMoMS) * time.Millisecond
		if slowMo <= 0 {
			slowMo = defaultHeadedSlowMo
		}
		var closeFn func()
		b, closeFn, err = launchHeaded(ctx, slowMo)
		if err != nil {
			return nil, err
		}
		defer closeFn()
	} else {
		b, err = getBrowser(ctx)
		if err != nil {
			return nil, err
		}
	}
	page, err := b.Page(proto.TargetCreateTarget{})
	if err != nil {
		return nil, fmt.Errorf("open page: %w", err)
	}
	defer func() { _ = page.Close() }()
	page = page.Context(ctx)

	dev, ok := ResolveDevice(opts.Device)
	if !ok {
		return nil, fmt.Errorf("unknown device %q (use desktop|tablet|mobile)", opts.Device)
	}
	width, height := dev.Width, dev.Height
	if opts.Width > 0 {
		width = opts.Width
	}
	if opts.Height > 0 {
		height = opts.Height
	}
	_ = page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width: width, Height: height, DeviceScaleFactor: dev.Scale, Mobile: dev.Mobile,
	})
	if dev.UA != "" {
		_ = page.SetUserAgent(&proto.NetworkSetUserAgentOverride{UserAgent: dev.UA})
	}

	res := &BrowserResult{
		Device:   dev.Name,
		Viewport: fmt.Sprintf("%dx%d@%gx", width, height, dev.Scale),
		Format:   NormalizeFormat(opts.Format),
	}
	res.MIME = MIMEForFormat(res.Format)
	var mu sync.Mutex
	reqURL := map[proto.NetworkRequestID]string{} // requestID -> url, for failure mapping

	// Subscribe BEFORE navigating so early console/network events aren't missed.
	// EachEvent registers the handlers synchronously and returns a blocking wait
	// func; run that in the background and stop it by cancelling the page context.
	wait := page.EachEvent(
		func(e *proto.RuntimeConsoleAPICalled) {
			parts := make([]string, 0, len(e.Args))
			for _, a := range e.Args {
				parts = append(parts, remoteObjectString(a))
			}
			mu.Lock()
			res.Console = append(res.Console, ConsoleMessage{Level: string(e.Type), Text: strings.Join(parts, " ")})
			mu.Unlock()
		},
		func(e *proto.RuntimeExceptionThrown) {
			mu.Lock()
			res.PageErrors = append(res.PageErrors, exceptionString(e.ExceptionDetails))
			mu.Unlock()
		},
		func(e *proto.NetworkRequestWillBeSent) {
			mu.Lock()
			if e.Request != nil {
				reqURL[e.RequestID] = e.Request.URL
			}
			mu.Unlock()
		},
		func(e *proto.NetworkResponseReceived) {
			if e.Response == nil {
				return
			}
			mu.Lock()
			if e.Type == proto.NetworkResourceTypeDocument && res.DocStatus == 0 {
				res.DocStatus = e.Response.Status
			}
			if e.Response.Status >= 400 {
				res.Failed = append(res.Failed, FailedRequest{URL: e.Response.URL, Status: e.Response.Status})
			}
			mu.Unlock()
		},
		func(e *proto.NetworkLoadingFailed) {
			if e.Canceled {
				return
			}
			mu.Lock()
			res.Failed = append(res.Failed, FailedRequest{URL: reqURL[e.RequestID], Error: e.ErrorText})
			mu.Unlock()
		},
	)
	go wait()

	// Core Web Vitals (LCP/CLS) must be observed from the very start, so inject
	// the collector before navigation when auditing.
	if opts.Audit || opts.AuditFull {
		_, _ = page.EvalOnNewDocument(cwvCollectorJS)
	}
	// In a visible browser, inject the highlighter helper before navigation so it
	// survives across page loads and each action can flash a box on its target.
	if opts.Headed {
		_, _ = page.EvalOnNewDocument(highlightHelperJS)
	}

	start := time.Now()
	if err := page.Navigate(opts.URL); err != nil {
		return nil, fmt.Errorf("navigate: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		// Non-fatal: capture whatever rendered (the report will show the timeout).
		_ = err
	}

	if opts.WaitSelector != "" {
		if _, err := page.Element(opts.WaitSelector); err != nil {
			mu.Lock()
			res.PageErrors = append(res.PageErrors, fmt.Sprintf("wait_selector %q not found: %v", opts.WaitSelector, err))
			mu.Unlock()
		}
	} else {
		_ = page.WaitDOMStable(300*time.Millisecond, 0.2)
	}
	if opts.WaitMS > 0 {
		select {
		case <-time.After(time.Duration(opts.WaitMS) * time.Millisecond):
		case <-ctx.Done():
		}
	}
	res.LoadMS = time.Since(start).Milliseconds()

	// Interactions run BEFORE we read text / capture, so everything below reflects
	// the post-interaction state.
	if len(opts.Actions) > 0 {
		res.ActionLog = runActions(page, opts.Actions, opts, res, res.Format, dev.Scale)
	}

	// The final read+capture gets its own fresh deadline so a slow/failed wait
	// (which may have eaten the main timeout) can't leave us unable to screenshot
	// where the page got stuck — the most useful artifact in that case.
	capCtx, capCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer capCancel()
	cap := page.Context(capCtx)

	if info, err := cap.Info(); err == nil {
		res.FinalURL = info.URL
		res.Title = info.Title
	}
	if obj, err := cap.Eval(`() => document.body ? document.body.innerText : ""`); err == nil {
		res.Text = capRunes(obj.Value.Str(), 4000)
	}
	if opts.Metrics {
		res.Perf = collectPerf(cap)
	}
	if opts.Audit || opts.AuditFull {
		res.Vitals = collectVitals(cap)
		if opts.AuditFull {
			res.A11y = collectAxe(cap)
		} else {
			res.A11y = collectA11y(cap)
		}
	}
	res.Headed = opts.Headed
	if opts.Outline {
		res.Outline = collectOutline(cap)
	}

	// Screenshot last, so it reflects the settled page. Splitting a long full page
	// into vertical pieces keeps each piece at full resolution (a single very tall
	// screenshot gets downscaled to ~1568px by the vision API, losing detail).
	if pw, ph := pageContentSize(cap); pw > 0 {
		res.PageDim = fmt.Sprintf("%dx%d", pw, ph)
	}
	if opts.Selector == "" && opts.FullPage && opts.SegmentPx > 0 {
		tiles, err := captureSegments(cap, res.Format, opts, dev.Scale)
		if err != nil {
			return nil, fmt.Errorf("segmented screenshot: %w", err)
		}
		res.Tiles = tiles
		if len(tiles) > 0 {
			res.Image = tiles[0]
		}
	} else {
		img, err := captureScreenshot(cap, opts, res.Format, dev.Scale)
		if err != nil {
			return nil, fmt.Errorf("screenshot: %w", err)
		}
		res.Image = img
	}
	// Never return an image whose magic bytes disagree with its declared MIME —
	// that is exactly what 400'd Playwright-MCP requests.
	for _, b := range append([][]byte{res.Image}, res.Tiles...) {
		if !ImageMagicMatches(b, res.Format) {
			return nil, fmt.Errorf("screenshot bytes are not valid %s", res.Format)
		}
	}

	cancel()  // stop the event goroutine
	mu.Lock() // settle any in-flight handler before returning the slices
	defer mu.Unlock()
	return res, nil
}

// maxActionPreviews caps step screenshots so a long flow cannot flood the response.
const maxActionPreviews = 16

// runActions executes interaction steps in order, stopping at the first failure
// (so the post-capture screenshot shows exactly where a flow got stuck). It
// returns a one-line log per step for the report. When opts.PreviewActions is
// true, a viewport screenshot is captured after each step (including the failing
// one) so the agent can see clicks, fills, and typing as they happen. When
// opts.Headed is set, each step also flashes a labelled box on its target element
// so a human watching the visible browser sees exactly where the action lands.
func runActions(page *rod.Page, actions []Action, opts BrowserOptions, res *BrowserResult, format string, scale float64) []string {
	log := make([]string, 0, len(actions))
	headed := opts.Headed
	preview := opts.PreviewActions
	previewOpts := opts
	previewOpts.FullPage = false
	previewOpts.Selector = ""
	previewOpts.ClipY = 0
	previewOpts.ClipHeight = 0

	for i, a := range actions {
		// In a visible browser, flash a labelled box on the target BEFORE acting so
		// the user sees exactly where the click/input lands. SlowMotion then paces
		// the real action a beat later. No-op (and cheap) in headless.
		if headed && a.Selector != "" {
			_, _ = page.Eval(`(sel, label) => window.__chHi && window.__chHi(sel, label)`, a.Selector, actionLabel(a))
		}
		if err := runAction(page, a); err != nil {
			log = append(log, fmt.Sprintf("step %d %s — FAILED: %v", i+1, actionLabel(a), err))
			if preview && len(res.ActionPreviews) < maxActionPreviews {
				appendActionPreview(page, res, i+1, actionLabel(a)+" — FAILED", previewOpts, format, scale)
			}
			break
		}
		log = append(log, fmt.Sprintf("step %d %s — ok", i+1, actionLabel(a)))
		if preview && len(res.ActionPreviews) < maxActionPreviews {
			appendActionPreview(page, res, i+1, actionLabel(a), previewOpts, format, scale)
		}
	}
	return log
}

func appendActionPreview(page *rod.Page, res *BrowserResult, step int, label string, opts BrowserOptions, format string, scale float64) {
	_ = page.WaitDOMStable(120*time.Millisecond, 0.2)
	img, err := captureScreenshot(page, opts, format, scale)
	if err != nil || len(img) == 0 || !ImageMagicMatches(img, format) {
		return
	}
	res.ActionPreviews = append(res.ActionPreviews, ActionPreview{Step: step, Label: label, Image: img})
}

// actionElemTimeout bounds how long a single action waits for its element, so a
// bad selector fails that step fast instead of consuming the whole capture
// budget (which would also starve the final screenshot).
const actionElemTimeout = 10 * time.Second

func runAction(page *rod.Page, a Action) error {
	// elem looks up the action's selector with a bounded per-step timeout.
	elem := func() (*rod.Element, error) {
		d := actionElemTimeout
		if a.MS > 0 {
			d = time.Duration(a.MS) * time.Millisecond
		}
		return page.Timeout(d).Element(a.Selector)
	}
	switch strings.ToLower(strings.TrimSpace(a.Do)) {
	case "click":
		el, err := elem()
		if err != nil {
			return err
		}
		return el.Click(proto.InputMouseButtonLeft, 1)
	case "type":
		el, err := elem()
		if err != nil {
			return err
		}
		return el.Input(a.Text)
	case "fill":
		el, err := elem()
		if err != nil {
			return err
		}
		_ = el.SelectAllText() // replace existing value rather than append
		return el.Input(a.Text)
	case "hover":
		el, err := elem()
		if err != nil {
			return err
		}
		return el.Hover()
	case "press":
		k, ok := keyByName(a.Key)
		if !ok {
			return fmt.Errorf("unknown key %q", a.Key)
		}
		return page.Keyboard.Press(k)
	case "scroll":
		if a.Selector != "" {
			el, err := elem()
			if err != nil {
				return err
			}
			return el.ScrollIntoView()
		}
		return page.Mouse.Scroll(0, float64(a.Y), 1)
	case "wait":
		if a.Selector != "" {
			_, err := elem()
			return err
		}
		ms := a.MS
		if ms <= 0 {
			ms = 500
		}
		time.Sleep(time.Duration(ms) * time.Millisecond)
		return nil
	case "assert":
		// The element must exist; if Text is given, its text must contain it.
		el, err := elem()
		if err != nil {
			return fmt.Errorf("element not found")
		}
		if a.Text == "" {
			return nil
		}
		got, err := el.Text()
		if err != nil {
			return err
		}
		if !strings.Contains(got, a.Text) {
			return fmt.Errorf("text %q not found (got %q)", a.Text, capRunes(strings.Join(strings.Fields(got), " "), 80))
		}
		return nil
	default:
		return fmt.Errorf("unknown action %q", a.Do)
	}
}

func actionLabel(a Action) string {
	switch strings.ToLower(a.Do) {
	case "type", "fill":
		return fmt.Sprintf("%s %q=%q", a.Do, a.Selector, a.Text)
	case "press":
		return fmt.Sprintf("press %s", a.Key)
	case "scroll":
		if a.Selector != "" {
			return "scroll to " + a.Selector
		}
		return fmt.Sprintf("scroll y=%d", a.Y)
	case "wait":
		if a.Selector != "" {
			return "wait for " + a.Selector
		}
		return fmt.Sprintf("wait %dms", a.MS)
	case "assert":
		if a.Text != "" {
			return fmt.Sprintf("assert %q contains %q", a.Selector, a.Text)
		}
		return fmt.Sprintf("assert %q exists", a.Selector)
	default:
		return fmt.Sprintf("%s %s", a.Do, a.Selector)
	}
}

var keyNames = map[string]input.Key{
	"enter": input.Enter, "return": input.Enter, "tab": input.Tab,
	"escape": input.Escape, "esc": input.Escape, "backspace": input.Backspace,
	"delete": input.Delete, "space": input.Space,
	"arrowdown": input.ArrowDown, "arrowup": input.ArrowUp,
	"arrowleft": input.ArrowLeft, "arrowright": input.ArrowRight,
}

func keyByName(name string) (input.Key, bool) {
	k, ok := keyNames[strings.ToLower(strings.TrimSpace(name))]
	return k, ok
}

// cwvCollectorJS is injected before navigation so the PerformanceObservers see
// the whole load. It accumulates the largest contentful paint and the
// cumulative layout shift into window.__cwv for collectVitals to read later.
const cwvCollectorJS = `
window.__cwv = {lcp:0, cls:0};
try {
  new PerformanceObserver(function(l){
    var es = l.getEntries(); var e = es[es.length-1];
    if (e) window.__cwv.lcp = Math.round(e.renderTime || e.startTime || 0);
  }).observe({type:'largest-contentful-paint', buffered:true});
  new PerformanceObserver(function(l){
    l.getEntries().forEach(function(e){ if(!e.hadRecentInput) window.__cwv.cls += e.value; });
  }).observe({type:'layout-shift', buffered:true});
} catch(e) {}
`

// highlightHelperJS defines window.__chHi(selector, label): before an action in
// a visible browser it draws a red box exactly over the target element plus a
// small label ("click", `fill "…"`, etc.), auto-removed after a short dwell. It
// is injected via EvalOnNewDocument so it persists across navigations. Purely
// in-page (no stdout), so it never touches the MCP protocol stream.
const highlightHelperJS = `
window.__chHi = function(sel, label){
  try {
    var el = document.querySelector(sel);
    if (!el) return;
    var r = el.getBoundingClientRect();
    var box = document.createElement('div');
    box.style.cssText = 'position:fixed;z-index:2147483647;pointer-events:none;'
      + 'border:3px solid #ff2d55;border-radius:4px;box-shadow:0 0 0 3px rgba(255,45,85,.25);'
      + 'transition:opacity .2s;left:'+(r.left-3)+'px;top:'+(r.top-3)+'px;'
      + 'width:'+r.width+'px;height:'+r.height+'px;';
    var tag = document.createElement('div');
    tag.textContent = label || '';
    tag.style.cssText = 'position:fixed;z-index:2147483647;pointer-events:none;'
      + 'background:#ff2d55;color:#fff;font:12px/1.5 ui-monospace,monospace;'
      + 'padding:1px 6px;border-radius:3px;white-space:nowrap;'
      + 'left:'+(r.left-3)+'px;top:'+(Math.max(0,r.top-24))+'px;';
    document.body.appendChild(box);
    document.body.appendChild(tag);
    setTimeout(function(){ box.remove(); tag.remove(); }, 1400);
  } catch(e){}
};
`

// outlineJS walks the page for interactive elements (links, buttons, form
// controls, ARIA/clickable widgets), keeps only the visible ones, and returns a
// compact record per element: a stable, ready-to-use CSS selector plus role,
// accessible name, input type, placeholder and current value. Capped so a huge
// page can't flood context — the bounded, opt-in counterpart to a full DOM dump.
const outlineJS = `() => {
  const uniq = (s) => { try { return document.querySelectorAll(s).length === 1; } catch(e){ return false; } };
  const esc = (v) => (window.CSS && CSS.escape) ? CSS.escape(v) : String(v).replace(/["\\]/g,'\\$&');
  const selectorFor = (el) => {
    if (el.id && uniq('#'+esc(el.id))) return '#'+esc(el.id);
    for (const a of ['data-testid','data-test','name','aria-label']) {
      const v = el.getAttribute(a);
      if (v) { const s = el.tagName.toLowerCase()+'['+a+'="'+v.replace(/"/g,'\\"')+'"]'; if (uniq(s)) return s; }
    }
    const parts = [];
    let n = el;
    while (n && n.nodeType === 1 && parts.length < 5) {
      if (n.id) { parts.unshift('#'+esc(n.id)); break; }
      let part = n.tagName.toLowerCase();
      const p = n.parentElement;
      if (p) {
        const same = [].slice.call(p.children).filter(c => c.tagName === n.tagName);
        if (same.length > 1) part += ':nth-of-type('+(same.indexOf(n)+1)+')';
      }
      parts.unshift(part);
      n = n.parentElement;
    }
    return parts.join(' > ');
  };
  const roleFor = (el) => {
    const explicit = el.getAttribute('role'); if (explicit) return explicit;
    const t = el.tagName.toLowerCase();
    if (t === 'a') return 'link';
    if (t === 'button') return 'button';
    if (t === 'select') return 'select';
    if (t === 'textarea') return 'textbox';
    if (t === 'input') {
      const it = (el.getAttribute('type')||'text').toLowerCase();
      if (['button','submit','reset','image'].indexOf(it) >= 0) return 'button';
      if (it === 'checkbox') return 'checkbox';
      if (it === 'radio') return 'radio';
      return 'textbox';
    }
    if (el.isContentEditable) return 'textbox';
    return 'clickable';
  };
  const nameFor = (el) => {
    const aria = el.getAttribute('aria-label'); if (aria) return aria.trim();
    if (el.id) { const l = document.querySelector('label[for="'+esc(el.id)+'"]'); if (l && l.textContent.trim()) return l.textContent.trim(); }
    const lab = el.closest && el.closest('label'); if (lab && lab.textContent.trim()) return lab.textContent.trim();
    const txt = (el.textContent||'').trim();
    if (txt) return txt;
    const ph = el.getAttribute('placeholder'); if (ph) return ph.trim();
    return (el.getAttribute('title')||'').trim();
  };
  const sel = 'a[href], button, input:not([type=hidden]), select, textarea, '
    + '[role=button], [role=link], [role=checkbox], [role=radio], [role=tab], [role=menuitem], '
    + '[role=switch], [onclick], [contenteditable=""], [contenteditable=true]';
  const seen = {};
  const out = [];
  const els = document.querySelectorAll(sel);
  for (let i = 0; i < els.length && out.length < 100; i++) {
    const el = els[i];
    const r = el.getBoundingClientRect();
    const cs = getComputedStyle(el);
    if (r.width <= 0 || r.height <= 0 || cs.visibility === 'hidden' || cs.display === 'none') continue;
    const s = selectorFor(el);
    if (!s || seen[s]) continue;
    seen[s] = 1;
    out.push({
      selector: s,
      role: roleFor(el),
      name: (nameFor(el)||'').replace(/\s+/g,' ').slice(0,80),
      type: (el.getAttribute('type')||''),
      placeholder: (el.getAttribute('placeholder')||''),
      value: (el.value||'').slice(0,60),
    });
  }
  return JSON.stringify(out);
}`

// collectOutline runs outlineJS and decodes the interactive-elements map.
func collectOutline(page *rod.Page) []OutlineElement {
	obj, err := page.Eval(outlineJS)
	if err != nil {
		return nil
	}
	var els []OutlineElement
	if err := json.Unmarshal([]byte(obj.Value.Str()), &els); err != nil {
		return nil
	}
	return els
}

// collectVitals reads the observed LCP/CLS plus FCP/TTFB from timing.
func collectVitals(page *rod.Page) *Vitals {
	// Return a JSON string (not an object) so we parse it deterministically rather
	// than relying on the remote-object serialization.
	const js = `() => {
		const nav = performance.getEntriesByType('navigation')[0] || {};
		const paint = performance.getEntriesByType('paint') || [];
		const fcp = (paint.find(p => p.name === 'first-contentful-paint') || {}).startTime || 0;
		const cwv = window.__cwv || {lcp:0, cls:0};
		return JSON.stringify({
			lcp: Math.round(cwv.lcp || 0),
			cls: Math.round((cwv.cls || 0) * 1000) / 1000,
			fcp: Math.round(fcp),
			ttfb: Math.round(nav.responseStart || 0),
		});
	}`
	obj, err := page.Eval(js)
	if err != nil {
		return nil
	}
	var v struct {
		LCP  int     `json:"lcp"`
		CLS  float64 `json:"cls"`
		FCP  int     `json:"fcp"`
		TTFB int     `json:"ttfb"`
	}
	if err := json.Unmarshal([]byte(obj.Value.Str()), &v); err != nil {
		return nil
	}
	return &Vitals{LCPms: v.LCP, CLS: v.CLS, FCPms: v.FCP, TTFBms: v.TTFB}
}

// collectA11y runs the lightweight accessibility checks in-page. These are the
// common, high-confidence rules (missing alt/labels/accessible names, page lang
// and title) — deliberately not a full axe-core bundle, to stay lean.
func collectA11y(page *rod.Page) []A11yIssue {
	const js = `() => {
		const issues = [];
		const add = (rule, els) => { if (els.length) issues.push({rule, count: els.length, sample: (els[0].outerHTML||'').replace(/\s+/g,' ').slice(0,100)}); };
		add('image-missing-alt', [...document.querySelectorAll('img:not([alt])')]);
		add('input-missing-label', [...document.querySelectorAll('input:not([type=hidden]):not([aria-label]):not([aria-labelledby]):not([title])')]
			.filter(i => !(i.id && document.querySelector('label[for="'+CSS.escape(i.id)+'"]'))));
		add('control-no-accessible-name', [...document.querySelectorAll('button, a[href]')]
			.filter(b => !(b.textContent||'').trim() && !b.getAttribute('aria-label') && !b.querySelector('img[alt]:not([alt=""])')));
		if (!document.documentElement.getAttribute('lang')) issues.push({rule:'html-missing-lang', count:1, sample:'<html>'});
		if (!(document.title||'').trim()) issues.push({rule:'missing-title', count:1, sample:''});
		return JSON.stringify({issues});
	}`
	obj, err := page.Eval(js)
	if err != nil {
		return nil
	}
	var r struct {
		Issues []A11yIssue `json:"issues"`
	}
	if err := json.Unmarshal([]byte(obj.Value.Str()), &r); err != nil {
		return nil
	}
	return r.Issues
}

// collectAxe runs the full axe-core engine: inject the cached bundle as a script
// tag (so it attaches to window in page scope), then await axe.run and summarize
// each violation to {rule,impact,count,help,sample} — kept lean (no per-node DOM
// dump). Falls back to a single explanatory issue when axe isn't provisioned.
func collectAxe(page *rod.Page) []A11yIssue {
	p, err := AxePath()
	if err != nil {
		return nil
	}
	src, err := os.ReadFile(p)
	if err != nil || len(src) == 0 {
		return []A11yIssue{{Rule: "axe-core-not-installed", Sample: "run `ch browser install` to fetch axe-core for audit=full"}}
	}
	if err := page.AddScriptTag("", string(src)); err != nil {
		return []A11yIssue{{Rule: "axe-inject-failed", Sample: err.Error()}}
	}
	const js = `() => axe.run(document, {resultTypes:['violations']}).then(function(r){
		return JSON.stringify((r.violations||[]).map(function(v){
			return {
				rule: v.id,
				impact: v.impact || '',
				count: (v.nodes||[]).length,
				help: v.help || '',
				sample: (v.nodes && v.nodes[0] && v.nodes[0].target) ? String(v.nodes[0].target.join(' ')) : ''
			};
		}));
	})`
	obj, err := page.Evaluate(rod.Eval(js).ByPromise())
	if err != nil {
		return []A11yIssue{{Rule: "axe-run-failed", Sample: err.Error()}}
	}
	var issues []A11yIssue
	if err := json.Unmarshal([]byte(obj.Value.Str()), &issues); err != nil {
		return nil
	}
	return issues
}

// maxSegments caps how many pieces a split capture returns, so a pathologically
// long page can't flood the response. Truncation is surfaced in the report.
const maxSegments = 24

func captureScreenshot(page *rod.Page, opts BrowserOptions, format string, scale float64) ([]byte, error) {
	pf := protoFormat(format)
	quality := opts.Quality
	if quality <= 0 {
		quality = 80
	}
	if opts.Selector != "" {
		el, err := page.Element(opts.Selector)
		if err != nil {
			return nil, fmt.Errorf("selector %q: %w", opts.Selector, err)
		}
		return el.Screenshot(pf, quality)
	}
	req := &proto.PageCaptureScreenshot{Format: pf}
	if format != FormatPNG { // PNG ignores quality; webp/jpeg honor it
		req.Quality = &quality
	}
	// Specific-height capture: clip to [ClipY, ClipY+ClipHeight) at full width.
	if opts.ClipHeight > 0 {
		w, _ := pageContentSize(page)
		if w == 0 {
			w = 1280
		}
		req.Clip = &proto.PageViewport{X: 0, Y: float64(opts.ClipY), Width: float64(w), Height: float64(opts.ClipHeight), Scale: scale}
		req.CaptureBeyondViewport = true
		return page.Screenshot(false, req)
	}
	return page.Screenshot(opts.FullPage, req)
}

// captureSegments slices a full page into vertical pieces of at most SegmentPx
// CSS px each, capturing every piece via a clip beyond the viewport so off-screen
// content renders at native resolution.
func captureSegments(page *rod.Page, format string, opts BrowserOptions, scale float64) ([][]byte, error) {
	w, h := pageContentSize(page)
	if w == 0 || h == 0 {
		return nil, fmt.Errorf("could not measure page size")
	}
	segH := opts.SegmentPx
	if segH < 200 {
		segH = 200 // guard against absurdly small segments
	}
	pf := protoFormat(format)
	quality := opts.Quality
	if quality <= 0 {
		quality = 80
	}
	var tiles [][]byte
	for y := 0; y < h && len(tiles) < maxSegments; y += segH {
		hh := segH
		if y+hh > h {
			hh = h - y
		}
		req := &proto.PageCaptureScreenshot{
			Format:                pf,
			Clip:                  &proto.PageViewport{X: 0, Y: float64(y), Width: float64(w), Height: float64(hh), Scale: scale},
			CaptureBeyondViewport: true,
		}
		if format != FormatPNG {
			req.Quality = &quality
		}
		b, err := page.Screenshot(false, req)
		if err != nil {
			return nil, err
		}
		tiles = append(tiles, b)
	}
	return tiles, nil
}

// pageContentSize returns the full scrollable content size in CSS px.
func pageContentSize(page *rod.Page) (int, int) {
	obj, err := page.Eval(`() => JSON.stringify({
		w: Math.max(document.documentElement.scrollWidth, document.body ? document.body.scrollWidth : 0),
		h: Math.max(document.documentElement.scrollHeight, document.body ? document.body.scrollHeight : 0)
	})`)
	if err != nil {
		return 0, 0
	}
	var d struct{ W, H int }
	if err := json.Unmarshal([]byte(obj.Value.Str()), &d); err != nil {
		return 0, 0
	}
	return d.W, d.H
}

func protoFormat(format string) proto.PageCaptureScreenshotFormat {
	switch format {
	case FormatPNG:
		return proto.PageCaptureScreenshotFormatPng
	case FormatJPEG:
		return proto.PageCaptureScreenshotFormatJpeg
	default:
		return proto.PageCaptureScreenshotFormatWebp
	}
}

// collectPerf reads paint/navigation timing and page weight from the Performance
// API in one eval — cheap, and it's the data a "is this page slow?" check wants
// without injecting observers or pulling the whole resource list into context.
func collectPerf(page *rod.Page) *PerfMetrics {
	const js = `() => {
		const nav = performance.getEntriesByType('navigation')[0] || {};
		const paint = performance.getEntriesByType('paint') || [];
		const fcp = (paint.find(p => p.name === 'first-contentful-paint') || {}).startTime || 0;
		const res = performance.getEntriesByType('resource') || [];
		let bytes = nav.transferSize || 0;
		for (const r of res) bytes += (r.transferSize || 0);
		return {
			fcp: Math.round(fcp),
			dcl: Math.round(nav.domContentLoadedEventEnd || 0),
			load: Math.round(nav.loadEventEnd || 0),
			requests: res.length + 1,
			transferKB: Math.round(bytes / 1024),
			heapMB: Math.round(((performance.memory || {}).usedJSHeapSize || 0) / 1048576),
		};
	}`
	obj, err := page.Eval(js)
	if err != nil {
		return nil
	}
	v := obj.Value
	return &PerfMetrics{
		FCPms:            v.Get("fcp").Int(),
		DOMContentLoaded: v.Get("dcl").Int(),
		LoadMs:           v.Get("load").Int(),
		Requests:         v.Get("requests").Int(),
		TransferKB:       v.Get("transferKB").Int(),
		JSHeapMB:         v.Get("heapMB").Int(),
	}
}

// remoteObjectString renders one console argument compactly: the unquoted string
// for string args, the engine's description for objects/errors, else the JSON.
func remoteObjectString(o *proto.RuntimeRemoteObject) string {
	if o == nil {
		return ""
	}
	if o.Type == proto.RuntimeRemoteObjectTypeString {
		return o.Value.Str()
	}
	if o.Description != "" {
		return o.Description
	}
	if s := o.Value.String(); s != "" && s != "null" {
		return s
	}
	return string(o.Type)
}

func exceptionString(d *proto.RuntimeExceptionDetails) string {
	if d == nil {
		return "uncaught exception"
	}
	if d.Exception != nil && d.Exception.Description != "" {
		return d.Exception.Description
	}
	if d.Text != "" {
		return d.Text
	}
	return "uncaught exception"
}

func capRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
