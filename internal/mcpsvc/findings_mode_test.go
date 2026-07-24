package mcpsvc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/internal/security"
	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestIsAuditNoiseSymbol(t *testing.T) {
	cases := []struct {
		name, loc string
		want      bool
	}{
		{"#search-owner-form", "src/main/resources/static/resources/css/petclinic.css:12", true},
		{".secrets-line", "web/static/app.css:3", true},
		{"resolveInjections", "packages/runtime-core/src/componentOptions.ts:40", true},
		{"sdscatlen_unsafe", "src/sds.c:100", true},
		{"Schema", "database/migrations/2024_01_01_create_users.php:10", true},
		{"mark_safe", "django/utils/safestring.py:65", false},
		{"Authenticate", "auth/middleware.go:20", false},
	}
	for _, tc := range cases {
		if got := isAuditNoiseSymbol(tc.name, tc.loc); got != tc.want {
			t.Errorf("isAuditNoiseSymbol(%q,%q)=%v want %v", tc.name, tc.loc, got, tc.want)
		}
	}
}

func TestFilterAuditCandidates(t *testing.T) {
	in := []reuseCandidate{
		{Name: "#search-owner-form", Loc: "app.css:1"},
		{Name: "OwnerController", Loc: "OwnerController.java:10"},
		{Name: "resolveInjections", Loc: "inject.ts:2"},
	}
	got := filterAuditCandidates(in, "security")
	if len(got) != 1 || got[0].Name != "OwnerController" {
		t.Fatalf("expected only OwnerController, got %+v", got)
	}
	// feature role keeps all
	if len(filterAuditCandidates(in, "feature")) != 3 {
		t.Fatal("feature role must not filter")
	}
}

func TestFilterAuditHits(t *testing.T) {
	hits := []retrieval.RankedSymbol{
		{Symbol: types.Symbol{Name: ".btn", Path: "a.css"}},
		{Symbol: types.Symbol{Name: "Login", Path: "auth.go"}},
	}
	got := filterAuditHits(hits, "performance")
	if len(got) != 1 || got[0].Symbol.Name != "Login" {
		t.Fatalf("got %+v", got)
	}
}

func TestIsVagueSecurityQuery(t *testing.T) {
	if !isVagueSecurityQuery("Is this secure?") {
		t.Fatal("expected vague")
	}
	if !isVagueSecurityQuery("any security issues?") {
		t.Fatal("expected vague")
	}
	if isVagueSecurityQuery("audit Auth/SessionStore for session fixation") {
		t.Fatal("precise prompt should not expand as vague-only")
	}
}

func TestPreferFeatureReuse(t *testing.T) {
	in := []reuseCandidate{
		{Name: ".list-unstyled", Loc: "petclinic.css:1"},
		{Name: "ByType", Loc: "gin.go:100"},
		{Name: "abort", Loc: "helpers.py:10"},
		{Name: "HealthController", Loc: "health_controller.rb:5"},
		{Name: "getHealth", Loc: "app.controller.ts:13"},
	}
	got := preferFeatureReuse(in, "Add a tiny GET /health endpoint")
	if len(got) == 0 {
		t.Fatal("expected candidates")
	}
	if got[0].Name != "HealthController" && got[0].Name != "getHealth" {
		t.Fatalf("expected health symbol first, got %+v", got[0])
	}
	for _, c := range got {
		if c.Name == ".list-unstyled" || c.Name == "ByType" || c.Name == "abort" {
			t.Fatalf("noise should be filtered: %+v", c)
		}
	}
}

func TestPreferSecurityReuse_AuthFalseFriends(t *testing.T) {
	in := []reuseCandidate{
		{Name: "getAuthor", Loc: "src/Entity/Post.php:133"},
		{Name: "impl_KeepAlive", Loc: "axum/src/response/sse.rs:523"},
		{Name: "keep_alive", Loc: "axum/src/response/sse.rs:75"},
		{Name: "IsDebugging", Loc: "debug.go:22"},
		{Name: "expected_token", Loc: "packages/svelte/src/compiler/errors.js:1165"},
		{Name: "cmdToken", Loc: "src/debug.c:1229"},
		{Name: "isDebugLog", Loc: "backend/internal/auth/handler.go:26"},
		{Name: "BasicAuth", Loc: "auth.go:72"},
		{Name: "GetSession", Loc: "backend/internal/auth/session_store.go:30"},
		{Name: "authenticate", Loc: "examples/auth/index.js:60"},
		{Name: "authorizationHeader", Loc: "auth.go:91"},
	}
	got := preferSecurityReuse(in, "fix auth bug — where does login/session/token middleware live?")
	if len(got) < 3 {
		t.Fatalf("expected reordered candidates, got %+v", got)
	}
	top := strings.ToLower(got[0].Name)
	if top != "basicauth" && top != "getsession" && top != "authenticate" && top != "authorizationheader" {
		t.Fatalf("expected real auth symbol first, got %+v", got[0])
	}
	for _, bad := range []string{"getAuthor", "impl_KeepAlive", "keep_alive", "IsDebugging", "expected_token", "cmdToken"} {
		if got[0].Name == bad {
			t.Fatalf("false friend %s must not rank first", bad)
		}
	}
	// All-noise → empty (framework cores with no auth symbols)
	noiseOnly := preferSecurityReuse([]reuseCandidate{
		{Name: "cmdToken", Loc: "src/debug.c:1"},
		{Name: "keep_alive", Loc: "sse.rs:1"},
	}, "where does login/session/token middleware live?")
	if noiseOnly != nil {
		t.Fatalf("all false-friends should clear reuse, got %+v", noiseOnly)
	}
}

func TestPreferFeatureReuse_DemotesAlreadyDoneAndSchema(t *testing.T) {
	in := []reuseCandidate{
		{Name: "already_done_comment", Loc: "scripts/notify_translations.py:377"},
		{Name: "Schema", Loc: "database/migrations/x.php:10"},
		{Name: "Controller", Loc: "app/Http/Controllers/Controller.php:1"},
		{Name: "route_get_9", Loc: "routes/web.php:9"},
	}
	got := preferFeatureReuse(in, "yo add health real quick")
	if len(got) == 0 {
		t.Fatal("expected candidates")
	}
	if got[0].Name != "route_get_9" && got[0].Name != "Controller" {
		// Schema/already_done must not win; route or controller OK
		t.Fatalf("expected route/controller first, got %+v", got)
	}
	for _, c := range got {
		if c.Name == "already_done_comment" || c.Name == "Schema" {
			t.Fatalf("noise should be filtered: %+v", c)
		}
	}
}

func TestSeedHealthRouteCandidates(t *testing.T) {
	dir := t.TempDir()
	routes := filepath.Join(dir, "routes")
	_ = os.MkdirAll(routes, 0o755)
	_ = os.WriteFile(filepath.Join(routes, "web.php"), []byte("<?php\nRoute::get('/health', function () {\n    return ['ok'=>true];\n});\n"), 0o644)
	in := []reuseCandidate{
		{Name: "Schema", Loc: "database/migrations/x.php:1"},
		{Name: "Controller", Loc: "app/Http/Controllers/Controller.php:1"},
	}
	got := seedHealthRouteCandidates(dir, preferFeatureReuse(in, "add GET /health"), "add GET /health")
	if len(got) == 0 || !strings.Contains(got[0].Loc, "routes/web.php") {
		t.Fatalf("expected seeded health route first, got %+v", got)
	}
}

func TestInferRoleFromTask_VibeWithoutRole(t *testing.T) {
	if got := inferRoleFromTask("", "this feels kinda insecure??"); got != "security" {
		t.Fatalf("got %q want security", got)
	}
	if got := inferRoleFromTask("feature", "why is this so slow / heavy — quick wins?"); got != "performance" {
		t.Fatalf("got %q want performance", got)
	}
	if got := inferRoleFromTask("", "add a tiny GET /health"); got != "feature" {
		t.Fatalf("got %q want feature", got)
	}
	if got := inferRoleFromTask("architect", "feels insecure"); got != "architect" {
		t.Fatalf("explicit role must win, got %q", got)
	}
}

func TestPreferPrimaryLanguageFiles_ForcesSrcC(t *testing.T) {
	rows := []hotspotRow{
		{File: "deps/lua/src/ltablib.c", Score: 0.99},
		{File: "utils/gen.py", Score: 0.9},
		{File: "src/networking.c", Score: 0.4},
		{File: "src/acl.c", Score: 0.3},
	}
	got := preferPrimaryLanguageFiles(rows, "c")
	if got[0].File != "src/networking.c" && got[0].File != "src/acl.c" {
		t.Fatalf("expected src/*.c first, got %+v", got)
	}
}

func TestApplyFindingsMode_SecurityAbstainOnClean(t *testing.T) {
	dir := t.TempDir()
	cands := []reuseCandidate{{Name: "#foo", Loc: "a.css:1"}}
	already := "old"
	note := "old"
	steps := []string{"old"}
	findings, mode, abstain := applyFindingsMode("security", dir, "", &cands, &already, &note, &steps)
	if mode != "security" {
		t.Fatalf("mode=%q", mode)
	}
	if abstain == "" && len(findings) == 0 {
		t.Fatal("expected abstain on clean tree with no findings")
	}
	if len(cands) != 0 {
		t.Fatalf("noise candidates should be filtered, got %+v", cands)
	}
	if already == "old" || note == "old" {
		t.Fatal("already/note should be rewritten for abstain")
	}
}

func TestApplyFindingsMode_SecurityConfigHardening(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".env.example"), []byte("APP_DEBUG=true\n"), 0o644)
	cands := []reuseCandidate{}
	already := ""
	note := ""
	steps := []string{}
	findings, mode, abstain := applyFindingsMode("security", dir, "", &cands, &already, &note, &steps)
	if mode != "security" {
		t.Fatalf("mode=%q", mode)
	}
	if len(findings) == 0 {
		t.Fatal("expected config hardening findings")
	}
	if findings[0].Confidence == "" || findings[0].Rank == 0 {
		t.Fatalf("expected rank/confidence on findings: %+v", findings[0])
	}
	if findings[0].Kind != "config_hardening" && findings[0].Rule != "config-debug-enabled" {
		// either kind set or rule matches
		if findings[0].Rule != "config-debug-enabled" {
			t.Fatalf("unexpected finding %+v", findings[0])
		}
	}
	_ = abstain // may be set when only config
}

func TestApplyFindingsMode_PerfLibraryGuidance(t *testing.T) {
	dir := t.TempDir()
	express := filepath.Join(dir, "express")
	_ = os.MkdirAll(filepath.Join(express, "lib", "router"), 0o755)
	_ = os.WriteFile(filepath.Join(express, "lib", "router", "index.js"), []byte("module.exports={}"), 0o644)
	_ = os.WriteFile(filepath.Join(express, "package.json"), []byte(`{"name":"express","description":"web framework","main":"index.js"}`), 0o644)
	cands := []reuseCandidate{}
	already := ""
	note := ""
	steps := []string{}
	findings, mode, _ := applyFindingsMode("performance", express, "", &cands, &already, &note, &steps)
	if mode != "performance" {
		t.Fatalf("mode=%q", mode)
	}
	if len(findings) == 0 {
		t.Fatal("expected library perf guidance findings")
	}
	if !strings.Contains(note, "framework_core") && !strings.Contains(note, "library") {
		t.Fatalf("note should mention library/framework shape: %s", note)
	}
}

func TestPreferCoreHotPaths(t *testing.T) {
	rows := []hotspotRow{
		{File: "src/rand.c", Score: 0.9},
		{File: "src/server.c", Score: 0.5},
		{File: "src/acl.c", Score: 0.4},
	}
	got := preferCoreHotPaths(rows, "c", security.ShapeFrameworkCore)
	if got[0].File != "src/server.c" && got[0].File != "src/acl.c" {
		t.Fatalf("expected core file first, got %+v", got)
	}
	if got[len(got)-1].File != "src/rand.c" {
		t.Fatalf("expected rand.c demoted, got %+v", got)
	}
}

func TestPreferFeatureReuse_DemotesCSSForHealth(t *testing.T) {
	in := []reuseCandidate{
		{Name: ".hero", Loc: "static/app.css:1"},
		{Name: "index.html", Loc: "public/index.html:1"},
		{Name: "getHello", Loc: "app.controller.ts:5"},
	}
	got := preferFeatureReuse(in, "add a tiny GET /health")
	if len(got) == 0 || got[0].Name != "getHello" {
		t.Fatalf("expected getHello first, got %+v", got)
	}
}

func TestSeedHTTPPlacement_FlaskLayout(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "src", "flask", "sansio"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "src", "flask", "app.py"), []byte("class Flask:\n    pass\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "src", "flask", "sansio", "scaffold.py"), []byte(
		"# scaffold\ndef route(self, rule):\n    pass\n"), 0o644)
	got := seedHealthRouteCandidates(dir, nil, "add GET /health real quick")
	if len(got) == 0 {
		t.Fatal("expected HTTP placement seed for flask")
	}
	if !strings.HasPrefix(got[0].Name, "placement_") {
		t.Fatalf("expected placement_* seed, got %+v", got[0])
	}
	note := featureEndpointAbstainNote("add GET /health", security.ShapeFrameworkCore, dir, got)
	if note != "" {
		t.Fatalf("flask must not abstain, got %q", note)
	}
}

func TestFeatureEndpointAbstainOnLibrary(t *testing.T) {
	// Unknown library root (temp) → non-HTTP → abstain when no health route.
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "lib"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "lib", "core.go"), []byte("package lib\n"), 0o644)
	note := featureEndpointAbstainNote("Add GET /health", security.ShapeFrameworkCore, dir, nil)
	if note == "" || !strings.Contains(note, "ABSTAIN") {
		t.Fatalf("expected abstain note, got %q", note)
	}
	withRoute := []reuseCandidate{{Name: "HealthController", Loc: "health.rb:1"}}
	if featureEndpointAbstainNote("Add GET /health", security.ShapeFrameworkCore, dir, withRoute) != "" {
		t.Fatal("should not abstain when health route exists")
	}
}

func TestFeatureEndpointNoAbstainHTTPFramework(t *testing.T) {
	flask := t.TempDir()
	_ = os.MkdirAll(filepath.Join(flask, "src", "flask"), 0o755)
	_ = os.WriteFile(filepath.Join(flask, "src", "flask", "app.py"), []byte("class Flask:\n    pass\n"), 0o644)
	note := featureEndpointAbstainNote("add a tiny GET /health", security.ShapeFrameworkCore, flask, nil)
	if note != "" {
		t.Fatalf("HTTP flask layout must not abstain as non-HTTP, got %q", note)
	}

	redis := t.TempDir()
	src := filepath.Join(redis, "src")
	_ = os.MkdirAll(src, 0o755)
	_ = os.WriteFile(filepath.Join(src, "server.c"), []byte("int main(){}\n"), 0o644)
	for i := 0; i < 20; i++ {
		_ = os.WriteFile(filepath.Join(src, fmt.Sprintf("f%d.c", i)), []byte("int x;\n"), 0o644)
	}
	note2 := featureEndpointAbstainNote("add GET /health", security.ShapeFrameworkCore, redis, nil)
	if note2 == "" || !strings.Contains(note2, "ABSTAIN") {
		t.Fatalf("redis-like C core must abstain, got %q", note2)
	}
	if !strings.Contains(note2, "C datastore") && !strings.Contains(note2, "no HTTP") {
		t.Fatalf("expected clear non-HTTP reason, got %q", note2)
	}
}

func TestIsVagueSecurityQuery_JuniorAdversarial(t *testing.T) {
	if !isVagueSecurityQuery("Is there anything sketchy in how we handle passwords, sessions, or user input?") {
		t.Fatal("expected junior adversarial prompt to expand via pack")
	}
	if !isVagueSecurityQuery("idk this codebase feels kinda insecure?? what should I worry about") {
		t.Fatal("vibe insecure prompt should expand")
	}
	if !isVaguePerfQuery("it's slow / feels heavy — any quick wins?") {
		t.Fatal("vibe perf prompt should expand")
	}
}

func TestPreferHighSignalDropsPureLowConfidence(t *testing.T) {
	in := []security.ContextFinding{
		{Rule: "sql-string-concat", Severity: "low", Confidence: "low", Kind: "sink_candidate",
			File: "help.go", Line: 1, Evidence: "updated successfully for ${n} categories"},
		{Rule: "eval-usage", Severity: "low", Confidence: "low", Kind: "sink_candidate",
			File: "bench.go", Line: 2, Evidence: "qrels eval (%d queries)"},
	}
	got := preferHighSignalFindings(in, 10)
	if len(got) != 0 {
		t.Fatalf("pure low-confidence sink noise should abstain empty, got %+v", got)
	}
}

func TestPreferCoreHotPaths_AllSrcC(t *testing.T) {
	rows := []hotspotRow{
		{File: "deps/lua/src/lapi.c", Score: 0.99},
		{File: "src/t_string.c", Score: 0.2},
		{File: "src/replication.c", Score: 0.3},
		{File: "utils/gen.py", Score: 0.8},
	}
	got := preferCoreHotPaths(rows, "c", security.ShapeFrameworkCore)
	if got[0].File != "src/t_string.c" && got[0].File != "src/replication.c" {
		t.Fatalf("expected src/*.c first, got %+v", got)
	}
}

func TestForceCCoreHotspots_DropsDepsLua(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	_ = os.MkdirAll(src, 0o755)
	for _, f := range []string{"server.c", "networking.c", "acl.c", "db.c"} {
		_ = os.WriteFile(filepath.Join(src, f), []byte("int x;\n"), 0o644)
	}
	rows := []hotspotRow{
		{File: "deps/lua/src/ltablib.c", Score: 0.99},
		{File: "deps/hiredis/adapters/qt.h", Score: 0.95},
		{File: "src/rand.c", Score: 0.9},
		{File: "modules/vector-sets/test.py", Score: 0.8},
		{File: "utils/generate-command-code.py", Score: 0.7},
	}
	got := forceCCoreHotspots(dropHotspotNoise(rows, "c"), dir, "c", security.ShapeFrameworkCore, 10)
	if len(got) == 0 {
		t.Fatal("expected seeded cores")
	}
	for i, r := range got {
		lower := strings.ToLower(r.File)
		if strings.Contains(lower, "deps/") || strings.Contains(lower, "lua") ||
			strings.HasSuffix(lower, ".py") || strings.Contains(lower, "rand.c") {
			t.Fatalf("noise still present at rank %d: %+v", i, got)
		}
	}
	top := strings.ToLower(got[0].File)
	if !strings.Contains(top, "server.c") && !strings.Contains(top, "networking.c") &&
		!strings.Contains(top, "acl.c") && !strings.Contains(top, "db.c") {
		t.Fatalf("expected redis core first, got %+v", got[0])
	}
}

func TestIsHotspotNoisePath(t *testing.T) {
	if !isHotspotNoisePath("deps/lua/src/ltablib.c", "c") {
		t.Fatal("deps/lua must be noise")
	}
	if !isHotspotNoisePath("utils/generate-command-code.py", "c") {
		t.Fatal("utils py must be noise for c")
	}
	if isHotspotNoisePath("src/acl.c", "c") {
		t.Fatal("src/acl.c must not be noise")
	}
}

func TestSecurityQueryPackNonEmpty(t *testing.T) {
	if len(securityQueryPack()) < 3 {
		t.Fatal("security query pack too small")
	}
	if len(perfQueryPack()) < 2 {
		t.Fatal("perf query pack too small")
	}
}

func TestSeedHealthRouteCandidates_DiscordMainGoNotBlocked(t *testing.T) {
	dir := t.TempDir()
	api := filepath.Join(dir, "backend", "cmd", "api")
	_ = os.MkdirAll(api, 0o755)
	_ = os.WriteFile(filepath.Join(api, "main.go"), []byte(`package main
func main() {
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {})
}
`), 0o644)
	// Ranked noise from the same file must NOT block seeding (LIVE discord_mod miss).
	in := []reuseCandidate{
		{Name: "corsMiddleware", Loc: "backend/cmd/api/main.go:10"},
		{Name: "maxBodyMiddleware", Loc: "backend/cmd/api/main.go:40"},
	}
	got := seedHealthRouteCandidates(dir, in, "add GET /health endpoint")
	if len(got) == 0 || !strings.HasPrefix(got[0].Name, "route_health") {
		t.Fatalf("expected seeded route_health first, got %+v", got)
	}
	if !strings.Contains(got[0].Loc, "backend/cmd/api/main.go") {
		t.Fatalf("expected main.go health loc, got %+v", got[0])
	}
}

func TestSeedHealthRouteCandidates_NextAppRouter(t *testing.T) {
	dir := t.TempDir()
	h := filepath.Join(dir, "app", "api", "health")
	r := filepath.Join(dir, "app", "api", "readyz")
	_ = os.MkdirAll(h, 0o755)
	_ = os.MkdirAll(r, 0o755)
	_ = os.WriteFile(filepath.Join(h, "route.ts"), []byte("import { NextResponse } from \"next/server\";\nexport async function GET() {\n  return NextResponse.json({ ok: true });\n}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(r, "route.ts"), []byte("import { NextResponse } from \"next/server\";\nexport async function GET() {\n  return NextResponse.json({ ready: true });\n}\n"), 0o644)
	got := seedHealthRouteCandidates(dir, nil, "add a tiny /health and readyz")
	if len(got) < 2 {
		t.Fatalf("expected health+readyz seeds, got %+v", got)
	}
	joined := got[0].Name + got[0].Loc + got[1].Name + got[1].Loc
	if !strings.Contains(joined, "health") || !strings.Contains(joined, "readyz") {
		t.Fatalf("expected health and readyz in seeds, got %+v", got)
	}
}

func TestDropStyleReuseCandidates_VibeWithoutRole(t *testing.T) {
	in := []reuseCandidate{
		{Name: ".hero", Loc: "static/app.css:1"},
		{Name: "#search-owner-form", Loc: "petclinic.css:12"},
		{Name: "OwnerController", Loc: "OwnerController.java:10"},
	}
	got := dropStyleReuseCandidates(in)
	if len(got) != 1 || got[0].Name != "OwnerController" {
		t.Fatalf("expected only OwnerController, got %+v", got)
	}
}

func TestIsFeatureNoise_AlreadyDoneComment(t *testing.T) {
	if !isFeatureNoiseSymbol("already_done_comment", "scripts/notify_translations.py:377") {
		t.Fatal("already_done_comment must be feature noise")
	}
	if !isFeatureNoiseSymbol("AlreadyDoneComment", "x.py:1") {
		t.Fatal("AlreadyDoneComment must be feature noise")
	}
	if !isFeatureNoiseSymbol("already_notified_comment", "scripts/notify_translations.py:376") {
		t.Fatal("already_notified_comment must be feature noise (fastapi LIVE miss)")
	}
	if !isFeatureNoiseSymbol("toJSON", "packages/svelte/src/index.js:1") {
		t.Fatal("toJSON must be feature noise for UI-lib health tasks")
	}
}

func TestFeatureEndpointAbstainClearsNoise(t *testing.T) {
	cands := []reuseCandidate{
		{Name: "already_notified_comment", Loc: "scripts/notify_translations.py:376"},
		{Name: "toJSON", Loc: "packages/svelte/src/index.js:10"},
		{Name: "applications", Loc: "fastapi/applications.py:1"},
	}
	// Non-HTTP core (svelte-like compiler layout) → abstain + clear noise.
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "packages", "svelte", "src", "compiler"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "packages", "svelte", "src", "compiler", "index.js"), []byte("export {}\n"), 0o644)
	note := featureEndpointAbstainNote("add a simple GET /health", security.ShapeFrameworkCore, dir, cands)
	if note == "" || !strings.Contains(note, "ABSTAIN") {
		t.Fatalf("expected non-HTTP health abstain, got %q", note)
	}
	got := clearNonHealthReuseForAbstain(cands, note)
	if len(got) != 0 {
		t.Fatalf("expected noise cleared on abstain, got %+v", got)
	}
}

func TestPreferPrimaryLanguageFiles_DemotesEntity(t *testing.T) {
	rows := []hotspotRow{
		{File: "src/Entity/User.php", Score: 0.99},
		{File: "src/DataFixtures/AppFixtures.php", Score: 0.9},
		{File: "src/Controller/Admin/BlogController.php", Score: 0.4},
		{File: "src/Controller/HealthController.php", Score: 0.3},
	}
	got := preferPrimaryLanguageFiles(rows, "php")
	if !strings.Contains(got[0].File, "Controller") {
		t.Fatalf("expected Controller first, got %+v", got)
	}
	last := got[len(got)-1].File + got[len(got)-2].File
	if !strings.Contains(last, "Entity") && !strings.Contains(got[len(got)-1].File, "Entity") &&
		!strings.Contains(got[len(got)-1].File, "DataFixtures") {
		// Entity/Fixtures should be in the demoted partition (after controllers).
		foundEntityAfter := false
		sawController := false
		for _, r := range got {
			if strings.Contains(r.File, "Controller") {
				sawController = true
			}
			if sawController && (strings.Contains(r.File, "Entity") || strings.Contains(r.File, "DataFixtures")) {
				foundEntityAfter = true
			}
		}
		if !foundEntityAfter {
			t.Fatalf("expected Entity/Fixtures after Controllers, got %+v", got)
		}
	}

	java := []hotspotRow{
		{File: "src/main/java/com/example/model/Pet.java", Score: 0.95},
		{File: "src/main/java/com/example/owner/OwnerController.java", Score: 0.4},
	}
	jgot := preferPrimaryLanguageFiles(java, "java")
	if !strings.Contains(jgot[0].File, "Controller") {
		t.Fatalf("expected Java Controller first, got %+v", jgot)
	}
	if !isHotspotNoisePath("src/Entity/User.php", "php") {
		t.Fatal("expected PHP Entity path as hotspot noise")
	}
	if !isHotspotNoisePath("src/main/java/com/example/model/Pet.java", "java") {
		t.Fatal("expected Java model path as hotspot noise")
	}
	if isHotspotNoisePath("src/Controller/BlogController.php", "php") {
		t.Fatal("Controller must not be hotspot noise")
	}
}

func TestPreferCoreHotPaths_FlaskAppOverScaffold(t *testing.T) {
	rows := []hotspotRow{
		{File: "src/flask/sansio/scaffold.py", Score: 0.9},
		{File: "src/flask/cli.py", Score: 0.8},
		{File: "src/flask/app.py", Score: 0.3},
		{File: "src/flask/wrappers.py", Score: 0.2},
	}
	got := preferCoreHotPaths(rows, "python", security.ShapeFrameworkCore)
	if !strings.Contains(got[0].File, "app.py") && !strings.Contains(got[0].File, "wrappers.py") {
		t.Fatalf("expected flask app/wrappers before scaffold/cli, got %+v", got)
	}
	// scaffold/cli should be peripheral (later).
	firstTwo := got[0].File + got[1].File
	if strings.Contains(firstTwo, "scaffold") || strings.Contains(firstTwo, "cli.py") {
		t.Fatalf("scaffold/cli must not lead, got %+v", got)
	}
}


func TestPreferFeatureReuse_DemotesSecretsGetForHealth(t *testing.T) {
	in := []reuseCandidate{
		{Name: "Get", Loc: "internal/secrets/secrets.go:193"},
		{Name: "pomDeps", Loc: "internal/profile/deps.go:278"},
		{Name: "handleHealthz", Loc: "internal/agentapi/server.go:98"},
	}
	got := preferFeatureReuse(in, "Add a tiny GET /health endpoint")
	if len(got) == 0 || got[0].Name != "handleHealthz" {
		t.Fatalf("expected handleHealthz first, got %+v", got)
	}
	for _, c := range got {
		if c.Name == "Get" || c.Name == "pomDeps" {
			t.Fatalf("secrets/deps must be demoted for health: %+v", got)
		}
	}
}

func TestSeedHealthRouteCandidates_AgentAPI(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "internal", "agentapi")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "mux.HandleFunc(\"GET /healthz\", s.handleHealthz)\nmux.HandleFunc(\"GET /ready\", s.handleHealthz)\n"
	if err := os.WriteFile(filepath.Join(p, "server.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := seedHealthRouteCandidates(dir, nil, "add GET /health")
	if len(got) == 0 {
		t.Fatal("expected agentapi /healthz seed")
	}
	if !strings.Contains(strings.ToLower(got[0].Name), "health") && !strings.Contains(got[0].Loc, "agentapi") {
		t.Fatalf("unexpected seed %+v", got)
	}
}

func TestSeedHealthRouteCandidates_SymfonyController(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "src", "Controller")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "<?php\n#[Route('/health', name: 'app_health', methods: ['GET'])]\npublic function __invoke() {}\n"
	if err := os.WriteFile(filepath.Join(p, "HealthController.php"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := seedHealthRouteCandidates(dir, nil, "add GET /health")
	if len(got) == 0 {
		t.Fatal("expected Symfony HealthController seed")
	}
	if !strings.Contains(got[0].Loc, "HealthController.php") {
		t.Fatalf("unexpected seed %+v", got[0])
	}
}

func TestPreferFeatureReuse_PlacementHealthJSON(t *testing.T) {
	in := []reuseCandidate{
		{Name: "placement_gin_engine", Loc: "gin.go:92"},
		{Name: "placement_gin_get", Loc: "routergroup.go:116"},
		{Name: "placement_health_json", Loc: "utils.go:65"},
	}
	got := preferFeatureReuse(in, "yo add a tiny health check real quick")
	if len(got) == 0 || got[0].Name != "placement_health_json" {
		t.Fatalf("expected placement_health_json first, got %+v", got)
	}
}

func TestDemoteSecurityLexicalNoise(t *testing.T) {
	in := []retrieval.RankedSymbol{
		{Symbol: types.Symbol{Name: "timeval", Path: "src/ae.c"}, Score: 0.9},
		{Symbol: types.Symbol{Name: "ACLAuthenticateUser", Path: "src/acl.c"}, Score: 0.5},
		{Symbol: types.Symbol{Name: "expand_with", Path: "axum-macros/src/lib.rs"}, Score: 0.8},
	}
	got := demoteSecurityLexicalNoise(in)
	if got[0].Symbol.Name != "ACLAuthenticateUser" {
		t.Fatalf("expected ACL first, got %+v", got)
	}
}

func TestDemoteIntentMismatchedHits_SQLDemotesResSend(t *testing.T) {
	in := []retrieval.RankedSymbol{
		{Symbol: types.Symbol{Name: "res.send", Path: "lib/response.js", LineStart: 126}, Score: 1.8},
		{Symbol: types.Symbol{Name: "res.sendFile", Path: "lib/response.js", LineStart: 373}, Score: 1.3},
		{Symbol: types.Symbol{Name: "exports.compileTrust", Path: "lib/utils.js", LineStart: 194}, Score: 1.0},
		{Symbol: types.Symbol{Name: "escapeHtml", Path: "lib/utils.js", LineStart: 50}, Score: 0.9},
	}
	q := "res.send SQL injection prototype pollution trust proxy"
	got := demoteIntentMismatchedHits(q, in)
	if got[0].Symbol.Name == "res.send" || got[0].Symbol.Name == "res.sendFile" {
		t.Fatalf("SQL-intent query must not rank bare res.send first, got %+v", got[0])
	}
	// Non-SQL query must leave order alone.
	plain := demoteIntentMismatchedHits("where is res.send defined", in)
	if plain[0].Symbol.Name != "res.send" {
		t.Fatalf("non-SQL query should keep res.send, got %+v", plain[0])
	}
}
