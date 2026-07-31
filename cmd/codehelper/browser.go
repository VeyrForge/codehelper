package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/connections"
	"github.com/VeyrForge/codehelper/internal/ops"
	"github.com/VeyrForge/codehelper/internal/web"
	"github.com/spf13/cobra"
)

// browserCmd manages the headless browser the `browser` MCP tool drives: a
// one-time download of an isolated Chromium-for-Testing (kept under
// ~/.codehelper/browser, separate from any system browser) and a smoke test.
func browserCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "browser",
		Short: "Manage the headless browser used for screenshots/console capture",
	}
	c.AddCommand(browserInstallCmd(), browserTestCmd())
	return c
}

func browserInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Download the managed browser (~150MB, one time) so screenshot/console tools work",
		Long: "Downloads an isolated Chromium-for-Testing into ~/.codehelper/browser. It is a " +
			"separate binary run with a throwaway profile, so it never touches Chrome/Firefox or " +
			"any browser profile you already have. Idempotent — a second run is a no-op when present.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !web.BrowserAvailable() {
				return fmt.Errorf("%w — rebuild with `codehelper update` / install.sh (default -tags rod)", web.ErrBrowserUnavailable)
			}
			dir, _ := web.BrowserDir()
			fmt.Fprintf(os.Stderr, "browser: provisioning managed Chromium into %s …\n", dir)
			path, err := web.EnsureBrowser(cmd.Context())
			if err != nil {
				return fmt.Errorf("install browser: %w", err)
			}
			fmt.Fprintf(os.Stderr, "browser: ready at %s\n", path)
			// Also fetch axe-core so audit=full works offline (non-fatal).
			if axe, err := web.EnsureAxe(cmd.Context()); err != nil {
				fmt.Fprintln(os.Stderr, "browser: axe-core (for audit=full) not fetched:", err)
			} else {
				fmt.Fprintf(os.Stderr, "browser: axe-core ready at %s\n", axe)
			}
			fmt.Println(path)
			return nil
		},
	}
}

func browserTestCmd() *cobra.Command {
	var out, device, format, audit, actionsJSON, recipe, site, path, session, report, debugPackDir, uploadAllow string
	var fullPage, metrics, outline, snapshot, trace, waitHydrate, headed, sessionClear, previewActions, pauseOnFail bool
	var segmentHeight, clipY, clipHeight, slowMo, pauseOnFailMS int
	c := &cobra.Command{
		Use:   "test [url]",
		Short: "Capture a URL and write the screenshot to a file (smoke test)",
		Long: `Smoke-test the managed browser. Pass a URL, or use --recipe/--site with a
connections website profile (credentials from env:/secret — never flags).

  codehelper browser test http://127.0.0.1:3000 -o /tmp/shot.webp
  codehelper browser test --recipe wp_login --site local-wp --path ~/Projects/wordpress/_wp-test-site
  codehelper browser test http://127.0.0.1:3000 --headed --slow-mo 800 --pause-on-fail
  CODEHELPER_BROWSER_HEADED=1 codehelper browser test http://127.0.0.1:3000

Headed/GUI mode needs a graphical display. Over SSH/CI: omit --headed, or
  xvfb-run codehelper browser test --headed …
On action/assert failure a debug pack (screenshot + report.json with console,
failed network, outline/snapshot, URL, action log) is written under
~/.codehelper/browser/debug-packs/ (or --debug-pack-dir). A JSON report is also
written next to -o (or --report). Uploads are sandboxed to --path /
--upload-allow / CODEHELPER_BROWSER_UPLOAD_ALLOW.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !web.BrowserAvailable() {
				return fmt.Errorf("%w — rebuild with `codehelper update` / install.sh (default -tags rod)", web.ErrBrowserUnavailable)
			}
			url := ""
			if len(args) > 0 {
				url = args[0]
			}
			var actions []web.Action
			if recipe != "" || site != "" {
				u, acts, err := resolveCLISiteRecipe(path, recipe, site, session, sessionClear)
				if err != nil {
					return err
				}
				if url == "" {
					url = u
				}
				actions = append(actions, acts...)
			}
			if url == "" {
				return fmt.Errorf("url is required (or pass --recipe/--site with a connections website)")
			}
			if err := web.GuardURL(url, true); err != nil {
				return err
			}
			if segmentHeight > 0 {
				fullPage = true
			}
			if actionsJSON != "" {
				var extra []web.Action
				if err := json.Unmarshal([]byte(actionsJSON), &extra); err != nil {
					return fmt.Errorf("parse --actions JSON: %w", err)
				}
				actions = append(actions, extra...)
			}
			workspace, _ := filepath.Abs(path)
			var allowDirs []string
			if uploadAllow != "" {
				for _, d := range filepath.SplitList(uploadAllow) {
					d = strings.TrimSpace(d)
					if d != "" {
						allowDirs = append(allowDirs, d)
					}
				}
			}
			if !cmd.Flags().Changed("headed") && !cmd.Flags().Changed("gui") && web.HeadedFromEnv() {
				headed = true
			}
			res, err := web.CaptureBrowser(cmd.Context(), web.BrowserOptions{
				URL: url, Device: device, Format: web.NormalizeFormat(format),
				FullPage: fullPage, Metrics: metrics, AllowPrivate: true,
				SegmentPx: segmentHeight, ClipY: clipY, ClipHeight: clipHeight,
				Audit:     audit == "lite" || audit == "full",
				AuditFull: audit == "full",
				Outline:   outline, Snapshot: snapshot, Trace: trace, WaitHydrate: waitHydrate,
				Headed: headed, SlowMoMS: slowMo,
				PauseOnFail: pauseOnFail || web.PauseOnFailFromEnv(), PauseOnFailMS: pauseOnFailMS,
				PreviewActions: web.PreviewActionsAllowed(previewActions),
				Actions:        actions, Session: session, SessionClear: sessionClear,
				WorkspaceRoot: workspace, UploadAllowDirs: allowDirs,
				WriteDebugPack: true, DebugPackDir: debugPackDir,
			})
			if err != nil {
				return err
			}
			for _, a := range res.ActionLog {
				fmt.Fprintf(os.Stderr, "  %s\n", a)
			}
			if res.Snapshot != "" {
				fmt.Fprintf(os.Stderr, "snapshot:\n%s\n", res.Snapshot)
			}
			if len(res.Trace) > 0 {
				fmt.Fprintf(os.Stderr, "trace: %d events\n", len(res.Trace))
			}
			if len(res.A11y) > 0 {
				fmt.Fprintf(os.Stderr, "a11y: %d issues\n", len(res.A11y))
				for _, a := range res.A11y {
					fmt.Fprintf(os.Stderr, "  - %s [%s] x%d\n", a.Rule, a.Impact, a.Count)
				}
			}
			if res.Vitals != nil {
				fmt.Fprintf(os.Stderr, "vitals: LCP %dms · CLS %.3f · FCP %dms · TTFB %dms\n", res.Vitals.LCPms, res.Vitals.CLS, res.Vitals.FCPms, res.Vitals.TTFBms)
			}
			if len(res.Outline) > 0 {
				fmt.Fprintf(os.Stderr, "interactive elements: %d\n", len(res.Outline))
				for _, e := range res.Outline {
					ref := e.Ref
					if ref == "" {
						ref = "?"
					}
					fmt.Fprintf(os.Stderr, "  [%s] [%s] %s — %q\n", ref, e.Role, e.Selector, e.Name)
				}
			}
			if out == "" {
				out = filepath.Join(os.TempDir(), "codehelper-browser-test."+res.Format)
			}
			fmt.Fprintf(os.Stderr, "browser: [%s %s] %s — status %d, %d console, %d uncaught, %d failed reqs, %dms\n",
				res.Device, res.Viewport, res.FinalURL, res.DocStatus, len(res.Console), len(res.PageErrors), len(res.Failed), res.LoadMS)
			if res.Perf != nil {
				fmt.Fprintf(os.Stderr, "perf: FCP %dms · load %dms · %d req · %dKB\n", res.Perf.FCPms, res.Perf.LoadMs, res.Perf.Requests, res.Perf.TransferKB)
			}
			if res.FailurePack != nil && res.FailurePack.Failed {
				fmt.Fprintf(os.Stderr, "TEST: FAILED — debug pack: %s\n", firstNonEmptyCLI(res.DebugPackJSON, res.DebugPackDir))
			}
			// Split capture: write one file per piece (out-0, out-1, …).
			if len(res.Tiles) > 1 {
				ext := filepath.Ext(out)
				stem := out[:len(out)-len(ext)]
				for i, t := range res.Tiles {
					p := fmt.Sprintf("%s-%d%s", stem, i, ext)
					if err := os.WriteFile(p, t, 0o644); err != nil {
						return err
					}
				}
				fmt.Printf("screenshot: %s-{0..%d}%s (%s, %d pieces of %s)\n", stem, len(res.Tiles)-1, ext, res.Format, len(res.Tiles), res.PageDim)
				res.ScreenshotPath = fmt.Sprintf("%s-0%s", stem, ext)
			} else {
				if err := os.WriteFile(out, res.Image, 0o644); err != nil {
					return err
				}
				res.ScreenshotPath = out
				fmt.Printf("screenshot: %s (%s, %d bytes)\n", out, res.Format, len(res.Image))
			}
			reportPath := report
			if reportPath == "" {
				ext := filepath.Ext(out)
				stem := out[:len(out)-len(ext)]
				reportPath = stem + ".report.json"
			}
			if err := web.WriteCaptureReport(reportPath, res); err != nil {
				return fmt.Errorf("write report: %w", err)
			}
			fmt.Printf("report: %s\n", reportPath)
			if res.DebugPackJSON != "" {
				fmt.Printf("debug_pack: %s\n", res.DebugPackJSON)
			}
			if web.ActionsFailed(res.ActionLog) {
				return fmt.Errorf("browser actions failed — see report %s", reportPath)
			}
			return nil
		},
	}
	c.Flags().StringVarP(&out, "out", "o", "", "output path (default: temp dir)")
	c.Flags().StringVar(&report, "report", "", "JSON report path (default: <out>.report.json)")
	c.Flags().StringVar(&debugPackDir, "debug-pack-dir", "", "directory for failure debug pack (screenshot + report.json)")
	c.Flags().StringVar(&uploadAllow, "upload-allow", "", "extra upload sandbox roots (os path list separator)")
	c.Flags().StringVar(&device, "device", "desktop", "viewport preset: desktop|tablet|mobile")
	c.Flags().StringVar(&format, "format", "webp", "screenshot format: webp|png|jpeg")
	c.Flags().BoolVar(&fullPage, "full-page", false, "capture the full scrollable page")
	c.Flags().BoolVar(&metrics, "metrics", false, "collect performance metrics")
	c.Flags().StringVar(&audit, "audit", "", "accessibility + web vitals audit: lite | full")
	c.Flags().IntVar(&segmentHeight, "segment-height", 0, "split full page into vertical pieces of this many CSS px")
	c.Flags().IntVar(&clipY, "clip-y", 0, "capture from this Y offset (CSS px)")
	c.Flags().IntVar(&clipHeight, "clip-height", 0, "height of the clipped region (CSS px)")
	c.Flags().BoolVar(&outline, "outline", false, "list interactive elements + ready-to-use selectors")
	c.Flags().BoolVar(&snapshot, "snapshot", false, "bounded ARIA/role snapshot")
	c.Flags().BoolVar(&trace, "trace", false, "compact action/timing debug trail")
	c.Flags().BoolVar(&waitHydrate, "wait-hydrate", false, "wait for network idle + DOM stable after load")
	c.Flags().BoolVar(&headed, "headed", false, "run a visible browser and highlight each action (needs a display; env CODEHELPER_BROWSER_HEADED=1)")
	c.Flags().BoolVar(&headed, "gui", false, "alias for --headed")
	c.Flags().IntVar(&slowMo, "slow-mo", 0, "headed: delay ms before each action (default ~650)")
	c.Flags().BoolVar(&pauseOnFail, "pause-on-fail", false, "headed: keep window open briefly after a failed step (env CODEHELPER_BROWSER_PAUSE_ON_FAIL=1)")
	c.Flags().IntVar(&pauseOnFailMS, "pause-on-fail-ms", 0, "headed pause-on-fail duration ms (default 3000)")
	c.Flags().BoolVar(&previewActions, "preview-actions", false, "screenshot after each action (requires ch config browser set --action-previews on)")
	c.Flags().StringVar(&actionsJSON, "actions", "", `JSON array of interaction steps run before capture, e.g. '[{"action":"fill","selector":"#email","text":"a@b.com"},{"action":"click","selector":"#submit"},{"action":"assert","selector":".ok","text":"Thanks"}]'`)
	c.Flags().StringVar(&recipe, "recipe", "", "named recipe: wp_login | wp_admin | wp_plugins | wp_posts | wp_new_post (requires --site)")
	c.Flags().StringVar(&site, "site", "", "connections website profile name")
	c.Flags().StringVar(&path, "path", ".", "repo path for connections/secrets + upload sandbox workspace root")
	c.Flags().StringVar(&session, "session", "", "reuse cookies across captures in this process (named jar; also persisted under ~/.codehelper/browser/sessions/)")
	c.Flags().BoolVar(&sessionClear, "session-clear", false, "clear the named --session cookie jar before capture")
	return c
}

func firstNonEmptyCLI(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func resolveCLISiteRecipe(repoPath, recipe, siteName, session string, sessionClear bool) (string, []web.Action, error) {
	if recipe == "" {
		recipe = web.RecipeWPLogin
	}
	if strings.TrimSpace(siteName) == "" {
		return "", nil, fmt.Errorf("--recipe requires --site=<connections website name>")
	}
	repoRoot, err := connectionsRepoRoot([]string{repoPath})
	if err != nil {
		return "", nil, err
	}
	cfg, err := connections.Load(repoRoot)
	if err != nil {
		return "", nil, err
	}
	site := cfg.FindWebSite(siteName)
	if site == nil {
		return "", nil, fmt.Errorf("no website profile %q — add with `codehelper connections add-site`", siteName)
	}
	pass, err := ops.ResolveRef(repoRoot, site.PasswordRef, site.Name)
	if err != nil {
		return "", nil, fmt.Errorf("resolve site password: %w", err)
	}
	if strings.TrimSpace(site.User) == "" || pass == "" {
		return "", nil, fmt.Errorf("website %q needs user + password_ref", siteName)
	}
	skipLogin := !sessionClear && web.SessionHasCookies(session)
	acts, err := web.ExpandRecipeOptions(recipe, site.User, pass, strings.TrimRight(strings.TrimSpace(site.BaseURL), "/"), skipLogin)
	if err != nil {
		return "", nil, err
	}
	var url string
	switch strings.ToLower(strings.TrimSpace(recipe)) {
	case web.RecipeWPAdmin:
		url, err = site.AdminURL()
	case web.RecipeWPPlugins, "wordpress_plugins", "wp-plugins":
		url, err = site.PathURL("/wp-admin/plugins.php")
	case web.RecipeWPPosts, "wordpress_posts", "wp-posts":
		url, err = site.PathURL("/wp-admin/edit.php")
	case web.RecipeWPNewPost, "wordpress_new_post", "wp-new-post":
		url, err = site.PathURL("/wp-admin/post-new.php")
	default:
		if skipLogin {
			url, err = site.AdminURL()
		} else {
			url, err = site.LoginURL()
		}
	}
	return url, acts, err
}
