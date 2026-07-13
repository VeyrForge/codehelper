package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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
				return fmt.Errorf("%w", web.ErrBrowserUnavailable)
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
	var out, device, format, audit, actionsJSON string
	var fullPage, metrics, outline, headed bool
	var segmentHeight, clipY, clipHeight, slowMo int
	c := &cobra.Command{
		Use:   "test <url>",
		Short: "Capture a URL and write the screenshot to a file (smoke test)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !web.BrowserAvailable() {
				return fmt.Errorf("%w", web.ErrBrowserUnavailable)
			}
			url := args[0]
			if err := web.GuardURL(url, true); err != nil {
				return err
			}
			if segmentHeight > 0 {
				fullPage = true
			}
			var actions []web.Action
			if actionsJSON != "" {
				if err := json.Unmarshal([]byte(actionsJSON), &actions); err != nil {
					return fmt.Errorf("parse --actions JSON: %w", err)
				}
			}
			res, err := web.CaptureBrowser(cmd.Context(), web.BrowserOptions{
				URL: url, Device: device, Format: web.NormalizeFormat(format),
				FullPage: fullPage, Metrics: metrics, AllowPrivate: true,
				SegmentPx: segmentHeight, ClipY: clipY, ClipHeight: clipHeight,
				Audit:     audit == "lite" || audit == "full",
				AuditFull: audit == "full",
				Outline:   outline, Headed: headed, SlowMoMS: slowMo,
				Actions: actions,
			})
			if err != nil {
				return err
			}
			for _, a := range res.ActionLog {
				fmt.Fprintf(os.Stderr, "  %s\n", a)
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
					fmt.Fprintf(os.Stderr, "  [%s] %s — %q\n", e.Role, e.Selector, e.Name)
				}
			}
			if out == "" {
				out = filepath.Join(os.TempDir(), "codehelper-browser-test."+res.Format)
			}
			fmt.Fprintf(os.Stderr, "browser: [%s %s] %s — status %d, %d console, %d errors, %d failed reqs, %dms\n",
				res.Device, res.Viewport, res.FinalURL, res.DocStatus, len(res.Console), len(res.PageErrors), len(res.Failed), res.LoadMS)
			if res.Perf != nil {
				fmt.Fprintf(os.Stderr, "perf: FCP %dms · load %dms · %d req · %dKB\n", res.Perf.FCPms, res.Perf.LoadMs, res.Perf.Requests, res.Perf.TransferKB)
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
				return nil
			}
			if err := os.WriteFile(out, res.Image, 0o644); err != nil {
				return err
			}
			fmt.Printf("screenshot: %s (%s, %d bytes)\n", out, res.Format, len(res.Image))
			return nil
		},
	}
	c.Flags().StringVarP(&out, "out", "o", "", "output path (default: temp dir)")
	c.Flags().StringVar(&device, "device", "desktop", "viewport preset: desktop|tablet|mobile")
	c.Flags().StringVar(&format, "format", "webp", "screenshot format: webp|png|jpeg")
	c.Flags().BoolVar(&fullPage, "full-page", false, "capture the full scrollable page")
	c.Flags().BoolVar(&metrics, "metrics", false, "collect performance metrics")
	c.Flags().StringVar(&audit, "audit", "", "accessibility + web vitals audit: lite | full")
	c.Flags().IntVar(&segmentHeight, "segment-height", 0, "split full page into vertical pieces of this many CSS px")
	c.Flags().IntVar(&clipY, "clip-y", 0, "capture from this Y offset (CSS px)")
	c.Flags().IntVar(&clipHeight, "clip-height", 0, "height of the clipped region (CSS px)")
	c.Flags().BoolVar(&outline, "outline", false, "list interactive elements + ready-to-use selectors")
	c.Flags().BoolVar(&headed, "headed", false, "run a visible browser and highlight each action (needs a display)")
	c.Flags().IntVar(&slowMo, "slow-mo", 0, "headed: delay ms before each action (default ~650)")
	c.Flags().StringVar(&actionsJSON, "actions", "", `JSON array of interaction steps run before capture, e.g. '[{"action":"fill","selector":"#email","text":"a@b.com"},{"action":"click","selector":"#submit"},{"action":"assert","selector":".ok","text":"Thanks"}]'`)
	return c
}
