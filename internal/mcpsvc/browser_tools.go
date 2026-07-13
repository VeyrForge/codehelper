package mcpsvc

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/VeyrForge/codehelper/internal/web"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// browserHandler serves the `browser` MCP tool: render a URL in headless
// Chromium and return a screenshot the agent can SEE (WebP, so it's small) plus
// the console output, uncaught page errors, failed network requests, optional
// performance metrics, and page metadata — the visual + diagnostic counterpart
// to the HTTP-only `web` tool.
//
// It is deliberately ONE lean tool with a bounded report: no full-DOM or
// accessibility-tree dump (the thing that makes Playwright-MCP cost 100K+
// tokens). Pass device=mobile|tablet|desktop, or devices=["mobile","desktop"]
// (or "all") to capture several viewports in one call.
func browserHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		url := argString(args, "url")
		if url == "" {
			return mcp.NewToolResultError("url is required"), nil
		}
		allowPrivate := argBool(args, "allow_private", false)
		if err := web.GuardURL(url, allowPrivate); err != nil {
			return mcp.NewToolResultError("blocked: " + err.Error()), nil
		}

		devices, err := resolveDevices(args)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		base := web.BrowserOptions{
			URL:          url,
			Width:        int(mcp.ParseInt64(req, "width", 0)),
			Height:       int(mcp.ParseInt64(req, "height", 0)),
			Format:       web.NormalizeFormat(argString(args, "format")),
			Quality:      int(mcp.ParseInt64(req, "quality", 0)),
			FullPage:     argBool(args, "full_page", false),
			Selector:     argString(args, "selector"),
			WaitSelector: argString(args, "wait_selector"),
			WaitMS:       int(mcp.ParseInt64(req, "wait_ms", 0)),
			TimeoutSec:   int(mcp.ParseInt64(req, "timeout_sec", 0)),
			AllowPrivate: allowPrivate,
			Metrics:      argBool(args, "metrics", false),
			SegmentPx:    int(mcp.ParseInt64(req, "segment_height", 0)),
			ClipY:        int(mcp.ParseInt64(req, "clip_y", 0)),
			ClipHeight:   int(mcp.ParseInt64(req, "clip_height", 0)),
			Actions:      parseActions(args["actions"]),
			Outline:      argBool(args, "outline", false),
			Headed:       resolveHeaded(args),
			SlowMoMS:     int(mcp.ParseInt64(req, "slow_mo", 0)),
		}
		previewRequested := argBool(args, "preview_actions", false)
		base.PreviewActions = web.PreviewActionsAllowed(previewRequested)
		// `split` is a shorthand: full-page, sliced into readable pieces.
		if argBool(args, "split", false) && base.SegmentPx == 0 {
			base.SegmentPx = 2000
		}
		if base.SegmentPx > 0 {
			base.FullPage = true // splitting only makes sense on a full-page capture
		}
		base.Audit, base.AuditFull = parseAudit(args)

		baseline := argString(args, "baseline")
		updateBaseline := argBool(args, "update_baseline", false)

		content := make([]mcp.Content, 0, len(devices)+1)
		var reports []string
		for _, dev := range devices {
			opts := base
			opts.Device = dev
			result, err := web.CaptureBrowser(ctx, opts)
			if err != nil {
				if errors.Is(err, web.ErrBrowserUnavailable) {
					return mcp.NewToolResultError(err.Error()), nil
				}
				msg := err.Error()
				if strings.Contains(msg, "provision browser") || strings.Contains(msg, "launch browser") {
					msg += "\n\nRun `ch browser install` once to download the managed browser (~150MB, one time)."
				}
				return mcp.NewToolResultError(msg), nil
			}
			report := renderBrowserReport(result, previewRequested)
			img, mime := result.Image, result.MIME

			if baseline != "" {
				note, diffImg, diffMime := applyBaseline(baseline, updateBaseline, result)
				report += note
				if diffImg != nil {
					img, mime = diffImg, diffMime
				}
			}

			reports = append(reports, report)
			// Step previews first (when enabled), then the final capture.
			if len(result.ActionPreviews) > 0 && baseline == "" {
				for _, p := range result.ActionPreviews {
					content = append(content, mcp.ImageContent{
						Type:     "image",
						Data:     base64.StdEncoding.EncodeToString(p.Image),
						MIMEType: result.MIME,
					})
				}
			}
			// Emit the split pieces as separate images when present (and not diffing);
			// otherwise the single capture/diff image.
			if len(result.Tiles) > 0 && baseline == "" {
				for _, t := range result.Tiles {
					content = append(content, mcp.ImageContent{
						Type:     "image",
						Data:     base64.StdEncoding.EncodeToString(t),
						MIMEType: result.MIME,
					})
				}
			} else {
				content = append(content, mcp.ImageContent{
					Type:     "image",
					Data:     base64.StdEncoding.EncodeToString(img),
					MIMEType: mime,
				})
			}
		}

		// Text report first, then the image(s) — one per captured device.
		text := strings.Join(reports, "\n"+strings.Repeat("─", 32)+"\n")
		return &mcp.CallToolResult{Content: append([]mcp.Content{mcp.TextContent{Type: "text", Text: text}}, content...)}, nil
	}
}

// resolveDevices picks the viewports to capture: an explicit `devices` array
// (or "all"), else the single `device` (default desktop). Unknown names are an
// error, not a silent fallback to the wrong size.
func resolveDevices(args map[string]any) ([]string, error) {
	names := argStringSlice(args, "devices")
	if len(names) == 1 && names[0] == "all" {
		all := web.Devices()
		out := make([]string, len(all))
		for i, d := range all {
			out[i] = d.Name
		}
		return out, nil
	}
	if len(names) == 0 {
		names = []string{argString(args, "device")}
	}
	for i, n := range names {
		if n == "" {
			names[i] = "desktop"
			continue
		}
		if _, ok := web.ResolveDevice(n); !ok {
			return nil, fmt.Errorf("unknown device %q (use desktop|tablet|mobile, or devices=[\"all\"])", n)
		}
	}
	return names, nil
}

// renderBrowserReport is the compact text block paired with the screenshot:
// metadata plus everything an image can't show — console, JS errors, failed
// requests, and (when requested) performance.
func renderBrowserReport(r *web.BrowserResult, previewRequested bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s %s] %s\n", r.Device, r.Viewport, r.FinalURL)
	if r.Title != "" {
		fmt.Fprintf(&b, "title: %s\n", r.Title)
	}
	fmt.Fprintf(&b, "doc_status: %d · loaded in %dms · %s %d bytes\n", r.DocStatus, r.LoadMS, r.Format, len(r.Image))
	if r.Headed {
		b.WriteString("mode: headed (visible browser — actions highlighted on the page)\n")
	}
	if r.PageDim != "" {
		fmt.Fprintf(&b, "page: %s", r.PageDim)
		if len(r.Tiles) > 0 {
			fmt.Fprintf(&b, " · split into %d pieces (top→bottom, full resolution)", len(r.Tiles))
		}
		b.WriteByte('\n')
	}

	if r.Perf != nil {
		p := r.Perf
		fmt.Fprintf(&b, "perf: FCP %dms · DOMContentLoaded %dms · load %dms · %d requests · %dKB",
			p.FCPms, p.DOMContentLoaded, p.LoadMs, p.Requests, p.TransferKB)
		if p.JSHeapMB > 0 {
			fmt.Fprintf(&b, " · heap %dMB", p.JSHeapMB)
		}
		b.WriteByte('\n')
	}
	if r.Vitals != nil {
		v := r.Vitals
		fmt.Fprintf(&b, "vitals: LCP %dms · CLS %.3f · FCP %dms · TTFB %dms%s\n",
			v.LCPms, v.CLS, v.FCPms, v.TTFBms, vitalsVerdict(v))
	}
	if len(r.A11y) > 0 {
		total := 0
		for _, a := range r.A11y {
			total += a.Count
		}
		fmt.Fprintf(&b, "\nA11Y (%d issues across %d rules):\n", total, len(r.A11y))
		for _, a := range r.A11y {
			line := "  • " + a.Rule
			if a.Impact != "" {
				line += " [" + a.Impact + "]"
			}
			fmt.Fprintf(&b, "%s ×%d", line, a.Count)
			if a.Help != "" {
				fmt.Fprintf(&b, " — %s", oneLine(a.Help))
			}
			if a.Sample != "" {
				fmt.Fprintf(&b, "  e.g. %s", oneLine(a.Sample))
			}
			b.WriteByte('\n')
		}
	}
	if len(r.Outline) > 0 {
		fmt.Fprintf(&b, "\nINTERACTIVE ELEMENTS (%d) — copy a selector straight into `actions`:\n", len(r.Outline))
		for _, e := range r.Outline {
			fmt.Fprintf(&b, "  [%s] %s", e.Role, e.Selector)
			if e.Name != "" {
				fmt.Fprintf(&b, " — %q", e.Name)
			}
			var extra []string
			if e.Type != "" {
				extra = append(extra, "type="+e.Type)
			}
			if e.Placeholder != "" {
				extra = append(extra, "placeholder="+oneLine(e.Placeholder))
			}
			if e.Value != "" {
				extra = append(extra, "value="+oneLine(e.Value))
			}
			if len(extra) > 0 {
				fmt.Fprintf(&b, " (%s)", strings.Join(extra, ", "))
			}
			b.WriteByte('\n')
		}
	}
	if len(r.ActionLog) > 0 {
		failed := false
		for _, a := range r.ActionLog {
			if strings.Contains(a, "FAILED") {
				failed = true
			}
		}
		if failed {
			b.WriteString("\nTEST: ✗ FAILED\n")
		} else {
			b.WriteString("\nTEST: ✓ all steps passed\n")
		}
		b.WriteString("ACTIONS:\n")
		for _, a := range r.ActionLog {
			fmt.Fprintf(&b, "  %s\n", a)
		}
		if previewRequested && len(r.ActionPreviews) == 0 {
			b.WriteString("\naction previews: requested but disabled — run `ch config browser set --action-previews on` to enable step screenshots\n")
		} else if len(r.ActionPreviews) > 0 {
			b.WriteString("\nACTION PREVIEWS (viewport after each step, top→bottom before final capture):\n")
			for _, p := range r.ActionPreviews {
				fmt.Fprintf(&b, "  step %d: %s (%d bytes)\n", p.Step, p.Label, len(p.Image))
			}
		}
	}
	if len(r.PageErrors) > 0 {
		b.WriteString("\nPAGE ERRORS (uncaught JS):\n")
		for _, e := range r.PageErrors {
			fmt.Fprintf(&b, "  • %s\n", oneLine(e))
		}
	}
	if len(r.Console) > 0 {
		fmt.Fprintf(&b, "\nCONSOLE (%d):\n", len(r.Console))
		for _, m := range capMessages(r.Console, 40) {
			fmt.Fprintf(&b, "  [%s] %s\n", m.Level, oneLine(m.Text))
		}
	}
	if len(r.Failed) > 0 {
		fmt.Fprintf(&b, "\nFAILED REQUESTS (%d):\n", len(r.Failed))
		for _, f := range r.Failed {
			if f.Status > 0 {
				fmt.Fprintf(&b, "  • %d %s\n", f.Status, f.URL)
			} else {
				fmt.Fprintf(&b, "  • %s — %s\n", f.URL, f.Error)
			}
		}
	}
	if strings.TrimSpace(r.Text) != "" {
		fmt.Fprintf(&b, "\nVISIBLE TEXT:\n%s\n", r.Text)
	}
	if len(r.PageErrors) == 0 && len(r.Failed) == 0 {
		b.WriteString("\nno page errors or failed requests.\n")
	}
	return b.String()
}

// applyBaseline implements the visual-regression flow for one captured device.
// First sight of a baseline (or update_baseline) saves the screenshot and
// returns no diff image. Otherwise it diffs against the stored baseline and
// returns the highlighted diff PNG plus a "% changed" note. Baselines are keyed
// per device so a responsive run keeps mobile/desktop separate.
func applyBaseline(name string, update bool, r *web.BrowserResult) (note string, diffImg []byte, diffMime string) {
	key := name + "_" + r.Device
	prev, ok := web.LoadBaseline(key)
	if !ok || update {
		if err := web.SaveBaseline(key, r.Format, r.Image); err != nil {
			return fmt.Sprintf("\nbaseline %q: save failed: %v\n", key, err), nil, ""
		}
		verb := "saved"
		if ok {
			verb = "updated"
		}
		return fmt.Sprintf("\nbaseline %q %s (%s) — re-run to diff against it\n", key, verb, r.Viewport), nil, ""
	}
	d, err := web.DiffImages(prev, r.Image)
	if err != nil {
		return fmt.Sprintf("\nbaseline %q: diff failed: %v\n", key, err), nil, ""
	}
	if d.SizeMismatch {
		return fmt.Sprintf("\nVISUAL DIFF vs %q: SIZE CHANGED %s → %s (layout shifted)\n", key, d.BaselineDim, d.CurrentDim), d.DiffPNG, "image/png"
	}
	return fmt.Sprintf("\nVISUAL DIFF vs %q: %.2f%% of pixels changed (%d/%d). Diff image: red = changed.\n",
		key, d.ChangedPct, d.ChangedPix, d.TotalPix), d.DiffPNG, "image/png"
}

// parseAudit reads the `audit` argument into (audit, full) flags. It accepts
// "lite"|"full" (the schema), and is robust to the value arriving as a real
// boolean true or the string "true"/"1"/"yes" — clients differ on how they
// coerce a value when a param's type changed from boolean to string.
func parseAudit(args map[string]any) (audit, full bool) {
	switch strings.ToLower(strings.TrimSpace(argString(args, "audit"))) {
	case "full":
		return true, true
	case "lite", "true", "1", "yes", "on":
		return true, false
	default:
		if argBool(args, "audit", false) {
			return true, false
		}
		return false, false
	}
}

// resolveHeaded decides whether to run a visible browser. The per-call `headed`
// argument wins; when absent, CODEHELPER_BROWSER_HEADED (1/true/yes/on) acts as a
// persistent user setting so someone can watch every run without passing the flag
// each time. Default is headless.
func resolveHeaded(args map[string]any) bool {
	if _, ok := args["headed"]; ok {
		return argBool(args, "headed", false)
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CODEHELPER_BROWSER_HEADED"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// parseActions converts the MCP `actions` argument (an array of objects) into
// typed interaction steps. Unknown/garbage entries are skipped rather than
// failing the whole call.
func parseActions(raw any) []web.Action {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]web.Action, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		do := strings.TrimSpace(mapStr(m, "action"))
		if do == "" {
			continue
		}
		out = append(out, web.Action{
			Do:       do,
			Selector: mapStr(m, "selector"),
			Text:     mapStr(m, "text"),
			Key:      mapStr(m, "key"),
			Y:        mapInt(m, "y"),
			MS:       mapInt(m, "ms"),
		})
	}
	return out
}

func mapStr(m map[string]any, k string) string {
	if s, ok := m[k].(string); ok {
		return s
	}
	return ""
}

func mapInt(m map[string]any, k string) int {
	switch v := m[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// vitalsVerdict flags Core Web Vitals against Google's good/poor thresholds so
// the agent gets a judgment, not just numbers (LCP good ≤2.5s, CLS good ≤0.1).
func vitalsVerdict(v *web.Vitals) string {
	var bad []string
	if v.LCPms > 2500 {
		bad = append(bad, "LCP slow")
	}
	if v.CLS > 0.1 {
		bad = append(bad, "CLS high")
	}
	if len(bad) == 0 {
		return "  (good)"
	}
	return "  ⚠ " + strings.Join(bad, ", ")
}

func capMessages(in []web.ConsoleMessage, max int) []web.ConsoleMessage {
	if len(in) <= max {
		return in
	}
	return in[len(in)-max:] // keep the most recent
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
