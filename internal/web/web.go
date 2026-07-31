// Package web is a lightweight, HTTP-only web verification tool — codehelper's
// optimized take on the Playwright use case. The optimization insight is that
// most web verification (API responses, SSR pages, health checks, content
// assertions) does not need a real browser: a fast HTTP request plus structured
// extraction and assertions covers it in milliseconds, with no Chromium, no node
// runtime, and no JS engine.
//
// It does not render client-side JavaScript; for SPA-only behavior a real
// browser is still required (a tier intentionally left out to stay lean).
package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/docs"
	"github.com/VeyrForge/codehelper/internal/netguard"
)

// Check describes a single web verification request and its assertions.
type Check struct {
	URL            string            `json:"url"`
	Method         string            `json:"method,omitempty"` // default GET
	Headers        map[string]string `json:"headers,omitempty"`
	Body           string            `json:"body,omitempty"`
	TimeoutSec     int               `json:"timeout_sec,omitempty"`
	FollowRedirect bool              `json:"follow_redirect,omitempty"`
	Insecure       bool              `json:"insecure,omitempty"`      // skip TLS verify (local dev)
	AllowPrivate   bool              `json:"allow_private,omitempty"` // permit RFC1918/LAN targets (off by default)
	ExtractText    bool              `json:"extract_text,omitempty"`

	// Assertions (all optional; all that are set must pass).
	ExpectStatus   int      `json:"expect_status,omitempty"`
	ExpectContains []string `json:"expect_contains,omitempty"`
	ExpectAbsent   []string `json:"expect_absent,omitempty"`
	ExpectRegex    string   `json:"expect_regex,omitempty"`
	ExpectJSONPath string   `json:"expect_json_path,omitempty"` // dotted path, e.g. data.items.0.id
	ExpectJSONVal  string   `json:"expect_json_value,omitempty"`
	MaxLatencyMs   int      `json:"max_latency_ms,omitempty"`
}

// Assertion is one evaluated condition.
type Assertion struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
	Pass   bool   `json:"pass"`
}

// Result is the outcome of a Check.
type Result struct {
	URL           string            `json:"url"`
	Method        string            `json:"method"`
	StatusCode    int               `json:"status_code"`
	LatencyMs     float64           `json:"latency_ms"`
	FinalURL      string            `json:"final_url,omitempty"`
	ContentType   string            `json:"content_type,omitempty"`
	BodyBytes     int               `json:"body_bytes"`
	Headers       map[string]string `json:"headers,omitempty"`
	Text          string            `json:"text,omitempty"`    // extracted text (when ExtractText)
	Snippet       string            `json:"snippet,omitempty"` // short body preview
	Assertions    []Assertion       `json:"assertions"`
	Passed        bool              `json:"passed"`
	NeedsJSRender bool              `json:"needs_js_render,omitempty"` // page is JS-rendered; HTTP body is an empty shell
	Rendered      bool              `json:"rendered,omitempty"`        // body was produced by the headless render tier
	Error         string            `json:"error,omitempty"`
}

const maxBody = 4 << 20 // 4 MiB

// Run executes a Check and evaluates its assertions. Network failures are
// returned in Result.Error with Passed=false rather than as a Go error, so the
// agent always gets a structured result.
func Run(ctx context.Context, c Check) Result {
	method := strings.ToUpper(strings.TrimSpace(c.Method))
	if method == "" {
		method = http.MethodGet
	}
	res := Result{URL: c.URL, Method: method}

	timeout := time.Duration(c.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	// SSRF guard: loopback stays reachable (verifying a local dev server is the
	// core use case) but RFC1918/LAN is opt-in and link-local/cloud-metadata is
	// always denied. Redirects are re-checked at dial time by the same policy.
	client := netguard.Client(timeout, netguard.Policy{AllowLoopback: true, AllowPrivate: c.AllowPrivate}, c.Insecure)
	if !c.FollowRedirect {
		client.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}

	var bodyReader io.Reader
	if c.Body != "" {
		bodyReader = strings.NewReader(c.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL, bodyReader)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "codehelper-web/1.0")
	}

	start := time.Now()
	resp, err := client.Do(req)
	res.LatencyMs = float64(time.Since(start).Microseconds()) / 1000.0
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer resp.Body.Close()
	res.StatusCode = resp.StatusCode
	res.ContentType = resp.Header.Get("Content-Type")
	if resp.Request != nil && resp.Request.URL != nil {
		res.FinalURL = resp.Request.URL.String()
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	res.BodyBytes = len(body)
	res.Headers = pickHeaders(resp.Header)
	bodyStr := string(body)
	res.Snippet = snippet(bodyStr, 280)

	// JS-render escalation: only meaningful for HTML responses. If the page is a
	// client-rendered shell, either render it with the headless tier (when wired)
	// or flag needs_js_render so the model knows the HTTP body is not the content.
	text := ""
	if strings.Contains(strings.ToLower(res.ContentType), "html") {
		text = docs.HTMLToText(bodyStr)
		if looksJSRendered(bodyStr, text) {
			if DefaultRenderer != nil {
				if rendered, rerr := DefaultRenderer.Render(ctx, c.URL); rerr == nil && strings.TrimSpace(rendered) != "" {
					bodyStr = rendered
					text = docs.HTMLToText(bodyStr)
					res.Rendered = true
					res.BodyBytes = len(bodyStr)
					res.Snippet = snippet(bodyStr, 280)
				} else {
					res.NeedsJSRender = true
				}
			} else {
				res.NeedsJSRender = true
			}
		}
	}
	if c.ExtractText {
		if text == "" {
			text = docs.HTMLToText(bodyStr)
		}
		res.Text = text
	}

	res.Assertions = evaluate(c, res.StatusCode, res.LatencyMs, bodyStr)
	res.Passed = allPass(res.Assertions)
	if len(res.Assertions) == 0 {
		// No explicit assertions: success means a non-error HTTP status.
		res.Passed = res.StatusCode > 0 && res.StatusCode < 400
	}
	return res
}

func evaluate(c Check, status int, latencyMs float64, body string) []Assertion {
	var a []Assertion
	if c.ExpectStatus != 0 {
		a = append(a, Assertion{
			Kind: "status", Detail: fmt.Sprintf("want %d got %d", c.ExpectStatus, status),
			Pass: status == c.ExpectStatus,
		})
	}
	for _, want := range c.ExpectContains {
		a = append(a, Assertion{
			Kind: "contains", Detail: want, Pass: strings.Contains(body, want),
		})
	}
	for _, absent := range c.ExpectAbsent {
		a = append(a, Assertion{
			Kind: "absent", Detail: absent, Pass: !strings.Contains(body, absent),
		})
	}
	if c.ExpectRegex != "" {
		re, err := regexp.Compile(c.ExpectRegex)
		a = append(a, Assertion{
			Kind: "regex", Detail: c.ExpectRegex, Pass: err == nil && re.MatchString(body),
		})
	}
	if c.ExpectJSONPath != "" {
		got, ok := jsonPath(body, c.ExpectJSONPath)
		pass := ok
		detail := fmt.Sprintf("%s = %v", c.ExpectJSONPath, got)
		if c.ExpectJSONVal != "" {
			pass = ok && fmt.Sprintf("%v", got) == c.ExpectJSONVal
			detail = fmt.Sprintf("%s want %q got %q", c.ExpectJSONPath, c.ExpectJSONVal, fmt.Sprintf("%v", got))
		}
		a = append(a, Assertion{Kind: "json_path", Detail: detail, Pass: pass})
	}
	if c.MaxLatencyMs > 0 {
		a = append(a, Assertion{
			Kind: "latency", Detail: fmt.Sprintf("want <=%dms got %.1fms", c.MaxLatencyMs, latencyMs),
			Pass: latencyMs <= float64(c.MaxLatencyMs),
		})
	}
	return a
}

// jsonPath resolves a dotted path (object keys and numeric array indexes)
// against a JSON document. Returns the value and whether the path resolved.
func jsonPath(body, path string) (any, bool) {
	var doc any
	if json.Unmarshal([]byte(body), &doc) != nil {
		return nil, false
	}
	cur := doc
	for _, seg := range strings.Split(path, ".") {
		if seg == "" {
			continue
		}
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[seg]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			idx := -1
			if _, err := fmt.Sscanf(seg, "%d", &idx); err != nil || idx < 0 || idx >= len(node) {
				return nil, false
			}
			cur = node[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

func allPass(a []Assertion) bool {
	for _, x := range a {
		if !x.Pass {
			return false
		}
	}
	return true
}

func pickHeaders(h http.Header) map[string]string {
	keep := []string{"Content-Type", "Content-Length", "Location", "Cache-Control", "Server", "Set-Cookie"}
	out := map[string]string{}
	for _, k := range keep {
		if v := h.Get(k); v != "" {
			out[k] = v
		}
	}
	return out
}

func snippet(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
