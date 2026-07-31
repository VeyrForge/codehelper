package mcpsvc

import (
	"github.com/VeyrForge/codehelper/internal/product"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerProductModules wires product-gated MCP tool groups (edit / check /
// browser / ops). Core tools register in RegisterAll itself. Default builds
// enable every shipping module; selective -tags ch_modules builds omit some.
func registerProductModules(s *server.MCPServer, reg *registry.Registry) {
	if product.CheckEnabled() {
		RegisterReviewTools(s, reg)
	}
	if product.EditEnabled() {
		RegisterSymbolEditTools(s, reg)
	}
	if product.OpsEnabled() {
		RegisterOpsTools(s, reg)
	}
	registerBrowserProduct(s, reg)
}

func registerBrowserProduct(s *server.MCPServer, reg *registry.Registry) {
	if !product.BrowserEnabled() {
		return
	}
	s.AddTool(mcp.NewTool("browser",
		mcp.WithDescription("BROWSER slot — render a URL in headless Chromium and SEE it: WebP screenshot + console/JS errors/failed requests (not `web`, which is HTTP-only). Prefer this for visual/client-side verification after UI changes. Write & run a UI test: outline=true lists interactive elements, then `actions` clicks/fills/asserts. WordPress admin: recipe=wp_login|wp_admin|… + site=<connections website>. Session reuse: session=<name>. Responsive: device=mobile|tablet|desktop or devices=[\"all\"]. Perf: metrics=true. Headed=true opens a visible browser. Lean by default — not a full-DOM dump. Loopback always allowed; allow_private for LAN. Needs managed browser (`ch browser install`); binary with -tags rod."),
		mcp.WithString("url", mcp.Description("URL to open (e.g. http://localhost:3000). Optional when site= is set — then the site login/admin URL is used.")),
		mcp.WithString("recipe", mcp.Description("Named interaction recipe prepended before actions: wp_login | wp_admin | wp_plugins | wp_posts | wp_new_post | laravel_login | django_admin | drupal_login | magento_login | spa_hydrate. Requires site=. When omitted with site=, uses site kind / project browser_recipe default.")),
		mcp.WithString("site", mcp.Description("Connections website profile name (codehelper connections add-site). Supplies base URL + user; password from env:/secret store only — never pass passwords in MCP args.")),
		mcp.WithString("repo", mcp.Description("Repository name for site/secret resolution (optional; defaults to current MCP workspace)")),
		mcp.WithString("session", mcp.Description("Named in-process cookie jar. Captures sharing the same session reuse auth cookies (e.g. wp_login then open plugins without re-login). Lives for the MCP server process lifetime.")),
		mcp.WithBoolean("session_clear", mcp.Description("Clear the named session cookie jar before this capture"), mcp.DefaultBool(false)),
		mcp.WithString("device", mcp.Description("Viewport preset: desktop (1280x800, default) | tablet (768x1024) | mobile (390x844). Sets size, pixel ratio, mobile emulation, and UA.")),
		mcp.WithArray("devices", mcp.Description("Capture several viewports in one call, e.g. [\"mobile\",\"desktop\"] or [\"all\"]. Overrides `device`. Returns one image per device.")),
		mcp.WithString("format", mcp.Description("Screenshot format: webp (default, smallest) | png | jpeg")),
		mcp.WithNumber("quality", mcp.Description("Compression quality 1-100 for webp/jpeg (default 80)"), mcp.DefaultNumber(0)),
		mcp.WithNumber("width", mcp.Description("Override viewport width px (else from device)"), mcp.DefaultNumber(0)),
		mcp.WithNumber("height", mcp.Description("Override viewport height px (else from device)"), mcp.DefaultNumber(0)),
		mcp.WithBoolean("full_page", mcp.Description("Capture the full scrollable page, not just the viewport"), mcp.DefaultBool(false)),
		mcp.WithBoolean("split", mcp.Description("Capture the full page split into vertical pieces (~2000px each), returned as multiple images at full resolution — read a long page without the downscaling a single tall screenshot suffers"), mcp.DefaultBool(false)),
		mcp.WithNumber("segment_height", mcp.Description("Max height (CSS px) per piece when splitting a full-page capture (implies split+full_page)"), mcp.DefaultNumber(0)),
		mcp.WithNumber("clip_y", mcp.Description("Capture only a region starting at this Y offset (CSS px); pair with clip_height"), mcp.DefaultNumber(0)),
		mcp.WithNumber("clip_height", mcp.Description("Height (CSS px) of the clipped region to capture at full width"), mcp.DefaultNumber(0)),
		mcp.WithBoolean("metrics", mcp.Description("Collect performance metrics: FCP, DOMContentLoaded, load, request count, transfer KB, JS heap"), mcp.DefaultBool(false)),
		mcp.WithString("audit", mcp.Description("Accessibility + Core Web Vitals audit. 'lite' = fast built-in checks (missing alt/labels/accessible-names, page lang/title); 'full' = the axe-core engine (comprehensive, with impact levels — needs `ch browser install`). Both also report LCP/CLS/FCP/TTFB with good/poor verdicts.")),
		mcp.WithBoolean("outline", mcp.Description("Return a compact map of the page's INTERACTIVE elements (inputs, buttons, links, form controls) — each with a stable ref (e1,e2,…), ready-to-use CSS selector, role, accessible name, input type, placeholder and value. Use this FIRST to discover targets; drive them with selector=ref:e3 or ref=\"e3\". Bounded (≤100 elements), not a full-DOM dump."), mcp.DefaultBool(false)),
		mcp.WithBoolean("snapshot", mcp.Description("Return a bounded ARIA/role snapshot (Playwright-MCP style: role \"name\" lines, ≤80 nodes). Prefer over dumping HTML. Use with role/name/testid locators in actions."), mcp.DefaultBool(false)),
		mcp.WithBoolean("trace", mcp.Description("Include a compact timing trail (navigate/action/wait/heal/fail) for debugging flaky flows — not a CDP file."), mcp.DefaultBool(false)),
		mcp.WithBoolean("wait_hydrate", mcp.Description("After load, wait for network idle + DOM stable (SPA/React/Vue/Next and WP admin hydration). Pair with wait_selector for a ready landmark (#root, #wpadminbar, …)."), mcp.DefaultBool(false)),
		mcp.WithArray("actions", mcp.Description(`Interaction + test steps before the screenshot. Locators: selector CSS, testid:/role:button:Name/text:/name:/ref:e3 prefixes, or fields role/name/testid/ref. Actions: click|type|fill|select|hover|press|scroll|wait|wait_idle|wait_hydrate|navigate|wait_nav|assert|assert_text|upload|snapshot|storage_set|storage_get|storage_clear|clear_cookies. Example: [{"action":"click","selector":"ref:e3"},{"action":"assert_text","selector":".ok","text":"Thanks"}]. Stops at first failure; failure_pack + screenshot always attached. Tip: outline/snapshot first; session= for login cookies.`)),
		mcp.WithBoolean("headed", mcp.Description("Run a VISIBLE browser (default headless) so a human can WATCH the agent drive the page: each action flashes a labelled box on its target element and SlowMotion paces the clicks/inputs. Needs a graphical display (skip over SSH/CI — or use xvfb-run). Alias: gui=true. Env CODEHELPER_BROWSER_HEADED=1 or project browser_headed sets the default."), mcp.DefaultBool(false)),
		mcp.WithBoolean("gui", mcp.Description("Alias for headed=true (visible Chromium)."), mcp.DefaultBool(false)),
		mcp.WithNumber("slow_mo", mcp.Description("Headed only: delay in ms before each action so clicks/inputs are perceptible (default ~650ms). Ignored in headless."), mcp.DefaultNumber(0)),
		mcp.WithBoolean("pause_on_fail", mcp.Description("Headed only: keep the window open ~3s after a failed step so a human can see the failure. Env CODEHELPER_BROWSER_PAUSE_ON_FAIL=1."), mcp.DefaultBool(false)),
		mcp.WithNumber("pause_on_fail_ms", mcp.Description("Headed + pause_on_fail: override pause duration in ms (default 3000)."), mcp.DefaultNumber(0)),
		mcp.WithBoolean("preview_actions", mcp.Description("Return a viewport screenshot after each interaction step (before the final capture). Requires `ch config browser set --action-previews on` (disabled by default). Failed steps always attach a shot even when this is off."), mcp.DefaultBool(false)),
		mcp.WithString("baseline", mcp.Description("Visual regression: name a baseline. First call saves the screenshot; later calls return a diff image (changed pixels in red) + % changed. Per-device baselines.")),
		mcp.WithBoolean("update_baseline", mcp.Description("Overwrite the named baseline with the current screenshot instead of diffing"), mcp.DefaultBool(false)),
		mcp.WithString("selector", mcp.Description("Screenshot only this CSS-selected element")),
		mcp.WithString("wait_selector", mcp.Description("Wait for this CSS selector to appear before capturing (also used as hydrate landmark when wait_hydrate=true)")),
		mcp.WithNumber("wait_ms", mcp.Description("Extra fixed wait after load, in milliseconds (with wait_hydrate: overall hydrate timeout)"), mcp.DefaultNumber(0)),
		mcp.WithNumber("timeout_sec", mcp.Description("Overall timeout seconds (default 30)"), mcp.DefaultNumber(0)),
		mcp.WithBoolean("allow_private", mcp.Description("Permit private/LAN (RFC1918) targets; loopback always allowed, cloud-metadata/link-local always blocked (default false)"), mcp.DefaultBool(false)),
		mcp.WithString("debug_pack_dir", mcp.Description("On action/assert failure, write a debug pack (failure screenshot + report.json with console errors, failed network, outline/snapshot, URL, action log) to this directory. Default: ~/.codehelper/browser/debug-packs/<timestamp>/.")),
		mcp.WithString("upload_allow", mcp.Description("Extra upload sandbox roots (os path-list separator). Upload paths must live under the workspace repo root and/or these dirs (also CODEHELPER_BROWSER_UPLOAD_ALLOW). Multi-file: text= path1||path2 or newlines.")),
		annotVerify(),
	), timedTool("browser", browserHandler(reg)))
}
