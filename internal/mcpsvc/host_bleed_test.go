package mcpsvc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/internal/workspacectx"
	"github.com/VeyrForge/codehelper/pkg/types"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestDropHostBleedHits_NestedBed(t *testing.T) {
	hits := []retrieval.RankedSymbol{
		{Symbol: types.Symbol{ID: "sym:codehelper:internal/x.go:1:Foo", RepoID: "codehelper", Name: "Foo", Path: "internal/x.go"}, Score: 1},
		{Symbol: types.Symbol{ID: "sym:godot:scripts/player.gd:1:_ready", RepoID: "godot", Name: "_ready", Path: "scripts/player.gd"}, Score: 0.9},
	}
	got, dropped := dropHostBleedHits(hits, "godot")
	if dropped != 1 || len(got) != 1 || got[0].Symbol.RepoID != "godot" {
		t.Fatalf("got=%+v dropped=%d", got, dropped)
	}
	kept, d2 := dropHostBleedHits(hits, "codehelper")
	if d2 != 0 || len(kept) != 2 {
		t.Fatalf("host product must keep all hits: len=%d dropped=%d", len(kept), d2)
	}
}

func TestScrubHostBleedPayload_DropsHostSymIDs(t *testing.T) {
	payload := map[string]any{
		"repo": "codehelper",
		"hits": []any{
			map[string]any{"id": "sym:codehelper:internal/a.go:1:X", "name": "X"},
			map[string]any{"id": "sym:godot:scripts/player.gd:1:_ready", "name": "_ready"},
		},
		"note": "see sym:codehelper:internal/a.go:1:X",
	}
	got := scrubHostBleedPayload(payload, "godot").(map[string]any)
	if got["repo"] != "godot" {
		t.Fatalf("repo rewrite: %v", got["repo"])
	}
	hits, _ := got["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit after scrub, got %#v", hits)
	}
	hm := hits[0].(map[string]any)
	if hm["name"] != "_ready" {
		t.Fatalf("expected godot hit, got %#v", hm)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "sym:codehelper:") {
		t.Fatalf("host bleed still present: %s", b)
	}
}

func TestScrubHostBleedPayload_DropsHostPathsAndRepoLabeledHits(t *testing.T) {
	payload := map[string]any{
		"repo": "codehelper",
		"hits": []any{
			map[string]any{
				"repo": "codehelper",
				"name": "scrubHostBleedPayload",
				"kind": "function",
				"path": "internal/mcpsvc/host_bleed.go",
				"id":   "sym:codehelper:internal/mcpsvc/host_bleed.go:52:scrubHostBleedPayload",
			},
			map[string]any{
				"repo_id": "codehelper",
				"name":    "HostOnly",
				"kind":    "function",
				"path":    "F:/Projects/codehelper/internal/mcpsvc/register.go",
			},
			map[string]any{
				"name": "Ok",
				"kind": "function",
				"path": "scripts/player.gd",
				"id":   "sym:godot:scripts/player.gd:1:Ok",
				"repo": "godot",
			},
		},
		"group_query": map[string]any{
			"hits": []any{
				map[string]any{
					"repo": "codehelper",
					"name": "Query",
					"path": "internal/mcpsvc/register.go",
					"id":   "sym:codehelper:internal/mcpsvc/register.go:837:queryHandler",
				},
				map[string]any{
					"repo": "godot",
					"name": "_ready",
					"path": "scripts/player.gd",
					"id":   "sym:godot:scripts/player.gd:1:_ready",
				},
			},
		},
		"cross_repo_candidates": []any{
			map[string]any{
				"name":      "codehelper",
				"root_path": `F:\Projects\codehelper`,
			},
			map[string]any{
				"name":      "godot",
				"root_path": `F:\Projects\codehelper\.testbeds\real-oss\godot`,
			},
		},
		"what_next": "change_kit target=sym:codehelper:internal/x.go:1:Foo",
		"evidence_paths": []any{
			"F:/Projects/codehelper/internal/mcpsvc/host_bleed.go",
			"scripts/player.gd",
			"F:/Projects/codehelper/.testbeds/real-oss/godot/scripts/player.gd",
		},
	}
	got := scrubHostBleedPayload(payload, "godot").(map[string]any)
	if got["repo"] != "godot" {
		t.Fatalf("repo rewrite: %v", got["repo"])
	}
	hits, _ := got["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("expected 1 bed hit, got %#v", hits)
	}
	gq, _ := got["group_query"].(map[string]any)
	gqHits, _ := gq["hits"].([]any)
	if len(gqHits) != 1 {
		t.Fatalf("expected 1 group hit, got %#v", gqHits)
	}
	cross, _ := got["cross_repo_candidates"].([]any)
	if len(cross) != 1 {
		t.Fatalf("expected host registry entry dropped, got %#v", cross)
	}
	ev, _ := got["evidence_paths"].([]any)
	if len(ev) != 2 {
		t.Fatalf("expected host absolute path dropped from evidence, got %#v", ev)
	}
	b, _ := json.Marshal(got)
	blob := string(b)
	if strings.Contains(blob, "sym:codehelper:") {
		t.Fatalf("sym bleed: %s", blob)
	}
	if strings.Contains(strings.ToLower(blob), "/codehelper/internal/") {
		t.Fatalf("path bleed: %s", blob)
	}
	if wn, ok := got["what_next"]; ok && wn != nil && wn != "" {
		t.Fatalf("what_next should be omitted after scrub, got %#v", wn)
	}
}

func TestScrubHostBleedPayload_PreservesHostProductWorkspace(t *testing.T) {
	payload := map[string]any{
		"repo": "codehelper",
		"hits": []any{
			map[string]any{"id": "sym:codehelper:internal/a.go:1:X", "name": "X"},
		},
	}
	got := scrubHostBleedPayload(payload, "codehelper").(map[string]any)
	hits, _ := got["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("host product must keep host hits: %#v", got)
	}
}

func TestIsHostBleedPath_AllowsNestedBeds(t *testing.T) {
	if isHostBleedPath(`F:\Projects\codehelper\.testbeds\real-oss\godot\scripts\player.gd`) {
		t.Fatal("nested bed path must not scrub")
	}
	if isHostBleedPath("F:/x/codehelper/.eval-projects/django/app.py") {
		t.Fatal("eval-project path must not scrub")
	}
	if !isHostBleedPath("F:/Projects/codehelper/internal/mcpsvc/host_bleed.go") {
		t.Fatal("host internal path must scrub")
	}
	if !isHostBleedPath(`F:\Projects\codehelper`) {
		t.Fatal("bare host root must scrub")
	}
}

func TestDropHostBleedHits_HostAbsolutePath(t *testing.T) {
	hits := []retrieval.RankedSymbol{
		{Symbol: types.Symbol{
			ID: "sym:godot:x:1:A", RepoID: "godot", Name: "A",
			Path: "F:/Projects/codehelper/internal/x.go",
		}},
		{Symbol: types.Symbol{
			ID: "sym:godot:scripts/player.gd:1:B", RepoID: "godot", Name: "B",
			Path: "scripts/player.gd",
		}},
	}
	got, dropped := dropHostBleedHits(hits, "godot")
	if dropped != 1 || len(got) != 1 || got[0].Symbol.Name != "B" {
		t.Fatalf("got=%+v dropped=%d", got, dropped)
	}
}

func TestAppendFreshnessWarnings_DedupesStale(t *testing.T) {
	fresh := freshness.Report{Stale: true, StaleReason: "working tree changed"}
	warn := "index may be stale: working tree changed — codehelper analyze --force"
	got := appendFreshnessWarnings(nil, warn, fresh)
	if len(got) != 1 {
		t.Fatalf("expected single warning, got %#v", got)
	}
	got2 := appendFreshnessWarnings(nil, "", fresh)
	if len(got2) != 1 || !strings.HasPrefix(got2[0], "stale index:") {
		t.Fatalf("expected stale index fallback, got %#v", got2)
	}
}

func TestBindOpenIndexedWorkspace_SkipsParentCodehelper(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "codehelper")
	child := filepath.Join(parent, ".testbeds", "godot")
	if err := os.MkdirAll(filepath.Join(child, ".codehelper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, ".codehelper", "graph.db"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, ".codehelper", "meta.json"), []byte(`{"schema_version":2,"symbol_count":5}`), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := &registry.Registry{
		Entries: map[string]registry.Entry{
			"codehelper": {Name: "codehelper", RootPath: parent, SchemaVer: 2},
		},
	}
	// Parent match is skipped because child has its own index — bindOpen must win.
	name, _, ok := repoNameForRoots(reg, []string{normalizeComparablePath(child)})
	if ok {
		t.Fatalf("expected no parent match, got %q", name)
	}
	got, ok := bindOpenIndexedWorkspace(context.Background(), reg, []string{child})
	if !ok || got != "godot" {
		t.Fatalf("bindOpen=%q ok=%v want godot", got, ok)
	}
	e, ok := reg.Get("godot")
	if !ok || normalizeComparablePath(e.RootPath) != normalizeComparablePath(child) {
		t.Fatalf("godot entry: %+v", e)
	}
}

func TestResolveRepo_NestedBedDoesNotBindHost(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "codehelper")
	child := filepath.Join(parent, ".testbeds", "lua")
	if err := os.MkdirAll(filepath.Join(child, ".codehelper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, ".codehelper", "graph.db"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, ".codehelper", "meta.json"), []byte(`{"schema_version":2,"symbol_count":3}`), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := &registry.Registry{
		Entries: map[string]registry.Entry{
			"codehelper": {Name: "codehelper", RootPath: parent, SchemaVer: 2},
		},
	}
	got, err := resolveRepo(workspacectx.WithRoots(child), reg, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "lua" {
		t.Fatalf("resolved %q want lua (not codehelper)", got.Name)
	}
	if normalizeComparablePath(got.RootPath) != normalizeComparablePath(child) {
		t.Fatalf("root=%s want %s", got.RootPath, child)
	}
}

func TestEnrichImpactResponse_ForcesUnknownRiskOnSparse(t *testing.T) {
	out := &impactMCPResponse{
		CallGraphConfidence: "LOW — sparse call graph (0 call edges / 10 symbols = 0.00/sym)",
	}
	res := &types.ImpactResult{Target: "Foo", RiskTier: "low", Nodes: []types.ImpactNode{{Name: "Foo"}}}
	enrichImpactResponse(out, res)
	if out.Impact == nil || out.Impact.RiskTier != "unknown" {
		t.Fatalf("expected risk_tier=unknown, got %#v", out.Impact)
	}
	found := false
	for _, w := range out.Warnings {
		if strings.Contains(w, "AGENT DIRECTIVE") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected AGENT DIRECTIVE warning, got %#v", out.Warnings)
	}
}

func TestNestedTestbedQuery_NoHostSymBleed(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bed := filepath.Join(root, ".testbeds", "real-oss", "godot")
	if _, err := os.Stat(filepath.Join(bed, ".codehelper", "meta.json")); err != nil {
		t.Skip("no .testbeds/real-oss/godot index")
	}
	reg := &registry.Registry{
		Entries: map[string]registry.Entry{
			"codehelper": {Name: "codehelper", RootPath: root, SchemaVer: 2},
		},
	}
	// Do NOT Upsert the bed first — exercise bindOpenIndexedWorkspace path.
	handlers := AllToolHandlers(reg)
	h, ok := handlers["query"]
	if !ok {
		t.Fatal("missing query")
	}
	ctx := workspacectx.WithRoots(bed)
	req := mcp.CallToolRequest{}
	req.Params.Name = "query"
	req.Params.Arguments = map[string]any{"query": "_ready", "format": "json", "top_k": 8}
	res, err := h(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(res)
	if strings.Contains(text, "sym:codehelper:") {
		t.Fatalf("nested bed response leaked host ids:\n%s", truncateSmoke(text, 800))
	}
	if strings.Contains(text, `"repo":"codehelper"`) || strings.Contains(text, `"repo": "codehelper"`) {
		t.Fatalf("nested bed response leaked host repo label:\n%s", truncateSmoke(text, 800))
	}
	// Resolve must bind godot, not codehelper.
	e, err := resolveRepo(ctx, reg, "")
	if err != nil {
		t.Fatal(err)
	}
	if e.Name == "codehelper" {
		t.Fatal("resolveRepo still bound host codehelper for nested godot bed")
	}
}

func TestAppendSparseIsolationWarnings_SwiftLuaBand(t *testing.T) {
	w := appendSparseIsolationWarnings(nil, "LOW — sparse", "swift", true)
	blob := strings.Join(w, "\n")
	if !strings.Contains(blob, "AGENT DIRECTIVE") || !strings.Contains(blob, "swift") {
		t.Fatalf("warnings weak: %s", blob)
	}
	w2 := appendSparseIsolationWarnings(nil, "LOW — sparse", "gdscript", true)
	if !strings.Contains(strings.Join(w2, "\n"), "gdscript") {
		t.Fatalf("gdscript warning missing: %v", w2)
	}
}

func TestAppendSparseIsolationWarnings_MediumBand(t *testing.T) {
	w := appendSparseIsolationWarnings(nil, "MEDIUM — moderate call density", "java", true)
	blob := strings.Join(w, "\n")
	if !strings.Contains(blob, "MEDIUM") || !strings.Contains(blob, "java") {
		t.Fatalf("medium warnings weak: %s", blob)
	}
	if strings.Contains(blob, "call_graph_confidence=LOW") {
		t.Fatalf("MEDIUM path should not emit LOW directive: %s", blob)
	}
}

func TestEnrichImpactResponse_MediumBand(t *testing.T) {
	out := &impactMCPResponse{
		CallGraphConfidence: "MEDIUM — moderate call density (12 call edges / 10 symbols = 1.20/sym)",
		PrimaryLanguage:     "java",
	}
	res := &types.ImpactResult{Target: "OwnerController", RiskTier: "low", Nodes: []types.ImpactNode{{Name: "OwnerController"}}}
	enrichImpactResponse(out, res)
	if out.Confidence < 0.5 || out.Confidence >= 0.8 {
		t.Fatalf("medium confidence band unexpected: %v", out.Confidence)
	}
	if out.Impact == nil || out.Impact.RiskTier != "unknown" {
		t.Fatalf("expected risk_tier=unknown on medium+self-only, got %#v", out.Impact)
	}
	if out.Note == "" || !strings.Contains(out.Note, "MEDIUM") {
		t.Fatalf("expected MEDIUM note, got %q", out.Note)
	}
	if out.WhatNext == "" || !strings.HasPrefix(out.WhatNext, "Thinner call graph") {
		t.Fatalf("expected junior MEDIUM what_next prefix, got %q", out.WhatNext)
	}
	if strings.Contains(out.WhatNext, "MEDIUM CALL GRAPH:") || strings.Contains(out.WhatNext, "call_graph_confidence=MEDIUM") {
		t.Fatalf("what_next still uses heavy MEDIUM jargon: %q", out.WhatNext)
	}
	found := false
	for _, w := range out.Warnings {
		if strings.Contains(w, "java") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected primary_language java warning, got %#v", out.Warnings)
	}
}
