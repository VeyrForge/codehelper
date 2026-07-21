# codehelper-browser

When to use:
- Any UI / frontend / CMS admin change that must be proven in a real page.
- Vibe or programmer loops: implement → browser-test → debug → retest.
- WordPress / Laravel / Django / Drupal / Magento / SPA admin or app flows.

Inputs needed:
- Running URL (dev server, `http://127.0.0.1:…`, SSH tunnel to loopback, or configured `site=`).
- What "done" looks like (selector, visible text, no console/uncaught errors).
- For authenticated admin: connections `site=` profile (password via secret/env — never paste).

**Propose setup before the first browser run**

1. Read `setup_suggestions` from `project_context` or `kickoff` orient.
2. Propose incomplete steps to the user (local URL, `connections add-site`, credentials location, headed mode, SSH tunnel if remote).
3. Wait for confirmation / overrides. Persist with:
   - `codehelper connections add-site --name … --url … --kind … --user … --password-ref secret`
   - `codehelper config project --browser-site … --browser-base-url … --browser-recipe … [--browser-headed on] [--browser-allow-private on] [--test-credentials-note …]`
4. Only then call `browser`.

Default tool sequence (implement → test → debug → retest):

1. Orient + implement (smallest safe change)
   - `kickoff` / `query` → `context` → `change_kit` → `apply_patch_workspace_file`
   - Start the app if needed (host shell); do not invent a green gate without a live URL.
2. Discover targets (once per unfamiliar page)
   - `browser` `url=…` `outline=true` and/or `snapshot=true` — bounded interactive map + ARIA snapshot.
   - Prefer `role`/`name`/`testid`/`ref:eN` (or `selector=testid:…` / `role:button:Name` / `ref:e3`) over brittle CSS.
   - Never dump raw DOM via other tools.
3. Browser-test (proof of the change)
   - Prefer one call: `browser` `url=…` `wait_hydrate=true` `actions=[…, {"action":"assert"|"assert_text", …}]`
   - Or named recipe + `site=`: `wp_login` / `laravel_login` / `django_admin` / `drupal_login` / `magento_login` / `spa_hydrate`
   - Always read the report: diagnostics (console · uncaught · failed requests) + **failure debug pack** (screenshot path, outline/snapshot, URL, action log) on assert fail.
   - Uploads are sandboxed to workspace / `upload_allow` / `CODEHELPER_BROWSER_UPLOAD_ALLOW` (multi-file: `path1||path2`).
   - CLI: `codehelper browser test … -o shot.webp` also writes `shot.report.json` (+ debug pack dir on fail).
   - Optional `trace=true` for a compact timing trail when debugging flakes.
   - **Watch the agent (GUI):** when the user asks to watch or a flake needs eyes — `headed=true` / `gui=true` (env `CODEHELPER_BROWSER_HEADED=1` or project `browser_headed`). Optional `slow_mo` + `pause_on_fail`. Needs a display; over SSH/CI stay headless or `xvfb-run`.
   - HTTP-only checks stay on `web` (status/JSON); client-side JS needs `browser`.
4. Debug on failure
   - Failed assert / wrong screen → re-`outline`/`snapshot` (drive via `ref:eN`), tighten locators, or fix the bug.
   - Console / uncaught / failed requests → `query` those phrases → `context` → patch.
   - Flaky wait → `wait_hydrate` / `wait_idle` / raise `ms`, or `session=` for logged-in flows.
5. Retest → gate
   - Re-run the same `browser` assert (or CMS `recipe=`).
   - Then `diagnostics` → `review_diff` → `verify` → `finish_check`.
   - Claim done ONLY when `finish_check.can_claim_done=true` **and** the browser assert passed.
   - UI changes without a browser/web assert are incomplete.

Framework / CMS recipes:

| Stack | Pattern |
|---|---|
| Any SPA / static | `recipe=spa_hydrate` or `wait_hydrate` → `outline`/`snapshot` → `fill`/`click`/`select` → `assert_text`; optional `devices=["all"]`, `metrics=true`, `audit=lite` |
| WordPress admin | `recipe=wp_login\|wp_admin\|wp_plugins\|wp_posts\|wp_new_post` + `site=<profile>` + `allow_private` if LAN; hydration landmark `#wpadminbar` |
| Laravel | `recipe=laravel_login` + `site=` (kind=laravel); login `/login` email+password |
| Django admin | `recipe=django_admin` + `site=` (kind=django); landmark `#user-tools` |
| Drupal | `recipe=drupal_login` + `site=` (kind=drupal); landmark `#toolbar-administration` |
| Magento 2 | `recipe=magento_login` + `site=` (kind=magento); `#username` / `#login` |
| Multi-turn admin | `session=<name>` (disk jar under `~/.codehelper/browser/sessions/`); `session_clear` / `clear_cookies` to reset |
| Remote via SSH | `ssh -N -L 8080:127.0.0.1:80 user@host` → `site` base_url=`http://127.0.0.1:8080` (loopback always passes GuardURL). LAN IPs need `allow_private=true` |
| Visual regression | `baseline="name"` after a known-good capture |
| Watch the agent (GUI) | `headed=true` / `gui=true` (+ `slow_mo`, `pause_on_fail`); needs display — else `xvfb-run` or headless |

Config keys (per-project `mcp-config.json` via `codehelper config project`):
- `browser_base_url`, `browser_site`, `browser_recipe`, `browser_headed`, `browser_allow_private`, `test_credentials_note`

Anti-patterns:
- Calling `browser` before proposing `setup_suggestions` when no `site=` / URL is configured.
- Claiming UI done from code/diff alone.
- Hand-rolling CMS login with plaintext passwords in `actions` when `site=` + secrets exist.
- Using `web` for client-rendered results.
- Re-running the same failing selector without `outline` or a code fix.
- Stopping after the first green screenshot without retesting the assert after the fix.

Setup (once per machine):
- `codehelper browser install` (managed Chromium; never touches your system browsers).
- Smoke: `codehelper browser test https://example.com`
- Rod-tagged binary on PATH (`codehelper doctor` / rebuild via install if stub).
- `codehelper init` / `codehelper doctor` print suggested `.mcp.json` + stack setup steps.

Example prompts:
- "Propose browser setup for this Laravel app, then prove the dashboard with recipe=laravel_login."
- "Use recipe=wp_plugins + site=local-wp, then fix any console errors and retest."
- "Tunnel staging via SSH to 127.0.0.1:8080 and smoke with spa_hydrate."
