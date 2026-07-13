//go:build rod

package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const testPage = `<!doctype html><html><head><title>cap test</title></head>
<body><h1 id="h">hello</h1>
<script>
  console.log("log line");
  console.error("an error");
  fetch("/missing").catch(()=>{});
  setTimeout(function(){ throw new Error("boom"); }, 10);
</script></body></html>`

// requireBrowser provisions the managed Chromium or skips — keeps the suite green
// on machines/CI without the one-time download.
func requireBrowser(t *testing.T) {
	t.Helper()
	if os.Getenv("CODEHELPER_SKIP_BROWSER_TEST") != "" {
		t.Skip("CODEHELPER_SKIP_BROWSER_TEST set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := EnsureBrowser(ctx); err != nil {
		t.Skipf("managed browser unavailable (run `ch browser install`): %v", err)
	}
}

func TestCaptureBrowserEndToEnd(t *testing.T) {
	requireBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(testPage))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := CaptureBrowser(ctx, BrowserOptions{URL: srv.URL, Device: "desktop", Metrics: true, WaitMS: 200})
	if err != nil {
		t.Fatalf("CaptureBrowser: %v", err)
	}

	// Screenshot is real WebP whose magic matches the declared MIME.
	if res.Format != FormatWebP || res.MIME != "image/webp" {
		t.Errorf("format=%q mime=%q, want webp/image/webp", res.Format, res.MIME)
	}
	if !ImageMagicMatches(res.Image, FormatWebP) {
		t.Errorf("screenshot is not valid webp (%d bytes)", len(res.Image))
	}
	if res.DocStatus != 200 {
		t.Errorf("doc_status=%d, want 200", res.DocStatus)
	}
	if res.Viewport != "1280x800@1x" {
		t.Errorf("viewport=%q, want 1280x800@1x", res.Viewport)
	}
	// Diagnostics captured: console, the uncaught error, and the 404 fetch.
	if len(res.Console) == 0 {
		t.Error("expected console messages")
	}
	if len(res.PageErrors) == 0 {
		t.Error("expected an uncaught page error")
	}
	if len(res.Failed) == 0 {
		t.Error("expected a failed request for /missing")
	}
	if res.Perf == nil || res.Perf.LoadMs <= 0 {
		t.Errorf("expected perf with load>0, got %+v", res.Perf)
	}
	if !strings.Contains(res.Text, "hello") {
		t.Errorf("expected visible text to contain 'hello', got %q", res.Text)
	}
}

const formPage = `<!doctype html><html><body>
<input id="name"><button id="go">go</button><div id="out"></div>
<script>
  document.getElementById("go").onclick = function(){
    document.getElementById("out").textContent = "submitted:" + document.getElementById("name").value;
  };
</script></body></html>`

func TestCaptureBrowserActions(t *testing.T) {
	requireBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(formPage))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := CaptureBrowser(ctx, BrowserOptions{
		URL: srv.URL,
		Actions: []Action{
			{Do: "fill", Selector: "#name", Text: "codehelper"},
			{Do: "click", Selector: "#go"},
			{Do: "wait", Selector: "#out"},
		},
	})
	if err != nil {
		t.Fatalf("CaptureBrowser actions: %v", err)
	}
	if len(res.ActionLog) != 3 {
		t.Fatalf("want 3 action log lines, got %d: %v", len(res.ActionLog), res.ActionLog)
	}
	for _, l := range res.ActionLog {
		if strings.Contains(l, "FAILED") {
			t.Fatalf("action failed: %v", res.ActionLog)
		}
	}
	// The flow actually ran: the button handler wrote the filled value.
	if !strings.Contains(res.Text, "submitted:codehelper") {
		t.Errorf("interaction did not take effect; page text = %q", res.Text)
	}
}

func TestCaptureBrowserActionPreviews(t *testing.T) {
	requireBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(formPage))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := CaptureBrowser(ctx, BrowserOptions{
		URL: srv.URL,
		Actions: []Action{
			{Do: "fill", Selector: "#name", Text: "codehelper"},
			{Do: "click", Selector: "#go"},
		},
		PreviewActions: true,
	})
	if err != nil {
		t.Fatalf("CaptureBrowser action previews: %v", err)
	}
	if len(res.ActionPreviews) != 2 {
		t.Fatalf("want 2 action previews, got %d", len(res.ActionPreviews))
	}
	for i, p := range res.ActionPreviews {
		if p.Step != i+1 || p.Label == "" {
			t.Errorf("preview %d: step=%d label=%q", i, p.Step, p.Label)
		}
		if len(p.Image) == 0 || !ImageMagicMatches(p.Image, res.Format) {
			t.Errorf("preview %d: invalid image bytes", i)
		}
	}
	if len(res.Image) == 0 {
		t.Error("expected final screenshot")
	}
}

func TestCaptureBrowserActionFailureStops(t *testing.T) {
	requireBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(formPage))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := CaptureBrowser(ctx, BrowserOptions{
		URL: srv.URL,
		Actions: []Action{
			{Do: "click", Selector: "#go"},
			{Do: "wait", Selector: "#does-not-exist", MS: 0},
		},
		TimeoutSec: 8,
	})
	// A missing wait-selector should be reported as a FAILED step (not crash the
	// capture), and we should still get a screenshot back.
	if err != nil {
		t.Fatalf("capture should still succeed with a failed action: %v", err)
	}
	if len(res.ActionLog) == 0 || !strings.Contains(res.ActionLog[len(res.ActionLog)-1], "FAILED") {
		t.Errorf("expected last action to be FAILED, got %v", res.ActionLog)
	}
	if len(res.Image) == 0 {
		t.Error("expected a screenshot even after a failed action")
	}
}

func TestVisualDiffEndToEnd(t *testing.T) {
	requireBrowser(t)
	// Same server, two pages whose background changes with ?c=
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := r.URL.Query().Get("c")
		if c == "" {
			c = "white"
		}
		fmt.Fprintf(w, `<!doctype html><body style="margin:0;background:%s;height:100vh"></body>`, c)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	v1, err := CaptureBrowser(ctx, BrowserOptions{URL: srv.URL + "/?c=white", Format: FormatPNG})
	if err != nil {
		t.Fatalf("capture v1: %v", err)
	}
	v2, err := CaptureBrowser(ctx, BrowserOptions{URL: srv.URL + "/?c=black", Format: FormatPNG})
	if err != nil {
		t.Fatalf("capture v2: %v", err)
	}

	same, err := DiffImages(v1.Image, v1.Image)
	if err != nil || same.ChangedPct != 0 {
		t.Fatalf("identical capture should be 0%% changed, got %.2f%% (err %v)", same.ChangedPct, err)
	}
	changed, err := DiffImages(v1.Image, v2.Image)
	if err != nil {
		t.Fatal(err)
	}
	if changed.ChangedPct < 90 { // white→black full-page repaint
		t.Errorf("white→black should be a large change, got %.2f%%", changed.ChangedPct)
	}
	if len(changed.DiffPNG) == 0 || !ImageMagicMatches(changed.DiffPNG, FormatPNG) {
		t.Error("diff image should be valid PNG")
	}
}

const a11yBadPage = `<!doctype html><html><body>
<img src="x.png">
<input type="text">
<button></button>
<h1>content</h1>
</body></html>` // no lang, no title, img w/o alt, unlabeled input, empty button

func TestAuditEndToEnd(t *testing.T) {
	requireBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(a11yBadPage))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := CaptureBrowser(ctx, BrowserOptions{URL: srv.URL, Audit: true, WaitMS: 300})
	if err != nil {
		t.Fatalf("audit capture: %v", err)
	}

	// Accessibility: the known issues are detected.
	rules := map[string]int{}
	for _, a := range res.A11y {
		rules[a.Rule] = a.Count
	}
	for _, want := range []string{"image-missing-alt", "input-missing-label", "control-no-accessible-name", "html-missing-lang", "missing-title"} {
		if rules[want] == 0 {
			t.Errorf("expected a11y rule %q to fire; got rules %v", want, rules)
		}
	}

	// Core Web Vitals: present and sane (FCP should be > 0 on a rendered page).
	if res.Vitals == nil {
		t.Fatal("expected vitals")
	}
	if res.Vitals.FCPms <= 0 {
		t.Errorf("expected FCP > 0, got %d", res.Vitals.FCPms)
	}
}

func TestAuditFullAxe(t *testing.T) {
	requireBrowser(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, err := EnsureAxe(ctx); err != nil {
		t.Skipf("axe-core unavailable (run `ch browser install`): %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(a11yBadPage))
	}))
	defer srv.Close()

	res, err := CaptureBrowser(ctx, BrowserOptions{URL: srv.URL, AuditFull: true, WaitMS: 300})
	if err != nil {
		t.Fatalf("full audit: %v", err)
	}
	if len(res.A11y) == 0 {
		t.Fatal("axe-core should report violations on a bad page")
	}
	// axe rules use ids like image-alt/document-title and carry an impact level.
	hasImpact := false
	rules := map[string]bool{}
	for _, a := range res.A11y {
		rules[a.Rule] = true
		if a.Impact != "" {
			hasImpact = true
		}
	}
	if !hasImpact {
		t.Error("axe results should carry impact levels")
	}
	if !rules["image-alt"] && !rules["document-title"] {
		t.Errorf("expected axe rule ids (image-alt/document-title); got %v", rules)
	}
}

// tallPage renders a page much taller than the viewport with numbered bands.
const tallPage = `<!doctype html><html><body style="margin:0">` +
	`<div style="height:6000px;background:linear-gradient(#fff,#000)">` +
	`<h1 id="top">TOP</h1><h1 style="margin-top:5800px" id="bot">BOTTOM</h1></div></body></html>`

func TestFullPageSplit(t *testing.T) {
	requireBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(tallPage))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := CaptureBrowser(ctx, BrowserOptions{URL: srv.URL, FullPage: true, SegmentPx: 2000})
	if err != nil {
		t.Fatalf("split capture: %v", err)
	}
	// ~6000px page / 2000px segments => ~3 pieces, each valid.
	if len(res.Tiles) < 3 {
		t.Fatalf("expected >=3 tiles for a 6000px page, got %d (page %s)", len(res.Tiles), res.PageDim)
	}
	for i, tile := range res.Tiles {
		if !ImageMagicMatches(tile, res.Format) {
			t.Errorf("tile %d is not valid %s", i, res.Format)
		}
	}
	if len(res.Image) == 0 || string(res.Image) != string(res.Tiles[0]) {
		t.Error("Image should equal the first tile")
	}
}

func TestClipRegion(t *testing.T) {
	requireBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(tallPage))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := CaptureBrowser(ctx, BrowserOptions{URL: srv.URL, Format: FormatPNG, ClipY: 1000, ClipHeight: 500})
	if err != nil {
		t.Fatalf("clip capture: %v", err)
	}
	if len(res.Tiles) != 0 {
		t.Error("clip should not split into tiles")
	}
	if !ImageMagicMatches(res.Image, FormatPNG) {
		t.Error("clip image should be valid PNG")
	}
}

func TestAssertPassAndFail(t *testing.T) {
	requireBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(formPage))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// PASS: fill → click → assert the result text appeared.
	pass, err := CaptureBrowser(ctx, BrowserOptions{URL: srv.URL, Actions: []Action{
		{Do: "fill", Selector: "#name", Text: "codehelper"},
		{Do: "click", Selector: "#go"},
		{Do: "assert", Selector: "#out", Text: "submitted:codehelper"},
	}})
	if err != nil {
		t.Fatalf("pass flow: %v", err)
	}
	for _, l := range pass.ActionLog {
		if strings.Contains(l, "FAILED") {
			t.Fatalf("expected all steps to pass, got %v", pass.ActionLog)
		}
	}

	// FAIL: assert the wrong text → last step FAILED.
	fail, err := CaptureBrowser(ctx, BrowserOptions{URL: srv.URL, Actions: []Action{
		{Do: "click", Selector: "#go"},
		{Do: "assert", Selector: "#out", Text: "this will not be there"},
	}})
	if err != nil {
		t.Fatalf("fail flow capture: %v", err)
	}
	last := fail.ActionLog[len(fail.ActionLog)-1]
	if !strings.Contains(last, "FAILED") {
		t.Errorf("expected assert to FAIL, got %v", fail.ActionLog)
	}
}

func TestCaptureBrowserMobileViewportAndPNG(t *testing.T) {
	requireBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(testPage))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := CaptureBrowser(ctx, BrowserOptions{URL: srv.URL, Device: "mobile", Format: FormatPNG})
	if err != nil {
		t.Fatalf("CaptureBrowser mobile: %v", err)
	}
	if res.Device != "mobile" || res.Viewport != "390x844@2x" {
		t.Errorf("device=%q viewport=%q, want mobile/390x844@2x", res.Device, res.Viewport)
	}
	if !ImageMagicMatches(res.Image, FormatPNG) {
		t.Error("expected valid PNG bytes when format=png")
	}
}
