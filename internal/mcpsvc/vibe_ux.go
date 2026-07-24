package mcpsvc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/security"
)

// vibe UX helpers for messy junior prompts: short what_next, copy-pasteable
// next_queries, setup-tax suppression, and clear framework-footgun labels.

// isSimpleVibeAsk reports tasks that should not drown in browser/CMS setup tax
// or heavy decision scaffolding (health/security/perf vibe prompts).
func isSimpleVibeAsk(task string) bool {
	t := strings.ToLower(strings.TrimSpace(task))
	if t == "" {
		return false
	}
	if isEndpointFeatureTask(t) {
		return true
	}
	if isVagueSecurityQuery(t) || hasSecurityIntent(t) {
		return true
	}
	if isVaguePerfQuery(t) || hasPerfIntent(t) {
		return true
	}
	// Tiny feature asks juniors type when they barely know the repo.
	simple := []string{
		"real quick", "quick add", "tiny ", "just add", "idk ", "feels ",
		"somehow", "whatever", "help me", "where do i", "how do i add",
	}
	for _, s := range simple {
		if strings.Contains(t, s) {
			return true
		}
	}
	return false
}

// shouldSuppressSetupTax drops browser/CMS connection suggestions that derail
// agents away from the actual code ask (vibe P1).
func shouldSuppressSetupTax(task, role string) bool {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "security" || role == "performance" {
		return true
	}
	return isSimpleVibeAsk(task)
}

// defaultVibeSections returns a lighter kickoff section allowlist for simple
// feature vibe asks when the caller did not pass sections=. Keeps orient+reuse+
// docs+steps (+findings when present) and drops decision/consideration bulk.
// Returns nil when the full payload should stay (security/perf/architect/complex).
func defaultVibeSections(task, role string) map[string]bool {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "security" || role == "performance" || role == "architect" {
		return nil
	}
	if !isSimpleVibeAsk(task) {
		return nil
	}
	return map[string]bool{
		"orient": true, "reuse": true, "docs": true, "steps": true, "findings": true,
	}
}

// vibeNextQueries returns exactly three copy-pasteable follow-up tool calls for
// vague prompts when findings are thin or abstained. repoRoot (optional) makes
// security follow-ups stack-aware — Redis/C must not get ValidationPipe/APP_DEBUG.
func vibeNextQueries(role string, shape security.ProjectShape, abstain bool, task, repoRoot string) []string {
	role = strings.ToLower(strings.TrimSpace(role))
	eco := detectEcosystem(repoRoot)
	switch role {
	case "security":
		if shape == security.ShapeLibrary || shape == security.ShapeFrameworkCore {
			return securityFrameworkNextQueries(eco)
		}
		if abstain || shape == security.ShapeSkeleton {
			return securitySkeletonNextQueries(eco)
		}
		return []string{
			"read_workspace_file on rank#1 finding file:line — confirm exploit path",
			"context on the sink symbol; impact if you plan to patch",
			"investigate recipe=security for the full ranked list",
		}
	case "performance":
		if shape == security.ShapeLibrary || shape == security.ShapeFrameworkCore {
			return []string{
				"hotspots — read commits_scanned; prefer primary-language core paths",
				"query: hot path alloc complexity mutex lock contention",
				"context on top hotspot symbol before optimizing",
			}
		}
		return []string{
			"hotspots — check commits_scanned honesty; prefer app controllers/services",
			"query: N+1 query prefetch select_related eager load pagination",
			"context on top hotspot; then impact + test_impact before optimizing",
		}
	default:
		if isEndpointFeatureTask(task) {
			if abstain {
				return []string{
					"query: GET /health healthz readyz HealthController getHealth",
					"scout task=\"add GET /health\" — confirm findings_mode=abstain if non-HTTP",
					"Ask user: HTTP server surface, or in-process helper instead of inventing /health?",
				}
			}
			if shape == security.ShapeLibrary || shape == security.ShapeFrameworkCore {
				return []string{
					"change_kit target=<top reuse / placement_* / route_health*> — extend or add /health near router",
					"If framework core: prefer examples/ hello-world or Router.GET / @app.route / APIRouter — do not invent a second stack",
					"diagnostics → review_diff → verify → finish_check after the patch",
				}
			}
			return []string{
				"change_kit target=<top reuse / route_health*> — extend existing /health|/ready",
				"If no route: add handler next to existing router (do not invent a second stack)",
				"diagnostics → review_diff → verify → finish_check after the patch",
			}
		}
		return []string{
			"kickoff sections=orient,reuse with a concrete noun (auth, health, list)",
			"query with the symbol/path you care about; then context on top hit",
			"change_kit target=<exact name from query> before editing",
		}
	}
}

// detectEcosystem returns a coarse stack family for next_queries wording.
// Empty root → "unknown" (generic web follow-ups).
func detectEcosystem(repoRoot string) string {
	root := strings.TrimSpace(repoRoot)
	if root == "" {
		return "unknown"
	}
	base := strings.ToLower(filepath.Base(root))
	if base == "redis" || fileExists(filepath.Join(root, "src", "server.c")) ||
		fileExists(filepath.Join(root, "src", "acl.c")) {
		return "c"
	}
	if fileExists(filepath.Join(root, "artisan")) || fileExists(filepath.Join(root, "composer.json")) {
		return "php"
	}
	if fileExists(filepath.Join(root, "manage.py")) || fileExists(filepath.Join(root, "pyproject.toml")) ||
		fileExists(filepath.Join(root, "setup.py")) || dirExists(filepath.Join(root, "django")) ||
		dirExists(filepath.Join(root, "fastapi")) || dirExists(filepath.Join(root, "src", "flask")) {
		return "python"
	}
	if fileExists(filepath.Join(root, "go.mod")) {
		return "go"
	}
	if fileExists(filepath.Join(root, "Cargo.toml")) {
		return "rust"
	}
	if fileExists(filepath.Join(root, "pom.xml")) || fileExists(filepath.Join(root, "build.gradle")) {
		return "java"
	}
	if fileExists(filepath.Join(root, "Gemfile")) {
		return "ruby"
	}
	if fileExists(filepath.Join(root, "package.json")) {
		return "node"
	}
	// Many .c under src/ → C datastore/server core.
	if dirExists(filepath.Join(root, "src")) {
		n := 0
		_ = filepath.WalkDir(filepath.Join(root, "src"), func(path string, d os.DirEntry, err error) error {
			if err != nil || n > 25 {
				return filepath.SkipAll
			}
			if !d.IsDir() && strings.EqualFold(filepath.Ext(path), ".c") {
				n++
			}
			return nil
		})
		if n > 15 {
			return "c"
		}
	}
	return "unknown"
}

func securityFrameworkNextQueries(eco string) []string {
	switch eco {
	case "c":
		return []string{
			"query: ACL protected-mode requirepass bind auth",
			"query: ACLAuthenticateUser checkPasswordBasedAuth NOPASS",
			"investigate recipe=security — treat library_guidance as footguns, not app CVEs",
		}
	case "python":
		return []string{
			"query: csrf_exempt mark_safe RawSQL extra dangerous",
			"query: SECRET_KEY DEBUG ALLOWED_HOSTS password hash",
			"investigate recipe=security — treat library_guidance as footguns, not app CVEs",
		}
	case "php":
		return []string{
			"query: APP_DEBUG APP_KEY mass assignment Form Request CSRF",
			"query: Blade {!! raw DB::raw unescaped",
			"investigate recipe=security — treat library_guidance as footguns, not app CVEs",
		}
	case "go":
		return []string{
			"query: auth middleware bearer token session cookie TLS",
			"query: sql.Open Exec QueryContext string concat secret",
			"investigate recipe=security — treat library_guidance as footguns, not app CVEs",
		}
	case "java":
		return []string{
			"query: permitAll csrf disable SecurityFilterChain actuator",
			"query: PreparedStatement createQuery nativeQuery secret",
			"investigate recipe=security — treat library_guidance as footguns, not app CVEs",
		}
	case "ruby":
		return []string{
			"query: skip_before_action authenticate html_safe raw SQL",
			"query: secret_key_base force_ssl protect_from_forgery",
			"investigate recipe=security — treat library_guidance as footguns, not app CVEs",
		}
	case "rust":
		return []string{
			"query: auth middleware tower layer jwt cookie csrf",
			"query: unsafe transmute secret env password",
			"investigate recipe=security — treat library_guidance as footguns, not app CVEs",
		}
	default: // node / unknown web frameworks
		return []string{
			"query: auth middleware bearer token CSRF CORS helmet trust boundary",
			"query: secret password hash compare helmet cors origin",
			"investigate recipe=security — treat library_guidance as footguns, not app CVEs",
		}
	}
}

func securitySkeletonNextQueries(eco string) []string {
	switch eco {
	case "c":
		return []string{
			"query: ACL protected-mode requirepass bind",
			"query: auth ACLAuthenticateUser securityWarningCommand",
			"context on top ACL/auth symbol from query; then investigate recipe=security",
		}
	case "php":
		return []string{
			"query: APP_DEBUG APP_KEY auth middleware sanctum csrf",
			"query: secrets env password hash compare",
			"context on top auth/session symbol from query; then investigate recipe=security",
		}
	case "python":
		return []string{
			"query: csrf_exempt auth login session middleware",
			"query: SECRET_KEY DEBUG password hash compare",
			"context on top auth/session symbol from query; then investigate recipe=security",
		}
	default:
		return []string{
			"query: auth session token bearer middleware Authorize AllowAnonymous",
			"query: secrets secret store api_key password hash compare",
			"context on top auth/session symbol from query; then investigate recipe=security",
		}
	}
}

// buildWhatNext returns one short junior-facing sentence: what to do next.
func buildWhatNext(role string, top *reuseCandidate, findings []auditFinding, abstain string, next []string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	if abstain != "" && len(findings) == 0 {
		// Role-aware abstain: feature/perf must NOT reuse security "exploit path" copy
		// (LIVE redis health kickoff used to tell agents there was no CVE to find).
		prefix := abstainWhatNextPrefix(role, abstain)
		if len(next) > 0 {
			return prefix + " Paste next: `" + next[0] + "`"
		}
		return prefix + " Refine with a concrete module (auth, session, secrets, /health)."
	}
	switch role {
	case "security":
		if len(findings) > 0 {
			f := findings[0]
			if securityFindingsAreGuidanceOnly(findings) {
				return fmt.Sprintf(
					"Guidance only (not a CVE): review `%s:%d` (%s). Then hunt app sinks — paste: `%s`",
					f.File, f.Line, f.Rule, firstOr(next, "investigate recipe=security"),
				)
			}
			label := "Inspect"
			if strings.EqualFold(f.Kind, "library_guidance") || strings.EqualFold(f.Kind, "config_hardening") {
				label = "Review footgun (not a confirmed CVE)"
			}
			return fmt.Sprintf("%s `%s:%d` (%s) → then `%s`",
				label, f.File, f.Line, f.Rule, firstOr(next, "investigate recipe=security"))
		}
	case "performance":
		if len(findings) > 0 {
			f := findings[0]
			why := strings.TrimSpace(f.Hint)
			if why == "" {
				why = "likely hot path / churn × centrality"
			}
			return fmt.Sprintf("Hotspot `%s:%d` — %s. Next: `%s`",
				f.File, f.Line, truncateRunes(why, 100), firstOr(next, "hotspots"))
		}
		return "Call `hotspots` first; then `context` on the top primary-language file."
	}
	if top != nil && top.Name != "" {
		pathHint := pathOnlyFromLoc(top.Loc)
		if pathHint != "" {
			return fmt.Sprintf(
				"Extend `%s` at %s via `change_kit target=%s` (or `context name=%s path=%s`) — do not invent a parallel path.",
				top.Name, top.Loc, top.Name, top.Name, pathHint)
		}
		return fmt.Sprintf("Extend `%s` at %s via `change_kit target=%s` — do not invent a parallel path.",
			top.Name, top.Loc, top.Name)
	}
	if len(next) > 0 {
		return "Next: `" + next[0] + "`"
	}
	return "Call `query` with a concrete symbol, then `context` / `change_kit` before editing."
}

// abstainWhatNextPrefix picks junior-facing ABSTAIN wording by role.
// Security keeps exploit-path language; feature/perf must not pretend a CVE hunt failed.
func abstainWhatNextPrefix(role, abstain string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	a := strings.TrimSpace(abstain)
	al := strings.ToLower(a)
	switch role {
	case "security":
		// Always keep the junior-friendly exploit-path framing; append a short
		// concrete reason when the caller already passed one.
		prefix := "ABSTAIN — no confirmed app exploit path found (that is OK, not empty). Labeled footguns ≠ CVEs."
		if a != "" && !strings.Contains(strings.ToLower(prefix), strings.TrimSpace(strings.TrimPrefix(al, "abstain:"))) {
			extra := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(a, "ABSTAIN:"), "ABSTAIN —"))
			extra = strings.TrimSpace(strings.TrimPrefix(extra, "ABSTAIN"))
			if extra != "" && !strings.Contains(strings.ToLower(prefix), strings.ToLower(extra)) {
				return prefix + " " + strings.TrimRight(extra, ". ") + "."
			}
		}
		return prefix
	case "performance":
		if strings.HasPrefix(strings.ToUpper(a), "ABSTAIN") {
			return strings.TrimRight(a, ". ") + "."
		}
		return "ABSTAIN — no actionable hotspot ranked yet (that is OK, not empty)."
	default:
		// feature / architect / other — prefer the concrete abstain (HTTP health, etc.)
		if strings.HasPrefix(strings.ToUpper(a), "ABSTAIN") {
			return strings.TrimRight(a, ". ") + "."
		}
		if a != "" {
			return "ABSTAIN — " + strings.TrimRight(a, ". ") + "."
		}
		return "ABSTAIN — nothing grounded to extend yet (that is OK, not empty)."
	}
}

// vibeRecommendedTools returns cheap next MCP tools for vibe hubs (kickoff/investigate/plan).
func vibeRecommendedTools(role string, top *reuseCandidate, abstain bool) []string {
	role = strings.ToLower(strings.TrimSpace(role))
	switch role {
	case "security":
		if abstain {
			return []string{"query", "investigate", "context", "plan"}
		}
		// Prefer investigate/query over context on lexical reuse — LIVE-BROAD-V6:
		// reuse tops are often auth false-friends (getAuthor, KeepAlive, cmdToken);
		// grounded sinks live in findings / investigate.
		return []string{"investigate", "query", "context", "change_kit"}
	case "performance", "perf":
		return []string{"hotspots", "context", "impact", "test_impact"}
	default:
		if top != nil && top.Name != "" {
			return []string{"change_kit", "apply_patch_workspace_file", "diagnostics", "verify"}
		}
		if abstain {
			return []string{"query", "scout", "kickoff", "change_kit"}
		}
		return []string{"change_kit", "query", "context", "diagnostics"}
	}
}

func securityFindingsAreGuidanceOnly(findings []auditFinding) bool {
	if len(findings) == 0 {
		return false
	}
	for _, f := range findings {
		k := strings.ToLower(strings.TrimSpace(f.Kind))
		if k != "library_guidance" && k != "config_hardening" {
			return false
		}
	}
	return true
}

func firstOr(next []string, fallback string) string {
	if len(next) > 0 && strings.TrimSpace(next[0]) != "" {
		return next[0]
	}
	return fallback
}

// labelFrameworkFootguns prefixes library/config guidance so vibe coders do not
// treat framework footguns as confirmed app CVEs.
func labelFrameworkFootguns(findings []auditFinding, shape security.ProjectShape) []auditFinding {
	if len(findings) == 0 {
		return findings
	}
	for i := range findings {
		f := &findings[i]
		kind := strings.ToLower(f.Kind)
		switch kind {
		case "library_guidance":
			prefix := "FRAMEWORK FOOTGUN (not an app CVE): "
			if shape == security.ShapeApp {
				prefix = "FRAMEWORK-STYLE FOOTGUN (verify in THIS app — not a confirmed CVE): "
			}
			if !strings.HasPrefix(f.Message, "FRAMEWORK") {
				f.Message = prefix + strings.TrimSpace(f.Message)
			}
			if f.Hint == "" {
				f.Hint = "Trust-boundary / library footgun — confirm exploitability in this codebase before alarming."
			}
		case "config_hardening":
			if !strings.Contains(strings.ToLower(f.Message), "checklist") &&
				!strings.HasPrefix(f.Message, "CONFIG CHECKLIST") {
				f.Message = "CONFIG CHECKLIST (not a CVE): " + strings.TrimSpace(f.Message)
			}
			if f.Hint == "" {
				f.Hint = "Bootstrap/config hardening — grounded file:line, not an exploit proof."
			}
		}
	}
	return findings
}

// enrichPerfFindingsWhy ensures each performance finding has a one-sentence why,
// one actionable rewrite suggestion when possible, and a concrete next tool.
func enrichPerfFindingsWhy(findings []auditFinding, shape security.ProjectShape) []auditFinding {
	if len(findings) == 0 {
		return findings
	}
	nextTool := "hotspots"
	whyDefault := "High churn × inbound call weight — changes here are costly and often slow under load."
	switch shape {
	case security.ShapeLibrary, security.ShapeFrameworkCore:
		nextTool = "query: hot path alloc complexity " + filepathBaseHint(findings[0].File)
		whyDefault = "Library/framework hot path — alloc/complexity here affects every consumer."
	case security.ShapeSkeleton:
		nextTool = "hotspots"
		whyDefault = "Skeleton sample path — use as a pattern, not a measured production hotspot."
	default:
		nextTool = "context on " + filepathBaseHint(findings[0].File) + "; then hotspots"
		whyDefault = "App controller/service path — likely list/detail or request hot path under load."
	}
	for i := range findings {
		f := &findings[i]
		if strings.TrimSpace(f.Hint) == "" {
			f.Hint = whyDefault
		}
		rewrite := suggestHotspotRewrite(f.File, shape)
		if rewrite != "" && !strings.Contains(strings.ToLower(f.Hint), "rewrite:") {
			f.Hint = strings.TrimSpace(f.Hint) + " Rewrite: " + rewrite
		}
		if !strings.Contains(strings.ToLower(f.Hint), "next tool:") &&
			!strings.Contains(strings.ToLower(f.Hint), "next:") {
			f.Hint = strings.TrimSpace(f.Hint) + " Next tool: `" + nextTool + "`."
		}
	}
	return findings
}

func filepathBaseHint(file string) string {
	file = strings.ReplaceAll(file, "\\", "/")
	if i := strings.LastIndex(file, "/"); i >= 0 {
		file = file[i+1:]
	}
	if i := strings.LastIndex(file, "."); i > 0 {
		file = file[:i]
	}
	return file
}

// isPlacementTarget reports synthetic HTTP placement seeds from kickoff/scout
// (placement_api_route, placement_url_resolver, …) — not real indexed symbols.
func isPlacementTarget(target string) bool {
	t := strings.ToLower(strings.TrimSpace(target))
	t = strings.TrimPrefix(t, "sym:")
	return strings.HasPrefix(t, "placement_")
}

// isHealthishTarget reports change_kit targets that mean HTTP health/ready.
func isHealthishTarget(target string) bool {
	t := strings.ToLower(strings.TrimSpace(target))
	t = strings.TrimPrefix(t, "sym:")
	if isPlacementTarget(t) {
		return true
	}
	switch t {
	case "health", "ready", "healthz", "readyz", "livez", "gethealth", "getready",
		"route_health", "route_ready", "route_healthz", "route_readyz", "healthcheck",
		"/health", "/ready", "/healthz", "/readyz", "/livez":
		return true
	}
	return strings.Contains(t, "health") && !strings.Contains(t, "unhealthy") &&
		!strings.Contains(t, "cluster")
}

// isBareHealthishTarget is a vibe typing of "health"/"ready"/paths — not a
// concrete symbol like getHealth / HealthController that should resolve from
// the graph when present.
func isBareHealthishTarget(target string) bool {
	t := strings.ToLower(strings.TrimSpace(target))
	t = strings.TrimPrefix(t, "sym:")
	switch t {
	case "health", "ready", "healthz", "readyz", "livez", "healthcheck",
		"/health", "/ready", "/healthz", "/readyz", "/livez",
		"route_health", "route_ready", "route_healthz", "route_readyz":
		return true
	}
	return false
}

// pathOnlyFromLoc strips a trailing :line from Loc (file:line → file).
func pathOnlyFromLoc(loc string) string {
	loc = strings.TrimSpace(loc)
	if loc == "" {
		return ""
	}
	if i := strings.LastIndex(loc, ":"); i > 0 {
		rest := loc[i+1:]
		allDigits := len(rest) > 0
		for _, r := range rest {
			if r < '0' || r > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return loc[:i]
		}
	}
	return loc
}

// featureEndpointNextQueries appends copy-paste follow-ups onto health abstains.
func featureEndpointNextQueries(task string, abstain bool) []string {
	return vibeNextQueries("feature", security.ShapeApp, abstain, task, "")
}
