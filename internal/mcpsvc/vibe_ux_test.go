package mcpsvc

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/security"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestIsSimpleVibeAsk(t *testing.T) {
	if !isSimpleVibeAsk("idk this feels insecure") {
		t.Fatal("insecure vibe")
	}
	if !isSimpleVibeAsk("make it faster somehow") {
		t.Fatal("perf vibe")
	}
	if !isSimpleVibeAsk("add health real quick") {
		t.Fatal("health vibe")
	}
	if isSimpleVibeAsk("refactor the AuthSessionStore to support rotating refresh tokens with Redis") {
		t.Fatal("precise refactor should not be simple vibe")
	}
}

func TestShouldSuppressSetupTax(t *testing.T) {
	if !shouldSuppressSetupTax("add GET /health", "feature") {
		t.Fatal("health")
	}
	if !shouldSuppressSetupTax("feels insecure", "security") {
		t.Fatal("security role")
	}
	if !shouldSuppressSetupTax("why so slow", "") {
		t.Fatal("perf intent")
	}
}

func TestDefaultVibeSections_LightPayload(t *testing.T) {
	sel := defaultVibeSections("yo add health real quick", "feature")
	if sel == nil || !sel["orient"] || !sel["reuse"] || !sel["docs"] || !sel["steps"] {
		t.Fatalf("expected light sections, got %+v", sel)
	}
	if sel["decisions"] {
		t.Fatal("decisions should be omitted from vibe default")
	}
	if defaultVibeSections("feels insecure", "security") != nil {
		t.Fatal("security must keep full findings payload")
	}
}

func TestVibeNextQueries_AlwaysThree(t *testing.T) {
	for _, role := range []string{"security", "performance", "feature"} {
		got := vibeNextQueries(role, security.ShapeApp, true, "add GET /health", "")
		if len(got) != 3 {
			t.Fatalf("%s: want 3 queries, got %d %+v", role, len(got), got)
		}
		for _, q := range got {
			if strings.TrimSpace(q) == "" {
				t.Fatalf("%s: empty query in %+v", role, got)
			}
		}
	}
}

func TestVibeNextQueries_StackAwareNoValidationPipeOnC(t *testing.T) {
	redisRoot := t.TempDir()
	_ = os.MkdirAll(filepath.Join(redisRoot, "src"), 0o755)
	_ = os.WriteFile(filepath.Join(redisRoot, "src", "server.c"), []byte("int main(){}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(redisRoot, "src", "acl.c"), []byte("void ACLInit(){}\n"), 0o644)
	got := vibeNextQueries("security", security.ShapeFrameworkCore, false, "ACL protected-mode", redisRoot)
	blob := strings.Join(got, "\n")
	if strings.Contains(blob, "ValidationPipe") || strings.Contains(blob, "APP_DEBUG") || strings.Contains(blob, "helmet") {
		t.Fatalf("C/redis must not get Node/PHP next_queries, got %+v", got)
	}
	if !strings.Contains(blob, "ACL") && !strings.Contains(blob, "protected-mode") && !strings.Contains(blob, "requirepass") {
		t.Fatalf("expected ACL/protected-mode follow-ups, got %+v", got)
	}
}

func TestPreferTaskAlignedFindings_AuthFirst(t *testing.T) {
	in := []auditFinding{
		{Rank: 1, Rule: "sql-string-concat", File: "a.ts", Line: 1, Kind: "sink_candidate"},
		{Rank: 2, Rule: "app-auth-middleware", File: "auth.go", Line: 70, Kind: "library_guidance"},
		{Rank: 3, Rule: "app-healthz", File: "server.go", Line: 60, Kind: "library_guidance"},
	}
	got := preferTaskAlignedFindings(in, "where does auth happen?")
	if got[0].Rule != "app-auth-middleware" {
		t.Fatalf("expected auth first, got %+v", got)
	}
	got2 := preferTaskAlignedFindings(in, "any SQL injection?")
	if got2[0].Rule != "sql-string-concat" {
		t.Fatalf("non-auth task must keep order, got %+v", got2)
	}
	got3 := preferTaskAlignedFindings(in, "")
	if got3[0].Rule == "app-healthz" {
		t.Fatalf("vague/empty security must not rank health first, got %+v", got3)
	}
	if got3[0].Rule != "app-auth-middleware" && got3[0].Rule != "sql-string-concat" {
		t.Fatalf("expected auth or sql first on empty task, got %+v", got3)
	}
}

func TestBuildWhatNext_AbstainAndGrounded(t *testing.T) {
	next := []string{"query: auth", "query: secrets", "investigate recipe=security"}
	got := buildWhatNext("security", nil, nil, "ABSTAIN: no sinks", next)
	if !strings.Contains(got, "ABSTAIN") || !strings.Contains(got, "query: auth") {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(got, "not empty") && !strings.Contains(got, "confirmed") {
		t.Fatalf("expected junior-friendly abstain wording, got %q", got)
	}
	// Feature health abstain must NOT reuse security "exploit path" copy (redis LIVE).
	feat := buildWhatNext("feature", nil, nil, "ABSTAIN: no HTTP /health surface (C datastore)", next)
	if strings.Contains(strings.ToLower(feat), "exploit") {
		t.Fatalf("feature abstain must not say exploit path, got %q", feat)
	}
	if !strings.Contains(feat, "ABSTAIN") || !strings.Contains(feat, "health") {
		t.Fatalf("feature abstain should keep concrete reason, got %q", feat)
	}
	findings := []auditFinding{{
		File: "auth.go", Line: 10, Rule: "authz-gap", Kind: "sink_candidate", Hint: "check",
	}}
	got2 := buildWhatNext("security", nil, findings, "", next)
	if !strings.Contains(got2, "auth.go:10") {
		t.Fatalf("expected grounded cite, got %q", got2)
	}
	footgun := []auditFinding{{
		File: "router.go", Line: 1, Rule: "trust-boundary", Kind: "library_guidance", Message: "CORS open",
	}}
	got3 := buildWhatNext("security", nil, footgun, "", next)
	if !strings.Contains(got3, "Guidance only") && !strings.Contains(got3, "footgun") && !strings.Contains(got3, "CVE") {
		t.Fatalf("expected guidance/footgun wording, got %q", got3)
	}
	if !strings.Contains(got3, "Guidance only") {
		t.Fatalf("guidance-only list should say Guidance only, got %q", got3)
	}
}

func TestLabelFrameworkFootguns(t *testing.T) {
	in := []auditFinding{
		{Kind: "library_guidance", Message: "CORS default open", File: "a.go", Line: 1},
		{Kind: "config_hardening", Message: "APP_DEBUG=true", File: "b.php", Line: 2},
		{Kind: "sink_candidate", Message: "SQL concat", File: "c.go", Line: 3},
	}
	got := labelFrameworkFootguns(in, security.ShapeFrameworkCore)
	if !strings.HasPrefix(got[0].Message, "FRAMEWORK FOOTGUN") {
		t.Fatalf("library_guidance: %q", got[0].Message)
	}
	if !strings.HasPrefix(got[1].Message, "CONFIG CHECKLIST") {
		t.Fatalf("config: %q", got[1].Message)
	}
	if strings.HasPrefix(got[2].Message, "FRAMEWORK") {
		t.Fatal("sink must stay unprefixed")
	}
}

func TestEnrichPerfFindingsWhy(t *testing.T) {
	in := []auditFinding{{File: "BlogController.php", Line: 40, Rule: "app-hot-path", Message: "hot"}}
	got := enrichPerfFindingsWhy(in, security.ShapeApp)
	if got[0].Hint == "" || !strings.Contains(got[0].Hint, "Next tool") {
		t.Fatalf("expected why+next tool, got %q", got[0].Hint)
	}
	if !strings.Contains(got[0].Hint, "Rewrite:") {
		t.Fatalf("expected Rewrite suggestion, got %q", got[0].Hint)
	}
}

func TestSuggestHotspotRewrite(t *testing.T) {
	got := suggestHotspotRewrite("app/Http/Controllers/PostController.php", security.ShapeApp)
	if !strings.Contains(strings.ToLower(got), "eager") && !strings.Contains(strings.ToLower(got), "paginat") {
		t.Fatalf("php controller rewrite: %q", got)
	}
	lib := suggestHotspotRewrite("packages/runtime-core/src/scheduler.ts", security.ShapeFrameworkCore)
	if lib == "" {
		t.Fatal("framework rewrite should not be empty")
	}
}

func TestIsHealthishTarget(t *testing.T) {
	if !isHealthishTarget("health") || !isHealthishTarget("/readyz") || !isHealthishTarget("getHealth") {
		t.Fatal("expected healthish")
	}
	if isHealthishTarget("isClusterHealthy") {
		t.Fatal("cluster healthy is noise")
	}
	if !isBareHealthishTarget("health") || !isBareHealthishTarget("/healthz") {
		t.Fatal("expected bare healthish")
	}
	if isBareHealthishTarget("Health") {
		t.Fatal("PascalCase Health is a type name (Unity), not a bare HTTP vibe")
	}
	if isBareHealthishTarget("getHealth") || isBareHealthishTarget("HealthController") {
		t.Fatal("concrete symbol names are not bare")
	}
	if !isPlacementTarget("placement_api_route") || !isHealthishTarget("placement_url_resolver") {
		t.Fatal("placement_* must resolve via healthish/placement fallback")
	}
	if isBareHealthishTarget("placement_api_route") {
		t.Fatal("placement_* is not a bare health vibe — it is a synthetic seed name")
	}
}

func TestVibeRecommendedTools_SecurityPrefersInvestigate(t *testing.T) {
	got := vibeRecommendedTools("security", nil, false)
	if len(got) == 0 || got[0] != "investigate" {
		t.Fatalf("security RNT should lead with investigate, got %v", got)
	}
}

func TestChangeKit_HealthSeedAndAbstain(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"pkg/util.go": "package pkg\n\nfunc Helper() int { return 1 }\n",
	})
	// Seed a health route on disk (anonymous / unindexed).
	_ = os.MkdirAll(filepath.Join(repo.RootPath, "cmd", "api"), 0o755)
	_ = os.WriteFile(filepath.Join(repo.RootPath, "cmd", "api", "main.go"), []byte(
		"package main\nfunc main() {\n\tr.Get(\"/health\", func() {})\n}\n",
	), 0o644)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "target": "health", "format": "json"}
	res, err := changeKitHandler(reg)(ctx, req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected seeded health kit, got error: %+v", res.Content)
	}
	out := decodeStructured[changeKitResponse](t, res)
	if !strings.Contains(out.Target.Loc, "main.go") || !strings.HasPrefix(out.Target.Name, "route_health") {
		t.Fatalf("expected route_health seed, got %+v", out.Target)
	}
	if out.Definition == "" {
		t.Fatal("expected definition snippet")
	}

	// Non-HTTP shape abstain: library-like tree with no health route.
	reg2, repo2, ctx2 := buildIndexedRepo(t, map[string]string{
		"lib/core.go": "package lib\n\nfunc Parse() {}\n",
	})
	req2 := mcp.CallToolRequest{}
	req2.Params.Arguments = map[string]any{"repo": repo2.Name, "target": "health", "format": "json"}
	res2, err := changeKitHandler(reg2)(ctx2, req2)
	if err != nil {
		t.Fatalf("handler2: %v", err)
	}
	if !res2.IsError {
		t.Fatal("expected ABSTAIN error when no health route")
	}
	text := toolResultText(res2)
	if !strings.Contains(text, "ABSTAIN") {
		t.Fatalf("expected ABSTAIN in error, got %q", text)
	}
	if !strings.Contains(text, "Next queries") && !strings.Contains(text, "query:") {
		t.Fatalf("expected copy-paste next queries, got %q", text)
	}

	// HTTP framework: placement seed (not non-HTTP abstain).
	flaskRoot := t.TempDir()
	_ = os.MkdirAll(filepath.Join(flaskRoot, "src", "flask", "sansio"), 0o755)
	_ = os.WriteFile(filepath.Join(flaskRoot, "src", "flask", "app.py"), []byte("class Flask:\n    pass\n"), 0o644)
	_ = os.WriteFile(filepath.Join(flaskRoot, "src", "flask", "sansio", "scaffold.py"), []byte(
		"def route(self, rule):\n    return rule\n"), 0o644)
	_ = os.WriteFile(filepath.Join(flaskRoot, "pkg.go"), []byte("package main\nfunc Helper() {}\n"), 0o644)
	reg3, repo3, ctx3 := buildIndexedRepo(t, map[string]string{
		"pkg/util.go": "package pkg\n\nfunc Helper() int { return 1 }\n",
	})
	// Point repo at flask layout by writing into the indexed root.
	_ = os.MkdirAll(filepath.Join(repo3.RootPath, "src", "flask", "sansio"), 0o755)
	_ = os.WriteFile(filepath.Join(repo3.RootPath, "src", "flask", "app.py"), []byte("class Flask:\n    pass\n"), 0o644)
	_ = os.WriteFile(filepath.Join(repo3.RootPath, "src", "flask", "sansio", "scaffold.py"), []byte(
		"def route(self, rule):\n    return rule\n"), 0o644)
	req3 := mcp.CallToolRequest{}
	req3.Params.Arguments = map[string]any{"repo": repo3.Name, "target": "health", "format": "json"}
	res3, err := changeKitHandler(reg3)(ctx3, req3)
	if err != nil {
		t.Fatalf("handler3: %v", err)
	}
	if res3.IsError {
		t.Fatalf("HTTP flask must not non-HTTP-abstain change_kit, got %q", toolResultText(res3))
	}
	out3 := decodeStructured[changeKitResponse](t, res3)
	if !strings.HasPrefix(out3.Target.Name, "placement_") {
		t.Fatalf("expected placement target, got %+v", out3.Target)
	}
	_ = flaskRoot // layout covered via repo3
}

func TestKickoff_VibeWhatNextAndNoSetupTax(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"cmd/api/main.go": "package main\n\nfunc main() {\n\t_ = \"/health\"\n}\n",
		"routes/web.php":  "<?php\nRoute::get('/health', fn() => ['ok'=>true]);\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo": repo.Name, "task": "yo add health real quick", "format": "json",
	}
	res, err := kickoffHandler(reg)(ctx, req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	out := decodeStructured[kickoffResponse](t, res)
	if out.WhatNext == "" {
		t.Fatal("expected what_next")
	}
	if len(out.NextQueries) != 3 {
		t.Fatalf("expected 3 next_queries, got %+v", out.NextQueries)
	}
	if out.Orient.SetupSuggestions != nil {
		t.Fatal("setup tax must be suppressed on simple health vibe ask")
	}
	// Light sections: decisions cleared by default vibe sections.
	if len(out.DecisionPoints) > 0 {
		t.Fatalf("vibe default should drop decisions, got %+v", out.DecisionPoints)
	}
}

// Without ResolveWalkRoot, WalkDir on junction/src sees 0 .c files and misses "c".
func TestDetectEcosystem_WindowsJunction(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows junctions only")
	}
	base := t.TempDir()
	target := filepath.Join(base, "core")
	link := filepath.Join(base, "bed")
	src := filepath.Join(target, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		p := filepath.Join(src, "file"+strconv.Itoa(i)+".c")
		if err := os.WriteFile(p, []byte("int x;\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("cmd", "/c", "mklink", "/J", link, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("mklink failed: %v (%s)", err, out)
	}
	if got := detectEcosystem(link); got != "c" {
		t.Fatalf("detectEcosystem(junction)=%q want c", got)
	}
}
