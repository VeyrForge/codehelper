package mcpsvc

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/connections"
	"github.com/VeyrForge/codehelper/internal/ops"
	"github.com/VeyrForge/codehelper/internal/projcfg"
	"github.com/VeyrForge/codehelper/internal/registry"
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
//
// WordPress admin: recipe=wp_login|wp_admin with site=<connections website name>
// fills the login form from encrypted/env secrets (never echoed in logs) and
// waits for #wpadminbar. Configure via `codehelper connections add-site`.
func browserHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		if !web.BrowserAvailable() {
			return mcp.NewToolResultError("browser tier not built into this binary — rebuild with `codehelper update` or `scripts/install.sh` (default includes -tags rod)"), nil
		}
		url := argString(args, "url")
		recipe := argString(args, "recipe")
		siteName := argString(args, "site")
		actions := parseActions(args["actions"])
		if msg := unsupportedBrowserActionHint(actions); msg != "" {
			return mcp.NewToolResultError(msg), nil
		}

		if recipe != "" || siteName != "" {
			resolvedURL, recipeActs, err := resolveBrowserSiteRecipe(ctx, reg, args)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if url == "" {
				url = resolvedURL
			}
			if len(recipeActs) > 0 {
				actions = append(recipeActs, actions...)
			}
		}
		if url == "" {
			return mcp.NewToolResultError("url is required (or pass site=… with a connections website profile)"), nil
		}
		allowPrivate := argBool(args, "allow_private", false)
		if _, set := args["allow_private"]; !set {
			if repo, rerr := resolveRepo(ctx, reg, argString(args, "repo")); rerr == nil {
				if pcfg, perr := projcfg.Load(repo.RootPath); perr == nil && pcfg.BrowserAllowPrivate != nil {
					allowPrivate = *pcfg.BrowserAllowPrivate
				}
			}
		}
		if err := web.GuardURL(url, allowPrivate); err != nil {
			return mcp.NewToolResultError("blocked: " + err.Error()), nil
		}

		devices, err := resolveDevices(args)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		base := web.BrowserOptions{
			URL:            url,
			Width:          int(mcp.ParseInt64(req, "width", 0)),
			Height:         int(mcp.ParseInt64(req, "height", 0)),
			Format:         web.NormalizeFormat(argString(args, "format")),
			Quality:        int(mcp.ParseInt64(req, "quality", 0)),
			FullPage:       argBool(args, "full_page", false),
			Selector:       argString(args, "selector"),
			WaitSelector:   argString(args, "wait_selector"),
			WaitMS:         int(mcp.ParseInt64(req, "wait_ms", 0)),
			TimeoutSec:     int(mcp.ParseInt64(req, "timeout_sec", 0)),
			AllowPrivate:   allowPrivate,
			Metrics:        argBool(args, "metrics", false),
			SegmentPx:      int(mcp.ParseInt64(req, "segment_height", 0)),
			ClipY:          int(mcp.ParseInt64(req, "clip_y", 0)),
			ClipHeight:     int(mcp.ParseInt64(req, "clip_height", 0)),
			Actions:        actions,
			Outline:        argBool(args, "outline", false),
			Snapshot:       argBool(args, "snapshot", false),
			Trace:          argBool(args, "trace", false),
			WaitHydrate:    argBool(args, "wait_hydrate", false),
			Headed:         resolveHeaded(args, projcfgBrowserHeaded(ctx, reg, args)),
			SlowMoMS:       int(mcp.ParseInt64(req, "slow_mo", 0)),
			PauseOnFail:    resolvePauseOnFail(args),
			PauseOnFailMS:  int(mcp.ParseInt64(req, "pause_on_fail_ms", 0)),
			Session:        argString(args, "session"),
			SessionClear:   argBool(args, "session_clear", false),
			WriteDebugPack: true,
			DebugPackDir:   argString(args, "debug_pack_dir"),
		}
		if repo, rerr := resolveRepo(ctx, reg, argString(args, "repo")); rerr == nil {
			base.WorkspaceRoot = repo.RootPath
		} else if wd, werr := os.Getwd(); werr == nil {
			base.WorkspaceRoot = wd
		}
		if allow := argString(args, "upload_allow"); allow != "" {
			base.UploadAllowDirs = filepath.SplitList(allow)
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
	consoleErrors := filterConsoleErrors(r.Console)
	fmt.Fprintf(&b, "diagnostics: %d console error(s) · %d uncaught · %d failed request(s ≥400/transport)\n",
		len(consoleErrors), len(r.PageErrors), len(r.Failed))
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
		fmt.Fprintf(&b, "\nINTERACTIVE ELEMENTS (%d) — use ref:eN or copy selector into `actions`:\n", len(r.Outline))
		for _, e := range r.Outline {
			ref := e.Ref
			if ref == "" {
				ref = "?"
			}
			fmt.Fprintf(&b, "  [%s] [%s] %s", ref, e.Role, e.Selector)
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
	if strings.TrimSpace(r.Snapshot) != "" {
		fmt.Fprintf(&b, "\nARIA SNAPSHOT (bounded; prefer role/name/testid locators):\n%s\n", r.Snapshot)
	}
	if len(r.Trace) > 0 {
		b.WriteString("\nTRACE:\n")
		for _, ev := range r.Trace {
			fmt.Fprintf(&b, "  +%dms [%s] %s\n", ev.AtMS, ev.Kind, oneLine(ev.Detail))
		}
	}
	if r.FailureShot {
		b.WriteString("\nfailure screenshot: attached (last failed step) — inspect image before the final capture\n")
	}
	if r.FailurePack != nil && r.FailurePack.Failed {
		b.WriteString("\nFAILURE DEBUG PACK (one bundle for debug→change→retest):\n")
		if r.FailurePack.ReportPath != "" {
			fmt.Fprintf(&b, "  report: %s\n", r.FailurePack.ReportPath)
		}
		if r.FailurePack.ScreenshotPath != "" {
			fmt.Fprintf(&b, "  screenshot: %s\n", r.FailurePack.ScreenshotPath)
		}
		if r.FailurePack.PackDir != "" {
			fmt.Fprintf(&b, "  pack_dir: %s\n", r.FailurePack.PackDir)
		}
		fmt.Fprintf(&b, "  url: %s\n", r.FailurePack.FinalURL)
		fmt.Fprintf(&b, "  console_errors: %d · page_errors: %d · failed_requests: %d · outline: %d · snapshot_chars: %d\n",
			len(r.FailurePack.ConsoleErrors), len(r.FailurePack.PageErrors), len(r.FailurePack.FailedRequests),
			len(r.FailurePack.Outline), len(r.FailurePack.Snapshot))
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
	if len(consoleErrors) > 0 {
		fmt.Fprintf(&b, "\nCONSOLE ERRORS (%d):\n", len(consoleErrors))
		for _, m := range capMessages(consoleErrors, 40) {
			fmt.Fprintf(&b, "  [%s] %s\n", m.Level, oneLine(m.Text))
		}
	}
	if len(r.Console) > 0 {
		fmt.Fprintf(&b, "\nCONSOLE (%d):\n", len(r.Console))
		for _, m := range capMessages(r.Console, 40) {
			fmt.Fprintf(&b, "  [%s] %s\n", m.Level, oneLine(m.Text))
		}
	}
	if len(r.Failed) > 0 {
		fmt.Fprintf(&b, "\nFAILED REQUESTS (%d, status≥400 or transport error):\n", len(r.Failed))
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
	if len(r.PageErrors) == 0 && len(r.Failed) == 0 && len(consoleErrors) == 0 {
		b.WriteString("\nno page errors, console errors, or failed requests.\n")
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

// resolveHeaded decides whether to run a visible browser. Precedence:
// per-call headed|gui → CODEHELPER_BROWSER_HEADED → project browser_headed → false.
func resolveHeaded(args map[string]any, projectDefault *bool) bool {
	if _, ok := args["headed"]; ok {
		return argBool(args, "headed", false)
	}
	if _, ok := args["gui"]; ok {
		return argBool(args, "gui", false)
	}
	if web.HeadedFromEnv() {
		return true
	}
	// Explicit env off wins over project default.
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CODEHELPER_BROWSER_HEADED"))) {
	case "0", "false", "no", "off":
		return false
	}
	if projectDefault != nil {
		return *projectDefault
	}
	return false
}

func projcfgBrowserHeaded(ctx context.Context, reg *registry.Registry, args map[string]any) *bool {
	repo, err := resolveRepo(ctx, reg, argString(args, "repo"))
	if err != nil {
		return nil
	}
	cfg, err := projcfg.Load(repo.RootPath)
	if err != nil {
		return nil
	}
	return cfg.BrowserHeaded
}

// resolvePauseOnFail decides whether to pause the headed window after a failed
// step. Per-call pause_on_fail wins; else CODEHELPER_BROWSER_PAUSE_ON_FAIL.
func resolvePauseOnFail(args map[string]any) bool {
	if _, ok := args["pause_on_fail"]; ok {
		return argBool(args, "pause_on_fail", false)
	}
	return web.PauseOnFailFromEnv()
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
			Role:     mapStr(m, "role"),
			Name:     firstNonEmpty(mapStr(m, "name"), mapStr(m, "accessible_name")),
			TestID:   firstNonEmpty(mapStr(m, "testid"), mapStr(m, "test_id"), mapStr(m, "data-testid")),
			Ref:      firstNonEmpty(mapStr(m, "ref"), mapStr(m, "outline_ref")),
		})
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// unsupportedBrowserActionHint returns a clear message for actions that cannot
// run without required fields (e.g. upload missing path), so agents do not burn
// a browser launch on a guaranteed failure. Upload itself is implemented via
// rod Element.SetFiles when selector + text(path) are present.
func unsupportedBrowserActionHint(actions []web.Action) string {
	for _, a := range actions {
		switch strings.ToLower(strings.TrimSpace(a.Do)) {
		case "upload", "set_input_files", "setinputfiles", "attach":
			if strings.TrimSpace(a.Selector) == "" || strings.TrimSpace(a.Text) == "" {
				return fmt.Sprintf("browser action %q requires selector= (file input) and text= (filesystem path; multi-file: newline or || separated, sandboxed to workspace/allowlist)", a.Do)
			}
		}
	}
	return ""
}

// resolveBrowserSiteRecipe loads a connections website profile and expands a
// named recipe (wp_login, laravel_login, django_admin, spa_hydrate, …).
// Passwords come from env:/secret only — never from MCP args — and are marked
// Sensitive so action logs redact them. Auth-less recipes (spa_hydrate) skip
// credential checks.
func resolveBrowserSiteRecipe(ctx context.Context, reg *registry.Registry, args map[string]any) (url string, actions []web.Action, err error) {
	recipe := argString(args, "recipe")
	siteName := argString(args, "site")
	if recipe == "" && siteName == "" {
		return "", nil, nil
	}
	if siteName == "" {
		return "", nil, fmt.Errorf("recipe=%q requires site=<connections website name> (configure with `codehelper connections add-site`)", recipe)
	}
	repo, rerr := resolveRepo(ctx, reg, argString(args, "repo"))
	if rerr != nil {
		return "", nil, fmt.Errorf("site recipe needs an indexed project workspace: %w", rerr)
	}
	cfg, err := connections.Load(repo.RootPath)
	if err != nil {
		return "", nil, err
	}
	site := cfg.FindWebSite(siteName)
	if site == nil {
		return "", nil, fmt.Errorf("no website profile %q — add with `codehelper connections add-site --name %s --url http://… --kind %s --user …`", siteName, siteName, strings.Join(connections.SupportedSiteKinds(), "|"))
	}
	if !site.Enabled() {
		return "", nil, fmt.Errorf("website profile %q is disabled", siteName)
	}
	if recipe == "" {
		if pcfg, perr := loadProjcfgBrowserRecipe(repo.RootPath); perr == nil && pcfg != "" {
			recipe = pcfg
		} else {
			recipe = site.DefaultRecipe()
		}
	}
	user, pass := site.User, ""
	if web.RecipeNeedsAuth(recipe) {
		pass, err = ops.ResolveRef(repo.RootPath, site.PasswordRef, site.Name)
		if err != nil {
			return "", nil, fmt.Errorf("resolve site password: %w", err)
		}
		if strings.TrimSpace(user) == "" || pass == "" {
			return "", nil, fmt.Errorf("website %q needs user + password_ref (env:VAR or `connections set-secret --name %s`)", siteName, siteName)
		}
	}
	skipLogin := !argBool(args, "session_clear", false) && web.SessionHasCookies(argString(args, "session"))
	acts, err := web.ExpandRecipeOptions(recipe, user, pass, strings.TrimRight(strings.TrimSpace(site.BaseURL), "/"), skipLogin)
	if err != nil {
		return "", nil, err
	}
	switch strings.ToLower(strings.TrimSpace(recipe)) {
	case web.RecipeWPAdmin:
		url, err = site.AdminURL()
	case web.RecipeWPPlugins, "wordpress_plugins", "wp-plugins":
		url, err = site.PathURL("/wp-admin/plugins.php")
	case web.RecipeWPPosts, "wordpress_posts", "wp-posts":
		url, err = site.PathURL("/wp-admin/edit.php")
	case web.RecipeWPNewPost, "wordpress_new_post", "wp-new-post":
		url, err = site.PathURL("/wp-admin/post-new.php")
	case web.RecipeSPAHydrate, "spa", "hydrate", "spa-hydrate":
		url = strings.TrimRight(strings.TrimSpace(site.BaseURL), "/")
		if url == "" {
			err = fmt.Errorf("site %q has empty base_url", siteName)
		}
	case web.RecipeLaravelLogin, "laravel", "laravel-login":
		if skipLogin {
			url, err = site.AdminURL()
		} else {
			url, err = site.LoginURL()
		}
	case web.RecipeDjangoAdmin, "django", "django-admin", "django_login":
		if skipLogin {
			url, err = site.AdminURL()
		} else {
			url, err = site.LoginURL()
		}
	case web.RecipeDrupalLogin, "drupal", "drupal-login":
		if skipLogin {
			url, err = site.AdminURL()
		} else {
			url, err = site.LoginURL()
		}
	case web.RecipeMagentoLogin, "magento", "magento-login", "magento_admin":
		if skipLogin {
			url, err = site.AdminURL()
		} else {
			url, err = site.LoginURL()
		}
	default:
		if skipLogin {
			url, err = site.AdminURL()
		} else {
			url, err = site.LoginURL()
		}
	}
	if err != nil {
		return "", nil, err
	}
	return url, acts, nil
}

func loadProjcfgBrowserRecipe(repoRoot string) (string, error) {
	cfg, err := projcfg.Load(repoRoot)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(cfg.BrowserRecipe), nil
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

func filterConsoleErrors(in []web.ConsoleMessage) []web.ConsoleMessage {
	out := make([]web.ConsoleMessage, 0, len(in))
	for _, m := range in {
		switch strings.ToLower(strings.TrimSpace(m.Level)) {
		case "error", "assert", "exception":
			out = append(out, m)
		}
	}
	return out
}
