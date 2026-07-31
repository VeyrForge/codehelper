package mcpsvc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/internal/workspacectx"
	"github.com/mark3labs/mcp-go/server"
)

// TestRealTreeNestExpressQuality probes .eval-projects Express OSS + Nest starter
// + Laravel, and the Nest stub (src + sample collisions), for agent-usable
// ranking/honesty across query → context → impact → investigate.
func TestRealTreeNestExpressQuality(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	root := findRepoRoot(t)
	expressRoot := filepath.Join(root, ".eval-projects", "express")
	expressDB := filepath.Join(expressRoot, ".codehelper", "graph.db")
	nestStarterRoot := filepath.Join(root, ".eval-projects", "nestjs-typescript-starter")
	nestStarterDB := filepath.Join(nestStarterRoot, ".codehelper", "graph.db")
	laravelRoot := filepath.Join(root, ".eval-projects", "laravel")
	laravelDB := filepath.Join(laravelRoot, ".codehelper", "graph.db")
	nestStub := nestStubGraph(t, root)
	if _, err := os.Stat(expressDB); err != nil {
		t.Skipf("express OSS not indexed: %v", err)
	}
	if _, err := os.Stat(nestStarterDB); err != nil {
		t.Skipf("nestjs-typescript-starter not indexed: %v", err)
	}

	reg, err := registry.Load()
	if err != nil {
		t.Fatal(err)
	}
	handlers := AllToolHandlers(reg)

	t.Run("express_createApplication_lib", func(t *testing.T) {
		st := mustOpenGraph(t, expressDB)
		hits, err := retrieval.QueryHybrid(context.Background(), st, "express", "createApplication", 12)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := demoteFixtureHits(hits)
		if len(got) == 0 || got[0].Symbol.Name != "createApplication" || !strings.Contains(got[0].Symbol.Path, "lib/") {
			t.Fatalf("top=%v want createApplication under lib/", summarizeTop(got, 3))
		}
	})

	t.Run("express_app_use_prefers_lib_over_examples", func(t *testing.T) {
		st := mustOpenGraph(t, expressDB)
		hits, err := retrieval.QueryHybrid(context.Background(), st, "express", "app.use middleware", 20)
		if err != nil {
			t.Fatal(err)
		}
		got, demoted := demoteFixtureHits(hits)
		t.Logf("demoted=%d top5=%v", demoted, summarizeTop(got, 5))
		if len(got) == 0 {
			t.Fatal("no hits")
		}
		top := got[0]
		// Prefer production Application.use (lib/) over examples/* middleware helpers.
		if strings.Contains(strings.ToLower(top.Symbol.Path), "/examples/") || strings.HasPrefix(strings.ToLower(top.Symbol.Path), "examples/") {
			t.Fatalf("examples still top after demote: %s %s — agent needs lib/app.use or path= honesty", top.Symbol.Name, top.Symbol.Path)
		}
		libHit := false
		for _, h := range got {
			p := strings.ToLower(strings.ReplaceAll(h.Symbol.Path, "\\", "/"))
			if strings.Contains(p, "lib/") && (h.Symbol.Name == "app.use" || h.Symbol.Name == "createApplication" || strings.Contains(h.Symbol.Name, "use")) {
				libHit = true
				break
			}
		}
		if !libHit {
			t.Fatalf("no lib use/createApplication in demoted hits: %v", summarizeTop(got, 8))
		}
		note := collisionHonestyNote(got, demoted)
		hasExamples := false
		for _, h := range got {
			p := strings.ToLower(strings.ReplaceAll(h.Symbol.Path, "\\", "/"))
			if strings.Contains(p, "/examples/") || strings.HasPrefix(p, "examples/") {
				hasExamples = true
				break
			}
		}
		if hasExamples && demoted == 0 && note == "" {
			t.Logf("note empty with examples still present (ok if exact prod topped): %q", note)
		}
	})

	t.Run("express_mcp_query_context_impact_investigate", func(t *testing.T) {
		assertMCPLocateChain(t, handlers, expressRoot, mcpLocateProbe{
			Query:      "app.use middleware",
			Symbol:     "createApplication",
			ExpectAny:  []string{"createApplication", "application", "lib/"},
			ExpectPath: "lib/",
		})
	})

	t.Run("nest_starter_AppService", func(t *testing.T) {
		st := mustOpenGraph(t, nestStarterDB)
		hits, err := retrieval.QueryHybrid(context.Background(), st, "nestjs-typescript-starter", "AppService", 8)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := demoteFixtureHits(hits)
		if len(got) == 0 || got[0].Symbol.Name != "AppService" || !strings.Contains(got[0].Symbol.Path, "app.service.ts") {
			t.Fatalf("top=%v want AppService src/app.service.ts", summarizeTop(got, 3))
		}
		ctx := workspacectx.WithRoots(nestStarterRoot)
		blob := pairedCall(ctx, handlers, "context", map[string]any{"name": "AppService", "format": "json"})
		if !strings.Contains(blob, "AppService") || !strings.Contains(blob, "app.service") {
			t.Fatalf("context AppService miss: %s", truncateSmoke(blob, 300))
		}
	})

	t.Run("nest_starter_mcp_investigate", func(t *testing.T) {
		assertMCPLocateChain(t, handlers, nestStarterRoot, mcpLocateProbe{
			Query:     "AppService",
			Symbol:    "AppService",
			ExpectAny: []string{"AppService", "app.service"},
		})
	})

	t.Run("nest_stub_src_beats_sample", func(t *testing.T) {
		if nestStub == "" {
			t.Skip("nest-starter CatsService stub not indexed")
		}
		st := mustOpenGraph(t, nestStub)
		hits, err := retrieval.QueryHybrid(context.Background(), st, "nest-starter", "CatsService", 20)
		if err != nil {
			t.Fatal(err)
		}
		got, demoted := demoteFixtureHits(hits)
		t.Logf("demoted=%d top5=%v", demoted, summarizeTop(got, 5))
		if len(got) == 0 || got[0].Symbol.Name != "CatsService" {
			t.Fatalf("top=%v", summarizeTop(got, 3))
		}
		p0 := strings.ReplaceAll(got[0].Symbol.Path, "\\", "/")
		if strings.Contains(p0, "sample/") || !strings.Contains(p0, "src/cats/") {
			t.Fatalf("production src/cats should beat samples, got %s", p0)
		}
		sampleRank := -1
		for i, h := range got {
			if h.Symbol.Name == "CatsService" && strings.Contains(h.Symbol.Path, "sample/01-cats-app/") {
				sampleRank = i + 1
				break
			}
		}
		if sampleRank != 2 {
			t.Fatalf("sample/01 CatsService rank=%d want 2 (after src/)", sampleRank)
		}
		note := collisionHonestyNote(got, demoted)
		if strings.Contains(note, "Ambiguous") {
			t.Fatalf("should not ambiguity-warn when production src exists: %q", note)
		}
	})

	t.Run("nest_stub_mcp_investigate_cats", func(t *testing.T) {
		if nestStub == "" {
			t.Skip("nest-starter CatsService stub not indexed")
		}
		nestRoot := filepath.Dir(filepath.Dir(nestStub)) // .../.codehelper/graph.db → bed root
		assertMCPLocateChain(t, handlers, nestRoot, mcpLocateProbe{
			Query:      "CatsService",
			Symbol:     "CatsService",
			ExpectAny:  []string{"CatsService", "cats.service"},
			ExpectPath: "src/",
		})
	})

	t.Run("laravel_User_model", func(t *testing.T) {
		if _, err := os.Stat(laravelDB); err != nil {
			t.Skipf("laravel not indexed: %v", err)
		}
		st := mustOpenGraph(t, laravelDB)
		hits, err := retrieval.QueryHybrid(context.Background(), st, "laravel", "User model", 12)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := demoteFixtureHits(hits)
		if len(got) == 0 {
			t.Fatal("no hits for User model")
		}
		ok := false
		n := len(got)
		if n > 5 {
			n = 5
		}
		for _, h := range got[:n] {
			p := strings.ReplaceAll(h.Symbol.Path, "\\", "/")
			if h.Symbol.Name == "User" && (strings.Contains(p, "Models") || strings.Contains(p, "models")) {
				ok = true
				break
			}
			if strings.Contains(h.Symbol.Name, "User") && strings.Contains(strings.ToLower(p), "model") {
				ok = true
				break
			}
		}
		if !ok {
			t.Fatalf("top=%v want User under Models/", summarizeTop(got, 5))
		}
	})

	t.Run("laravel_mcp_query_context_impact_investigate", func(t *testing.T) {
		if _, err := os.Stat(laravelDB); err != nil {
			t.Skipf("laravel not indexed: %v", err)
		}
		assertMCPLocateChain(t, handlers, laravelRoot, mcpLocateProbe{
			Query:     "User model",
			Symbol:    "User",
			ExpectAny: []string{"User", "Models"},
		})
	})
}

type mcpLocateProbe struct {
	Query      string
	Symbol     string
	ExpectAny  []string
	ExpectPath string
}

// assertMCPLocateChain requires query → context → impact → investigate to locate
// gold symbols and for investigate to fuse context+impact with what_next.
func assertMCPLocateChain(t *testing.T, handlers map[string]server.ToolHandlerFunc, root string, p mcpLocateProbe) {
	t.Helper()
	ctx := workspacectx.WithRoots(root)

	q := pairedCall(ctx, handlers, "query", map[string]any{
		"query": p.Query, "format": "json", "top_k": 8,
	})
	c := pairedCall(ctx, handlers, "context", map[string]any{
		"name": p.Symbol, "format": "json",
	})
	imp := pairedCall(ctx, handlers, "impact", map[string]any{
		"name": p.Symbol, "format": "json",
		"depth": 1, "max_candidates": 8, "include_tests": false,
	})
	inv := pairedCall(ctx, handlers, "investigate", map[string]any{
		"query": p.Query, "format": "json",
	})

	blob := q + "\n" + c + "\n" + imp + "\n" + inv
	ok, hits := locateHits(blob, p.ExpectAny)
	if !ok {
		t.Fatalf("locate miss for %v; hits=%v\nq=%s\nc=%s\nimp=%s\ninv=%s",
			p.ExpectAny, hits, truncateSmoke(q, 240), truncateSmoke(c, 240), truncateSmoke(imp, 240), truncateSmoke(inv, 240))
	}
	if p.ExpectPath != "" {
		ql := strings.ToLower(strings.ReplaceAll(q, "\\", "/"))
		if !strings.Contains(ql, strings.ToLower(filepath.ToSlash(p.ExpectPath))) {
			t.Fatalf("query missing expect path %q: %s", p.ExpectPath, truncateSmoke(q, 400))
		}
	}
	ivl := strings.ToLower(inv)
	if strings.HasPrefix(inv, "tool_error:") || strings.HasPrefix(inv, "missing tool:") {
		t.Fatalf("investigate failed: %s", truncateSmoke(inv, 400))
	}
	if !strings.Contains(ivl, "context") || !strings.Contains(ivl, "impact") {
		t.Fatalf("investigate missing fuse steps context/impact: %s", truncateSmoke(inv, 500))
	}
	if !strings.Contains(ivl, "what_next") {
		t.Fatalf("investigate missing what_next: %s", truncateSmoke(inv, 500))
	}
	invOK, _ := locateHits(inv, p.ExpectAny)
	if !invOK {
		t.Fatalf("investigate locate miss for %v: %s", p.ExpectAny, truncateSmoke(inv, 500))
	}
	// Nested beds must not displace gold with host-repo-only matches.
	if strippedOK, _ := locateHits(stripCodehelperLeakLines(blob), p.ExpectAny); !strippedOK {
		t.Fatalf("wrong-repo bleed for %v", p.ExpectAny)
	}
}

func nestStubGraph(t *testing.T, root string) string {
	t.Helper()
	candidates := []string{
		filepath.Join(root, ".testbeds", "active", "nest-starter", ".codehelper", "graph.db"),
		filepath.Join(root, ".testbeds", "active", ".stub-src", "nest", ".codehelper", "graph.db"),
		filepath.Join(root, ".testbeds", "real-oss", "nest-starter", ".codehelper", "graph.db"),
		filepath.Join(root, ".testbeds", "real-oss", "nest", ".codehelper", "graph.db"),
		filepath.Join(root, ".ci-testbeds-extended", "nest", ".codehelper", "graph.db"), // legacy
	}
	for _, p := range candidates {
		st, err := graph.Open(p)
		if err != nil {
			continue
		}
		// Repo id in the graph may be nest or nest-starter depending on analyze --name.
		hits, qerr := retrieval.QueryHybrid(context.Background(), st, "nest-starter", "CatsService", 3)
		if qerr != nil || len(hits) == 0 {
			hits, qerr = retrieval.QueryHybrid(context.Background(), st, "nest", "CatsService", 3)
		}
		_ = st.Close()
		if qerr == nil && len(hits) > 0 {
			return p
		}
	}
	return ""
}

func mustOpenGraph(t *testing.T, db string) *graph.Store {
	t.Helper()
	st, err := graph.Open(db)
	if err != nil {
		t.Fatalf("open %s: %v", db, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func summarizeTop(hits []retrieval.RankedSymbol, n int) []string {
	var out []string
	for i, h := range hits {
		if i >= n {
			break
		}
		out = append(out, h.Symbol.Name+"@"+h.Symbol.Path)
	}
	return out
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
