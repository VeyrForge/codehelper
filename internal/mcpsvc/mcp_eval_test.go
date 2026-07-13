//go:build !windows

package mcpsvc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/pkg/types"
	"github.com/mark3labs/mcp-go/mcp"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProjectContextReturnsRepoAndEntrypoints(t *testing.T) {
	base := t.TempDir()
	repoRoot := filepath.Join(base, "repo")
	otherRoot := filepath.Join(base, "other")
	reg := testRegistryWithRoots(repoRoot, otherRoot)
	if err := os.MkdirAll(filepath.Join(repoRoot, "cmd", "codehelper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(otherRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(repoRoot, "go.mod"), "module example.com/x\n")
	writeTestFile(t, filepath.Join(repoRoot, "cmd", "codehelper", "main.go"), "package main\nfunc main() {}\n")
	ctx := contextWithRoots(repoRoot)

	// project_context defaults to TOON text now; request json for the structured assertion.
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"format": "json"}
	res, err := projectContextHandler(reg)(ctx, req)
	if err != nil {
		t.Fatalf("projectContextHandler: %v", err)
	}
	if res.StructuredContent == nil {
		t.Fatal("expected structuredContent")
	}
	var out projectContextMCPResponse
	sb, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(sb, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Repo != "target" {
		t.Fatalf("repo: %q", out.Repo)
	}
	found := false
	for _, e := range out.KeyEntrypoints {
		if e == "go.mod" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected go.mod in key_entrypoints: %#v", out.KeyEntrypoints)
	}
	if len(out.RecommendedNextTools) == 0 {
		t.Fatal("expected recommended_next_tools")
	}
	if out.MCPToolCount != 60 {
		t.Fatalf("mcp_tool_count: got %d want 60", out.MCPToolCount)
	}
	if out.MCPParamKeys == "" || out.ToolContractPath != "AGENTS.md" {
		t.Fatalf("expected MCP catalog header, got count=%d keys=%q contract=%q", out.MCPToolCount, out.MCPParamKeys, out.ToolContractPath)
	}
	if len(out.MCPMainTools) == 0 {
		t.Fatal("expected mcp_main_tools")
	}
	if out.SelectionReason != "" {
		t.Fatalf("short default should omit selection_reason, got %q", out.SelectionReason)
	}
	if len(out.TopLevelDirectories) != 0 {
		t.Fatalf("short default should omit top_level_directories: %#v", out.TopLevelDirectories)
	}
}

func TestProjectContextVerbosityShortOmitsLayout(t *testing.T) {
	base := t.TempDir()
	repoRoot := filepath.Join(base, "repo")
	otherRoot := filepath.Join(base, "other")
	reg := testRegistryWithRoots(repoRoot, otherRoot)
	if err := os.MkdirAll(filepath.Join(repoRoot, "cmd", "codehelper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(otherRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(repoRoot, "go.mod"), "module example.com/x\n")
	writeTestFile(t, filepath.Join(repoRoot, "cmd", "codehelper", "main.go"), "package main\nfunc main() {}\n")
	ctx := contextWithRoots(repoRoot)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"format": "json", "verbosity": "short"}
	res, err := projectContextHandler(reg)(ctx, req)
	if err != nil {
		t.Fatalf("projectContextHandler: %v", err)
	}
	var short projectContextMCPResponse
	sb, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(sb, &short); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if short.Repo == "" {
		t.Fatal("expected repo in short response")
	}
	if len(short.TopLevelDirectories) != 0 {
		t.Fatalf("short should omit top_level_directories, got %#v", short.TopLevelDirectories)
	}
	if len(short.Dependencies) != 0 {
		t.Fatalf("short should omit dependencies, got %#v", short.Dependencies)
	}

	req.Params.Arguments = map[string]any{"format": "json", "verbosity": "detailed"}
	res, err = projectContextHandler(reg)(ctx, req)
	if err != nil {
		t.Fatalf("projectContextHandler detailed: %v", err)
	}
	var detailed projectContextMCPResponse
	db, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(db, &detailed); err != nil {
		t.Fatalf("unmarshal detailed: %v", err)
	}
	if len(detailed.TopLevelDirectories) == 0 {
		t.Fatalf("detailed should include top_level_directories, got %#v", detailed.TopLevelDirectories)
	}
	if detailed.SelectionReason != "matched_mcp_roots" {
		t.Fatalf("detailed selection_reason: %q", detailed.SelectionReason)
	}
	hasMain := false
	for _, e := range detailed.LikelyEntrypointFiles {
		if e == "cmd/codehelper/main.go" {
			hasMain = true
			break
		}
	}
	if !hasMain {
		t.Fatalf("expected cmd/codehelper/main.go in likely_entrypoint_files: %#v", detailed.LikelyEntrypointFiles)
	}
	if detailed.MCPToolsByGroup == nil || len(detailed.MCPToolsByGroup) == 0 {
		t.Fatal("detailed should include mcp_tools_by_group")
	}
}

func TestEnrichQueryToolResponseAddsProjectContextWhenStale(t *testing.T) {
	hits := []retrieval.RankedSymbol{{Symbol: types.Symbol{Path: "internal/x.go"}}}
	out := queryToolResponse{
		Hits: hitsView(hits, false),
		Freshness: freshness.Report{
			Stale:       true,
			StaleReason: "git HEAD advanced past indexed commit",
		},
		Warning: "index may be stale",
	}
	enrichQueryToolResponse(&out, hits)
	found := false
	for _, x := range out.RecommendedNextTools {
		if x == "project_context" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected project_context first when stale, got %#v", out.RecommendedNextTools)
	}
	// A single clear hit now yields the cheapest deep-dive (`context`) rather than a
	// static read_workspace_file — the next-step hint adapts to the result state.
	hasContext := false
	for _, x := range out.RecommendedNextTools {
		if x == "context" {
			hasContext = true
			break
		}
	}
	if !hasContext {
		t.Fatalf("expected context as the next tool for a clear hit, got %#v", out.RecommendedNextTools)
	}
}

func TestEnrichImpactResponseIncludesImpactRelatedTools(t *testing.T) {
	out := impactMCPResponse{
		Freshness: freshness.Report{},
	}
	enrichImpactResponse(&out, nil)
	has := false
	for _, x := range out.RecommendedNextTools {
		if x == "detect_changes" {
			has = true
			break
		}
	}
	if !has {
		t.Fatalf("expected detect_changes in %#v", out.RecommendedNextTools)
	}
}
