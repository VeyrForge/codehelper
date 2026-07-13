package web

// Shared, dependency-free types and helpers for the headless-browser capture
// tool. Kept untagged (no `rod` build constraint) so the MCP tool and CLI
// compile in every build; the actual Chrome-driving implementation lives in the
// build-tagged browser_rod.go (real) / browser_stub.go (unavailable) pair.
//
// Design (informed by what Playwright-MCP gets wrong — see AGENTS.md):
//   - LEAN by default: one tool, a bounded report, no full-DOM/accessibility-tree
//     dump. A single Playwright screenshot has hit 200K+ tokens; we cap.
//   - WebP screenshots by default — Chromium encodes them natively (no Go image
//     deps), 60-80% smaller than PNG, so the image costs fewer transport bytes.
//   - Correct, named device presets (mobile/tablet/desktop) so responsive checks
//     don't need the agent to remember dimensions.
//   - MIME MUST match the bytes (Playwright shipped a media-type-mismatch bug
//     that 400'd the whole request) — validated before we hand back an image.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/VeyrForge/codehelper/internal/netguard"
	"github.com/VeyrForge/codehelper/internal/paths"
)

// ErrBrowserUnavailable is returned by EnsureBrowser/CaptureBrowser in a build
// without the headless-browser tier compiled in (no `rod` build tag).
var ErrBrowserUnavailable = errors.New(
	"browser tier not built into this binary — rebuild with `-tags rod` (the default build includes it) to enable screenshots/console")

// Screenshot formats. WebP is the default: Chromium encodes it natively and it
// is markedly smaller than PNG, which keeps the image payload small.
const (
	FormatWebP = "webp"
	FormatPNG  = "png"
	FormatJPEG = "jpeg"
)

// Device is a named viewport preset: a real-world width/height plus the device
// pixel ratio, mobile-emulation flag, and UA so responsive sites serve the right
// layout. Scale is deliberately capped at 2 (even for phones whose true DPR is 3)
// — past 2× the screenshot barely looks sharper to a model but the byte size
// climbs fast, and the agent's image tokens are governed by the resized
// dimensions either way.
type Device struct {
	Name   string
	Width  int
	Height int
	Scale  float64
	Mobile bool
	UA     string
}

const (
	uaIPhone = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"
	uaIPad   = "Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"
)

// deviceList is ordered desktop→mobile so a "responsive" multi-capture reads
// large→small. Dimensions are widely-used real devices: iPhone 14 (390×844),
// iPad (768×1024), and a common laptop (1280×800).
var deviceList = []Device{
	{Name: "desktop", Width: 1280, Height: 800, Scale: 1, Mobile: false},
	{Name: "tablet", Width: 768, Height: 1024, Scale: 2, Mobile: true, UA: uaIPad},
	{Name: "mobile", Width: 390, Height: 844, Scale: 2, Mobile: true, UA: uaIPhone},
}

// Devices returns the preset list (desktop, tablet, mobile).
func Devices() []Device {
	out := make([]Device, len(deviceList))
	copy(out, deviceList)
	return out
}

// ResolveDevice maps a preset name to its Device. An empty name is desktop; an
// unknown name returns ok=false so the caller can report it instead of silently
// using the wrong size.
func ResolveDevice(name string) (Device, bool) {
	if name == "" {
		return deviceList[0], true
	}
	for _, d := range deviceList {
		if d.Name == name {
			return d, true
		}
	}
	return Device{}, false
}

// BrowserOptions controls one capture.
type BrowserOptions struct {
	URL            string
	Device         string   // preset name: desktop (default) | tablet | mobile
	Width          int      // explicit viewport width override (0 = use device)
	Height         int      // explicit viewport height override (0 = use device)
	Format         string   // webp (default) | png | jpeg
	Quality        int      // 1-100 for webp/jpeg (default 80); ignored for png
	FullPage       bool     // capture the entire scrollable page, not just the viewport
	SegmentPx      int      // when >0, split a full-page capture into vertical pieces ≤ this many CSS px tall
	ClipY          int      // capture only the region starting at this Y (CSS px)
	ClipHeight     int      // height of the clipped region (CSS px); requires ClipY usage
	Selector       string   // screenshot only this element (CSS selector) when set
	WaitSelector   string   // wait for this selector before capturing
	WaitMS         int      // additional fixed wait after load (milliseconds)
	TimeoutSec     int      // overall navigation/capture timeout (default 30)
	AllowPrivate   bool     // permit RFC1918/LAN targets (loopback is always allowed)
	Metrics        bool     // collect navigation/paint timing + resource weight
	Audit          bool     // run the accessibility + Core Web Vitals audit
	AuditFull      bool     // use the full axe-core engine for a11y (vs the lite scan)
	Actions        []Action // interaction steps to run before capturing
	Outline        bool     // return a compact map of interactive elements + ready-to-use selectors
	Headed         bool     // run a VISIBLE browser (default headless) so a human can watch it drive the page
	SlowMoMS       int      // per-action delay in ms for headed mode (0 = default pacing); also drives the highlight dwell
	PreviewActions bool     // capture a screenshot after each action step (requires user config)
}

// ActionPreview is one viewport screenshot taken immediately after an interaction
// step when PreviewActions is enabled.
type ActionPreview struct {
	Step  int    `json:"step"`
	Label string `json:"label"`
	Image []byte `json:"-"`
}

// Action is one interaction step run before the final capture (the "drive the
// page" capability): click, type, fill, press a key, scroll, hover, wait, or
// assert. Steps run in order and stop at the first failure (the screenshot then
// shows where it got stuck), so a flow reads top-to-bottom like a script and
// the asserts make it a real pass/fail test.
type Action struct {
	Do       string `json:"action"`   // click | type | fill | press | scroll | hover | wait | assert
	Selector string `json:"selector"` // CSS target for click/type/fill/hover/scroll/wait/assert
	Text     string `json:"text"`     // text for type/fill; for assert: substring the element must contain
	Key      string `json:"key"`      // key name for press, e.g. Enter, Tab, Escape
	Y        int    `json:"y"`        // pixels for scroll (when no selector)
	MS       int    `json:"ms"`       // milliseconds for wait (when no selector)
}

// OutlineElement is one interactive element the agent can drive: a ready-to-use
// CSS selector (stable id/name/data-testid when available, else a short nth-of-type
// path) plus enough context — role, accessible name, input type — to decide what to
// click/fill without pulling the whole DOM into context. This is the opt-in,
// bounded answer to "what selectors exist on the form I just wrote?".
type OutlineElement struct {
	Selector    string `json:"selector"`
	Role        string `json:"role"` // button | link | textbox | checkbox | radio | select | ...
	Name        string `json:"name"` // accessible name: aria-label, <label>, placeholder, or visible text
	Type        string `json:"type,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Value       string `json:"value,omitempty"`
}

// ConsoleMessage is one browser console entry (console.log/warn/error/...).
type ConsoleMessage struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

// FailedRequest is a network request that errored or returned an HTTP error.
type FailedRequest struct {
	URL    string `json:"url"`
	Status int    `json:"status,omitempty"` // 0 for a transport-level failure
	Error  string `json:"error,omitempty"`  // Chrome's error text, when transport failed
}

// A11yIssue is one accessibility finding. The lite scan fills rule/count/sample;
// the full axe-core run also fills impact (minor/moderate/serious/critical) and
// help (the rule's guidance).
type A11yIssue struct {
	Rule   string `json:"rule"`
	Impact string `json:"impact,omitempty"`
	Count  int    `json:"count"`
	Help   string `json:"help,omitempty"`
	Sample string `json:"sample"`
}

// Vitals are Core Web Vitals + key paint/network timings.
type Vitals struct {
	LCPms  int     `json:"lcp_ms"`  // largest contentful paint
	CLS    float64 `json:"cls"`     // cumulative layout shift
	FCPms  int     `json:"fcp_ms"`  // first contentful paint
	TTFBms int     `json:"ttfb_ms"` // time to first byte
}

// PerfMetrics is a compact page-performance snapshot (the "web performance
// check" use case): paint + navigation timing and how much the page weighs.
type PerfMetrics struct {
	FCPms            int `json:"fcp_ms"`               // first contentful paint
	DOMContentLoaded int `json:"dom_content_loaded"`   // ms
	LoadMs           int `json:"load_ms"`              // load event end, ms
	Requests         int `json:"requests"`             // resource count
	TransferKB       int `json:"transfer_kb"`          // total bytes over the wire / 1024
	JSHeapMB         int `json:"js_heap_mb,omitempty"` // used JS heap
}

// BrowserResult is everything one capture observed.
type BrowserResult struct {
	FinalURL       string           `json:"final_url"`
	Title          string           `json:"title"`
	Device         string           `json:"device"`
	Viewport       string           `json:"viewport"` // "WxH@Sx"
	DocStatus      int              `json:"doc_status"`
	Format         string           `json:"format"`
	MIME           string           `json:"mime"`
	Image          []byte           `json:"-"`                  // primary screenshot bytes in Format (first tile when split)
	Tiles          [][]byte         `json:"-"`                  // vertical pieces of a split full-page capture (Image is Tiles[0])
	PageDim        string           `json:"page_dim,omitempty"` // full content "WxH" when measured
	Console        []ConsoleMessage `json:"console"`
	PageErrors     []string         `json:"page_errors"`
	Failed         []FailedRequest  `json:"failed_requests"`
	Perf           *PerfMetrics     `json:"perf,omitempty"`
	Vitals         *Vitals          `json:"vitals,omitempty"`
	A11y           []A11yIssue      `json:"a11y,omitempty"`
	Outline        []OutlineElement `json:"outline,omitempty"`         // interactive elements + selectors (when Outline requested)
	Headed         bool             `json:"headed,omitempty"`          // whether this capture ran in a visible browser
	ActionLog      []string         `json:"action_log,omitempty"`      // one line per interaction step
	ActionPreviews []ActionPreview  `json:"action_previews,omitempty"` // viewport shot after each step when enabled
	Text           string           `json:"text"`
	LoadMS         int64            `json:"load_ms"`
}

// MIMEForFormat returns the image MIME type for a screenshot format.
func MIMEForFormat(format string) string {
	switch format {
	case FormatPNG:
		return "image/png"
	case FormatJPEG:
		return "image/jpeg"
	default:
		return "image/webp"
	}
}

// NormalizeFormat coerces an unknown/empty format to the WebP default.
func NormalizeFormat(format string) string {
	switch format {
	case FormatPNG, FormatJPEG, FormatWebP:
		return format
	default:
		return FormatWebP
	}
}

// ImageMagicMatches verifies the bytes actually are the claimed format. This is
// the guard against the exact bug Playwright-MCP shipped — declaring image/jpeg
// for non-jpeg bytes, which makes Claude's API reject the whole message with a
// 400. We never hand back an image whose magic bytes disagree with its MIME.
func ImageMagicMatches(data []byte, format string) bool {
	switch format {
	case FormatPNG:
		return len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	case FormatJPEG:
		return len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff
	case FormatWebP:
		return len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP"))
	}
	return false
}

// BrowserDir is where the managed Chromium-for-Testing is downloaded. It lives
// under codehelper's own config dir (~/.codehelper/browser), deliberately
// separate from any system Chrome/Firefox install and from rod's default
// ~/.cache/rod — so provisioning never touches a browser the user already has.
func BrowserDir() (string, error) {
	base, err := paths.RegistryDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "browser"), nil
}

// axeURL is the pinned axe-core build fetched for the full accessibility audit.
const axeURL = "https://cdn.jsdelivr.net/npm/axe-core@4.10.2/axe.min.js"

// AxePath is where the axe-core bundle is cached (next to the managed browser).
func AxePath() (string, error) {
	dir, err := BrowserDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "axe.min.js"), nil
}

// EnsureAxe downloads axe-core once into AxePath and returns the path. Idempotent
// — a present, non-empty file is reused. Called by `ch browser install` (so the
// full audit is ready offline) and lazily by the full-audit path.
func EnsureAxe(ctx context.Context) (string, error) {
	p, err := AxePath()
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
		return p, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, axeURL, nil)
	if err != nil {
		return "", err
	}
	cl := &http.Client{Timeout: 60 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return "", fmt.Errorf("download axe-core: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download axe-core: http %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return "", err
	}
	return p, nil
}

// GuardURL enforces the same SSRF policy as the HTTP web tool before a browser
// ever navigates: only http/https, loopback always allowed, RFC1918/LAN gated by
// allowPrivate, and cloud-metadata/link-local always denied. It resolves the
// host and checks every resolved IP. This is best-effort (a page can still cause
// the browser to fetch sub-resources, and DNS can rebind), so the browser is
// meant for local/dev verification, not for pointing at untrusted hosts.
func GuardURL(raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported url scheme %q (only http/https)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("url has no host")
	}
	pol := netguard.Policy{AllowLoopback: true, AllowPrivate: allowPrivate}
	if ip := net.ParseIP(host); ip != nil {
		return pol.CheckIP(ip)
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", host, err)
	}
	for _, ip := range ips {
		if err := pol.CheckIP(ip); err != nil {
			return err
		}
	}
	return nil
}
