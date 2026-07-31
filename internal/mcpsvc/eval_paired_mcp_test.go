package mcpsvc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VeyrForge/codehelper/internal/indexer"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/workspacectx"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// pairedProbe is one methodology-lite locate task (mcp-eval-methodology §1.1 arms A/B).
type pairedProbe struct {
	Bed            string
	Kind           string // architecture_qa | fix_bug_orient | feature_orient
	Task           string
	Query          string
	Symbol         string
	ExpectAny      []string // path/name substrings; hit if any appear in arm output
	BaselineNeedle string
}

// TestPairedMCPLiteFixture always runs: same underspecified task with MCP graph
// tools (arm B) vs host-style file scan (arm A). No cloud LLM; objective locate.
func TestPairedMCPLiteFixture(t *testing.T) {
	reg, repo, _ := buildIndexedRepo(t, map[string]string{
		"svc/auth.go": "package svc\n\n// Authenticate validates a session token.\nfunc Authenticate(token string) bool {\n\treturn token != \"\"\n}\n",
		"cmd/main.go": "package main\n\nimport \"example.com/demo/svc\"\n\nfunc main() {\n\t_ = svc.Authenticate(\"t\")\n}\n",
	})
	handlers := AllToolHandlers(reg)
	probe := pairedProbe{
		Bed:            repo.Name,
		Kind:           "architecture_qa",
		Task:           "How does Authenticate get used?",
		Query:          "Authenticate",
		Symbol:         "Authenticate",
		ExpectAny:      []string{"Authenticate", "svc/auth.go", "cmd/main.go"},
		BaselineNeedle: "Authenticate",
	}
	pair := runPairedProbe(t, handlers, repo.RootPath, repo.Name, probe)
	if !pair.MCP.LocateHit {
		t.Fatalf("MCP arm should locate Authenticate: %+v", pair.MCP)
	}
	if pair.Winner == "baseline" {
		t.Fatalf("expected MCP win or tie on dense fixture, got baseline: %+v", pair)
	}
	t.Logf("fixture pair winner=%s mcp_hit=%v base_hit=%v mcp_ms=%d base_ms=%d",
		pair.Winner, pair.MCP.LocateHit, pair.Baseline.LocateHit, pair.MCP.Ms, pair.Baseline.Ms)

	if p := os.Getenv("CODEHELPER_PAIRED_REPORT"); p != "" {
		winsMCP, winsBase, ties := 0, 0, 0
		switch pair.Winner {
		case "mcp":
			winsMCP = 1
		case "baseline":
			winsBase = 1
		default:
			ties = 1
		}
		summary := map[string]any{
			"generated_at":  time.Now().UTC().Format(time.RFC3339),
			"methodology":   "mcp-eval-methodology.md §1.1 lite (arms A vs B, objective locate)",
			"mode":          "fixture",
			"beds_run":      1,
			"pairs":         1,
			"wins_mcp":      winsMCP,
			"wins_baseline": winsBase,
			"ties":          ties,
			"results":       []pairedResult{pair},
		}
		b, err := json.MarshalIndent(summary, "", "  ")
		if err != nil {
			t.Fatalf("marshal fixture report: %v", err)
		}
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatalf("write fixture paired report: %v", err)
		}
		t.Logf("wrote fixture paired report %s", p)
	}
}

// TestPairedMCPLiteTestbeds runs methodology-lite A/B probes across indexed
// beds when CODEHELPER_TESTBEDS (or repo .testbeds/) is present.
func TestPairedMCPLiteTestbeds(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	base := testbedsRoot()
	if base == "" {
		t.Skip("no testbeds root")
	}
	reg, err := registry.Load()
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	handlers := AllToolHandlers(reg)
	probes := defaultPairedProbes()
	var pairs []pairedResult
	ran := 0
	for _, p := range probes {
		root := filepath.Join(base, p.Bed)
		if _, err := os.Stat(filepath.Join(root, ".codehelper")); err != nil {
			t.Logf("skip %s: not indexed", p.Bed)
			continue
		}
		// Windows junctions are ModeSymlink + !IsDir under Lstat; WalkDir then
		// treats the bed root as a file and Arm A always misses. Resolve first.
		root = resolveBedRoot(root)
		if p.Bed == "nest" {
			p = nestPairedProbeForRoot(root)
		}
		if p.Bed == "nextjs" {
			p = nextjsPairedProbeForRoot(root)
		}
		// Bind the bed in-process so query/context hit this index, not a parent
		// monorepo that contains .testbeds/ (no Save — test-local only).
		_ = reg.Upsert(p.Bed, root, "", 2)
		ran++
		pairs = append(pairs, runPairedProbe(t, handlers, root, p.Bed, p))
	}
	if ran == 0 {
		t.Skip("no indexed beds for paired probes")
	}

	winsMCP, winsBase, ties := 0, 0, 0
	for _, pr := range pairs {
		switch pr.Winner {
		case "mcp":
			winsMCP++
		case "baseline":
			winsBase++
		default:
			ties++
		}
		t.Logf("%s/%s winner=%s mcp_hit=%v base_hit=%v mcp_ms=%d base_ms=%d mcp_bytes=%d",
			pr.Bed, pr.Kind, pr.Winner, pr.MCP.LocateHit, pr.Baseline.LocateHit,
			pr.MCP.Ms, pr.Baseline.Ms, pr.MCP.RespBytes)
		if !pr.MCP.LocateHit && pr.Kind != "feature_orient" {
			t.Errorf("%s/%s: MCP locate miss (hits=%v)", pr.Bed, pr.Kind, pr.MCP.Hits)
		}
	}

	summary := map[string]any{
		"generated_at":  time.Now().UTC().Format(time.RFC3339),
		"methodology":   "mcp-eval-methodology.md §1.1 lite (arms A vs B, objective locate)",
		"beds_run":      ran,
		"pairs":         len(pairs),
		"wins_mcp":      winsMCP,
		"wins_baseline": winsBase,
		"ties":          ties,
		"results":       pairs,
	}
	if p := os.Getenv("CODEHELPER_PAIRED_REPORT"); p != "" {
		b, _ := json.MarshalIndent(summary, "", "  ")
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatalf("write paired report: %v", err)
		}
		t.Logf("wrote paired report %s", p)
	}
	t.Logf("paired summary: mcp=%d baseline=%d ties=%d over %d pairs", winsMCP, winsBase, ties, len(pairs))
	if winsMCP+ties < winsBase {
		t.Errorf("MCP underperformed baseline on locate pairs: mcp=%d base=%d ties=%d", winsMCP, winsBase, ties)
	}
}

type armMetrics struct {
	LocateHit bool     `json:"locate_hit"`
	Ms        int64    `json:"ms"`
	ToolCalls int      `json:"tool_calls"`
	RespBytes int      `json:"resp_bytes"`
	Hits      []string `json:"hits,omitempty"`
	Preview   string   `json:"preview,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type pairedResult struct {
	Bed      string     `json:"bed"`
	Kind     string     `json:"kind"`
	Task     string     `json:"task"`
	MCP      armMetrics `json:"arm_b_mcp"`
	Baseline armMetrics `json:"arm_a_baseline"`
	Winner   string     `json:"winner"` // mcp | baseline | tie
	DeltaMs  int64      `json:"delta_ms_mcp_minus_base"`
}

func runPairedProbe(t *testing.T, handlers map[string]server.ToolHandlerFunc, root, expectRepo string, p pairedProbe) pairedResult {
	t.Helper()
	out := pairedResult{Bed: p.Bed, Kind: p.Kind, Task: p.Task}
	out.MCP = runMCPArm(t, handlers, root, expectRepo, p)
	out.Baseline = runBaselineArm(t, root, p)
	out.DeltaMs = out.MCP.Ms - out.Baseline.Ms
	out.Winner = pickPairedWinner(out.MCP, out.Baseline)
	return out
}

func runMCPArm(t *testing.T, handlers map[string]server.ToolHandlerFunc, root, expectRepo string, p pairedProbe) armMetrics {
	t.Helper()
	ctx := workspacectx.WithRoots(root)
	start := time.Now()
	var parts []string
	calls := 0

	// Scope via workspacectx.WithRoots(root). Do not pass repo= for stub beds —
	// they are analyzed into CODEHELPER_TESTBEDS and may not be in the global registry.
	q := pairedCall(ctx, handlers, "query", map[string]any{
		"query": p.Query, "format": "json", "top_k": 8,
	})
	calls++
	parts = append(parts, q)

	c := pairedCall(ctx, handlers, "context", map[string]any{
		"name": p.Symbol, "format": "json",
	})
	calls++
	parts = append(parts, c)

	// Agent-realistic impact: target= (canonical), shallow depth, shortlist only —
	// hubs like axum Router otherwise dump hundreds of dependents into resp_bytes.
	imp := pairedCall(ctx, handlers, "impact", map[string]any{
		"target": p.Symbol, "format": "json",
		"depth": 1, "max_candidates": 8, "include_tests": false,
	})
	calls++
	parts = append(parts, imp)

	blob := strings.Join(parts, "\n")
	m := armMetrics{
		Ms:        time.Since(start).Milliseconds(),
		ToolCalls: calls,
		RespBytes: len(blob),
		Preview:   truncateSmoke(blob, 400),
	}
	m.LocateHit, m.Hits = locateHits(blob, p.ExpectAny)
	if m.LocateHit && expectRepo != "" && expectRepo != "codehelper" {
		// Reject only when ExpectAny matches solely inside codehelper-labeled lines
		// (cross-repo bleed). Mentions of codehelper elsewhere must not fail the bed.
		if ok, hits := locateHits(stripCodehelperLeakLines(blob), p.ExpectAny); !ok {
			m.Error = "wrong_repo_leak"
			m.LocateHit = false
			m.Hits = hits
		} else {
			m.Hits = hits
		}
	}
	return m
}

func stripCodehelperLeakLines(blob string) string {
	var b strings.Builder
	for _, line := range strings.Split(blob, "\n") {
		if strings.Contains(line, "sym:codehelper:") ||
			strings.Contains(line, `"repo":"codehelper"`) ||
			strings.Contains(line, `"repo": "codehelper"`) {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func runBaselineArm(t *testing.T, root string, p pairedProbe) armMetrics {
	t.Helper()
	// Arm A: host-builtin style — walk source files and substring-match (no graph).
	start := time.Now()
	needle := p.BaselineNeedle
	if needle == "" {
		needle = p.Symbol
	}
	var hits []string
	var bytes int
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			name := ""
			if d != nil {
				name = d.Name()
			}
			if name == ".git" || name == ".codehelper" || name == "node_modules" ||
				name == "vendor" || name == "dist" || name == "build" || name == ".venv" {
				return filepath.SkipDir
			}
			return nil
		}
		base := strings.ToLower(filepath.Base(path))
		ext := strings.ToLower(filepath.Ext(path))
		relForHint, _ := filepath.Rel(root, path)
		relSlash := "/" + strings.ToLower(filepath.ToSlash(relForHint)) + "/"
		// Match indexer walk: devops basenames + path-hinted k8s/ansible YAML + protobuf.
		switch {
		case base == "dockerfile" || strings.HasPrefix(base, "dockerfile.") ||
			base == "makefile" || base == "gnumakefile" ||
			base == "docker-compose.yml" || base == "docker-compose.yaml" ||
			strings.HasPrefix(base, "compose.") && (strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".yaml")):
			// include
		case strings.Contains(relSlash, "/k8s/") || strings.Contains(relSlash, "/kubernetes/") ||
			strings.Contains(relSlash, "/manifests/") || strings.Contains(relSlash, "/playbooks/") ||
			strings.Contains(relSlash, "/roles/") ||
			base == "site.yml" || base == "site.yaml" || base == "playbook.yml" || base == "playbook.yaml" ||
			strings.Contains(base, "deployment") || strings.Contains(base, "ingress") ||
			(strings.Contains(base, "service") && (strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".yaml"))):
			// include path-hinted ops YAML
		default:
			switch ext {
			case ".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".php", ".rb", ".java", ".rs",
				".svelte", ".vue", ".astro", ".mdx", ".kt", ".kts", ".cs", ".ex", ".exs", ".md",
				".cpp", ".cc", ".cxx", ".h", ".hpp", ".hxx", ".gd", ".swift", ".dart",
				".zig", ".sol", ".clj", ".cljs", ".cljc", ".erl", ".hrl", ".fs", ".fsi", ".fsx",
				".r", ".pl", ".pm", ".t", ".ml", ".mli", ".hs", ".lhs",
				".scala", ".lua", ".frag", ".vert", ".glsl", ".hlsl", ".hlsli", ".tf", ".prisma",
				".proto", ".yml", ".yaml", ".ps1", ".psm1":
			default:
				return nil
			}
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		bytes += len(b)
		if strings.Contains(string(b), needle) {
			rel, _ := filepath.Rel(root, path)
			hits = append(hits, rel)
			if len(hits) >= 8 {
				return filepath.SkipAll
			}
		}
		return nil
	})
	blob := strings.Join(hits, "\n")
	m := armMetrics{
		Ms:        time.Since(start).Milliseconds(),
		ToolCalls: 1,
		RespBytes: bytes,
		Preview:   truncateSmoke(blob, 400),
	}
	// Baseline "locate" = found the needle in at least one file AND that path
	// overlaps expected gold (when gold paths are path-like).
	m.LocateHit, m.Hits = locateHits(blob+"\n"+needle, mergeExpect(p.ExpectAny, hits))
	if len(hits) == 0 {
		m.LocateHit = false
	} else if goldPathsOnly(p.ExpectAny) {
		m.LocateHit, m.Hits = locateHits(blob, p.ExpectAny)
	} else {
		// Symbol-name gold: any file hit counts as baseline locate.
		m.LocateHit = true
		m.Hits = hits
	}
	return m
}

func mergeExpect(expect, hits []string) []string {
	out := append([]string{}, expect...)
	out = append(out, hits...)
	return out
}

func goldPathsOnly(expect []string) bool {
	// Only path-shaped gold (has a slash) forces path overlap. Dotted symbol
	// names like app.use / cats.service must not trip this — otherwise Arm A
	// finds the needle but is scored a miss when Rel paths omit those tokens.
	for _, e := range expect {
		if strings.Contains(e, "/") || strings.Contains(e, `\`) {
			return true
		}
	}
	return false
}

func locateHits(blob string, expect []string) (bool, []string) {
	var found []string
	lower := strings.ToLower(blob)
	for _, e := range expect {
		if e == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(e)) {
			found = append(found, e)
		}
	}
	return len(found) > 0, found
}

func pickPairedWinner(mcp, base armMetrics) string {
	switch {
	case mcp.LocateHit && !base.LocateHit:
		return "mcp"
	case !mcp.LocateHit && base.LocateHit:
		return "baseline"
	case mcp.LocateHit && base.LocateHit:
		// Both located: prefer fewer agent-facing bytes (mcpbr efficiency),
		// then lower latency — SkillCI-style cost comparator.
		if mcp.RespBytes > 0 && base.RespBytes > 0 {
			if mcp.RespBytes*2 < base.RespBytes {
				return "mcp"
			}
			if base.RespBytes*2 < mcp.RespBytes {
				return "baseline"
			}
		}
		return "tie"
	default:
		return "tie"
	}
}

func pairedCall(ctx context.Context, handlers map[string]server.ToolHandlerFunc, tool string, args map[string]any) string {
	h, ok := handlers[tool]
	if !ok {
		return "missing tool:" + tool
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = tool
	req.Params.Arguments = args
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	res, err := h(cctx, req)
	if err != nil {
		return err.Error()
	}
	if res == nil {
		return ""
	}
	if res.IsError {
		return "tool_error:" + resultText(res)
	}
	return resultText(res)
}

func testbedsRoot() string {
	if v := os.Getenv("CODEHELPER_TESTBEDS"); v != "" {
		if st, err := os.Stat(v); err == nil && st.IsDir() {
			return v
		}
		return ""
	}
	base := filepath.Join("..", "..", ".testbeds")
	if abs, err := filepath.Abs(base); err == nil {
		base = abs
	}
	if st, err := os.Stat(base); err == nil && st.IsDir() {
		return base
	}
	return ""
}

// resolveBedRoot follows Windows junctions / symlinks so filepath.WalkDir can
// descend (shared with indexer.ResolveWalkRoot — analyze roots staged as
// .testbeds/real-oss junctions must not index 0 files).
func resolveBedRoot(path string) string {
	return indexer.ResolveWalkRoot(path)
}

// nestPairedProbeForRoot picks RealWorld ArticleService gold when present,
// otherwise the CatsService stub (CI minimal / offline).
func nestPairedProbeForRoot(root string) pairedProbe {
	article := filepath.Join(root, "src", "article", "article.service.ts")
	if _, err := os.Stat(article); err == nil {
		return pairedProbe{
			Bed: "nest", Kind: "architecture_qa", Task: "What depends on ArticleService?",
			Query: "ArticleService", Symbol: "ArticleService",
			ExpectAny:      []string{"ArticleService", "article.service", "article.controller", "article.module"},
			BaselineNeedle: "ArticleService",
		}
	}
	return pairedProbe{
		Bed: "nest", Kind: "architecture_qa", Task: "What depends on CatsService?",
		Query: "CatsService", Symbol: "CatsService",
		ExpectAny:      []string{"CatsService", "cats.service", "cats.controller", "cats.module"},
		BaselineNeedle: "CatsService",
	}
}

// nextjsPairedProbeForRoot picks App Router playground gold (useCounter) when
// dense, otherwise the Page/greet stub (CI minimal / offline).
func nextjsPairedProbeForRoot(root string) pairedProbe {
	greet := filepath.Join(root, "lib", "greet.ts")
	if _, err := os.Stat(greet); err == nil {
		return pairedProbe{
			Bed: "nextjs", Kind: "architecture_qa", Task: "Where is the App Router Page?",
			Query: "Page greet", Symbol: "Page",
			ExpectAny:      []string{"Page", "app/page", "greet", "layout"},
			BaselineNeedle: "function Page",
		}
	}
	return pairedProbe{
		Bed: "nextjs", Kind: "architecture_qa", Task: "How does useCounter work with CounterProvider?",
		Query: "useCounter CounterProvider", Symbol: "useCounter",
		ExpectAny:      []string{"useCounter", "CounterProvider", "counter-context"},
		BaselineNeedle: "function useCounter",
	}
}

func defaultPairedProbes() []pairedProbe {
	return []pairedProbe{
		{
			// Gold adapts at runtime: RealWorld ArticleService vs CI stub CatsService.
			Bed: "nest", Kind: "architecture_qa", Task: "What depends on ArticleService?",
			Query: "ArticleService", Symbol: "ArticleService",
			ExpectAny:      []string{"ArticleService", "article.service", "article.controller", "article.module"},
			BaselineNeedle: "ArticleService",
		},
		{
			Bed: "nest-starter", Kind: "architecture_qa", Task: "What depends on CatsService?",
			Query: "CatsService", Symbol: "CatsService",
			ExpectAny:      []string{"CatsService", "cats.service", "cats.controller", "cats.module"},
			BaselineNeedle: "CatsService",
		},
		{
			Bed: "laravel", Kind: "feature_orient", Task: "Where is the User model?",
			Query: "User model", Symbol: "User",
			ExpectAny:      []string{"User", "Models/User", "app/Models"},
			BaselineNeedle: "class User",
		},
		{
			Bed: "svelte", Kind: "architecture_qa", Task: "How does Toggle.toggle work?",
			Query: "Toggle toggle", Symbol: "toggle",
			ExpectAny:      []string{"toggle", "Toggle.svelte", "format"},
			BaselineNeedle: "function toggle",
		},
		{
			Bed: "vue", Kind: "architecture_qa", Task: "How does Greeter.greet work with ref/computed?",
			Query: "Greeter greet open label", Symbol: "greet",
			ExpectAny:      []string{"greet", "Greeter.vue", "defineProps", "helper", "open", "label"},
			BaselineNeedle: "function greet",
		},
		{
			Bed: "angular", Kind: "architecture_qa", Task: "What depends on HeroService?",
			Query: "HeroService", Symbol: "HeroService",
			ExpectAny:      []string{"HeroService", "HeroComponent", "HeroModule", "hero.service"},
			BaselineNeedle: "class HeroService",
		},
		{
			// Gold adapts at runtime: playground useCounter vs CI stub Page/greet.
			Bed: "nextjs", Kind: "architecture_qa", Task: "How does useCounter work with CounterProvider?",
			Query: "useCounter CounterProvider", Symbol: "useCounter",
			ExpectAny:      []string{"useCounter", "CounterProvider", "counter-context"},
			BaselineNeedle: "function useCounter",
		},
		{
			Bed: "nextjs-starter", Kind: "architecture_qa", Task: "Where is the App Router Page?",
			Query: "Page greet", Symbol: "Page",
			ExpectAny:      []string{"Page", "app/page", "greet", "layout"},
			BaselineNeedle: "function Page",
		},
		{
			Bed: "nuxt", Kind: "architecture_qa", Task: "How does defineEventHandler reach healthPayload?",
			Query: "defineEventHandler healthPayload useCounter", Symbol: "healthPayload",
			ExpectAny:      []string{"healthPayload", "defineEventHandler", "useCounter", "server/api", "composables"},
			BaselineNeedle: "function healthPayload",
		},
		{
			Bed: "sveltekit", Kind: "architecture_qa", Task: "How does +page.server load reach greet?",
			Query: "load greet healthPayload sveltekit", Symbol: "load",
			ExpectAny:      []string{"load", "greet", "healthPayload", "+page.server", "sveltekit"},
			BaselineNeedle: "function load",
		},
		{
			Bed: "remix", Kind: "architecture_qa", Task: "How does Remix loader reach greet?",
			Query: "loader action greet saveGreeting remix", Symbol: "loader",
			ExpectAny:      []string{"loader", "action", "greet", "saveGreeting", "app/routes"},
			BaselineNeedle: "function loader",
		},
		{
			Bed: "electron", Kind: "architecture_qa", Task: "How does ipcMain.handle reach handleGreet?",
			Query: "ipcMain handleGreet greet preload", Symbol: "handleGreet",
			ExpectAny:      []string{"handleGreet", "greet", "ipcMain", "preload", "electron"},
			BaselineNeedle: "function handleGreet",
		},
		{
			Bed: "deno", Kind: "architecture_qa", Task: "How does Deno.serve reach greet via route handlers?",
			Query: "handler route greetHandler healthHandler greet Deno.serve", Symbol: "handler",
			ExpectAny:      []string{"handler", "route", "greetHandler", "healthHandler", "greet", "Deno.serve"},
			BaselineNeedle: "function handler",
		},
		{
			Bed: "bun", Kind: "architecture_qa", Task: "How does Bun.serve reach greet via fetchHandler?",
			Query: "fetchHandler route greetHandler greet Bun.serve", Symbol: "fetchHandler",
			ExpectAny:      []string{"fetchHandler", "route", "greetHandler", "greet", "Bun.serve"},
			BaselineNeedle: "function fetchHandler",
		},
		{
			Bed: "cloudflare-worker", Kind: "architecture_qa", Task: "How does the Worker fetch handler call greet?",
			Query: "fetch greet workers", Symbol: "fetch",
			ExpectAny:      []string{"fetch", "greet", "workers", "index.ts"},
			BaselineNeedle: "async fetch",
		},
		{
			Bed: "astro", Kind: "architecture_qa", Task: "Where is getStaticPaths and Card client:load island?",
			Query: "getStaticPaths island:Card client:load", Symbol: "getStaticPaths",
			ExpectAny:      []string{"getStaticPaths", "Index.astro", "island:Card", "client"},
			BaselineNeedle: "getStaticPaths",
		},
		{
			Bed: "mdx", Kind: "architecture_qa", Task: "How does Intro densify Callout/Hint components?",
			Query: "Intro Callout Hint highlight fence", Symbol: "highlight",
			ExpectAny:      []string{"Intro", "Callout", "Hint", "highlight", "fence", "Intro.mdx"},
			BaselineNeedle: "function highlight",
		},
		{
			Bed: "express", Kind: "fix_bug_orient", Task: "Where is app.use middleware registered?",
			Query: "app.use middleware", Symbol: "createApplication",
			ExpectAny:      []string{"app.use", "application.js", "createApplication"},
			BaselineNeedle: "app.use",
		},
		{
			Bed: "cpp", Kind: "architecture_qa", Task: "How does Widget.resize call draw?",
			Query: "Widget resize draw", Symbol: "resize",
			ExpectAny:      []string{"Widget", "resize", "draw", "widget.cpp"},
			BaselineNeedle: "Widget::resize",
		},
		{
			Bed: "fastapi", Kind: "architecture_qa", Task: "Where is Depends used?",
			Query: "Depends list_users get_db", Symbol: "Depends",
			ExpectAny:      []string{"Depends", "list_users", "get_db", "UserService"},
			BaselineNeedle: "Depends",
		},
		{
			Bed: "axum", Kind: "architecture_qa", Task: "What depends on Router?",
			Query: "Router routing mod", Symbol: "Router",
			ExpectAny:      []string{"Router"},
			BaselineNeedle: "Router",
		},
		{
			Bed: "fiber", Kind: "feature_orient", Task: "How does App.Use register middleware?",
			Query: "App.Use", Symbol: "Use",
			ExpectAny:      []string{"Use", "Listen", "app.go"},
			BaselineNeedle: "func (app *App) Use",
		},
		{
			Bed: "echo", Kind: "architecture_qa", Task: "How does Echo routes reach HealthHandler?",
			Query: "HealthHandler GET Routes", Symbol: "HealthHandler",
			ExpectAny:      []string{"HealthHandler", "Routes", "GET", "ListUsers"},
			BaselineNeedle: "func HealthHandler",
		},
		{
			Bed: "chi", Kind: "architecture_qa", Task: "How does chi Routes reach HealthHandler?",
			Query: "HealthHandler Get Routes", Symbol: "HealthHandler",
			ExpectAny:      []string{"HealthHandler", "Routes", "ListUsers"},
			BaselineNeedle: "func HealthHandler",
		},
		{
			Bed: "beego", Kind: "architecture_qa", Task: "How does Beego Router reach UserController and AuthFilter?",
			Query: "UserController Router AuthFilter InsertFilter UserService HealthHandler Routes", Symbol: "UserController",
			ExpectAny:      []string{"UserController", "Router", "AuthFilter", "InsertFilter", "UserService", "HealthHandler", "Routes"},
			BaselineNeedle: "type UserController",
		},
		{
			Bed: "gin", Kind: "architecture_qa", Task: "How does Context.JSON write a response?",
			Query: "Context.JSON", Symbol: "JSON",
			ExpectAny:      []string{"JSON"},
			BaselineNeedle: "func (c *Context) JSON",
		},
		{
			Bed: "flask", Kind: "architecture_qa", Task: "How does Flask application boot?",
			Query: "Flask route list_users", Symbol: "Flask",
			ExpectAny:      []string{"Flask", "list_users", "route", "UserService"},
			BaselineNeedle: "class Flask",
		},
		{
			Bed: "djangorest", Kind: "architecture_qa", Task: "Where is APIView defined?",
			Query: "APIView UserViewSet", Symbol: "APIView",
			ExpectAny:      []string{"APIView", "UserViewSet", "list", "UserService"},
			BaselineNeedle: "class APIView",
		},
		{
			Bed: "sinatra", Kind: "feature_orient", Task: "Where is Sinatra::Base?",
			Query: "Sinatra Base", Symbol: "Sinatra",
			ExpectAny:      []string{"Sinatra", "Base", "sinatra", "lib/sinatra"},
			BaselineNeedle: "module Sinatra",
		},
		{
			Bed: "rails", Kind: "architecture_qa", Task: "How does routes reach UsersController#show?",
			Query: "UsersController show set_user", Symbol: "UsersController",
			ExpectAny:      []string{"UsersController", "show", "set_user", "users_controller"},
			BaselineNeedle: "class UsersController",
		},
		{
			Bed: "wordpress", Kind: "feature_orient", Task: "Where is ProbePlugin boot hooked?",
			Query: "ProbePlugin boot add_action init", Symbol: "boot",
			ExpectAny:      []string{"ProbePlugin", "boot", "probe-plugin", "add_action"},
			BaselineNeedle: "class ProbePlugin",
		},
		{
			Bed: "spring-petclinic", Kind: "architecture_qa", Task: "How does PetClinicApplication boot?",
			Query: "PetClinicApplication", Symbol: "PetClinicApplication",
			ExpectAny:      []string{"PetClinicApplication"},
			BaselineNeedle: "PetClinicApplication",
		},
		{
			Bed: "spring", Kind: "architecture_qa", Task: "How does OwnerController reach PetService?",
			Query: "OwnerController PetService", Symbol: "OwnerController",
			ExpectAny:      []string{"OwnerController", "PetService", "greet"},
			BaselineNeedle: "class OwnerController",
		},
		{
			Bed: "godot", Kind: "architecture_qa", Task: "What depends on Enemy.take_hit?",
			Query: "Enemy take_hit Player _ready HealthBar", Symbol: "take_hit",
			ExpectAny:      []string{"take_hit", "Enemy", "player.gd", "_ready", "HealthBar"},
			BaselineNeedle: "func take_hit",
		},
		{
			Bed: "unreal", Kind: "architecture_qa", Task: "What depends on UHealthComponent?",
			Query: "UHealthComponent BeginPlay ApplyDamage AMyGameCharacter", Symbol: "UHealthComponent",
			ExpectAny:      []string{"UHealthComponent", "BeginPlay", "ApplyDamage", "AMyGameCharacter", "MyGameCharacter"},
			BaselineNeedle: "class UHealthComponent",
		},
		{
			Bed: "unity", Kind: "architecture_qa", Task: "What depends on Health?",
			Query: "Health", Symbol: "Health",
			ExpectAny:      []string{"Health", "PlayerController", "Assets/Scripts"},
			BaselineNeedle: "class Health",
		},
		{
			Bed: "csharp", Kind: "architecture_qa", Task: "How do UsersController and MapGet reach UserService?",
			Query: "UsersController UserService MapGet Find Save", Symbol: "UsersController",
			ExpectAny:      []string{"UsersController", "UserService", "MapGet", "Find", "Controllers", "Program"},
			BaselineNeedle: "class UsersController",
		},
		{
			Bed: "swift", Kind: "architecture_qa", Task: "How does Greeter.greet call format?",
			Query: "Greeter greet format", Symbol: "greet",
			ExpectAny:      []string{"Greeter", "greet", "format", "Greeter.swift"},
			BaselineNeedle: "func greet",
		},
		{
			Bed: "elixir", Kind: "feature_orient", Task: "How does Demo.Greeter.greet reach Format.apply?",
			Query: "Demo.Greeter greet Format.apply normalize", Symbol: "greet",
			ExpectAny:      []string{"Demo.Greeter", "greet", "Format.apply", "Format", "greeter.ex"},
			BaselineNeedle: "def greet",
		},
		{
			Bed: "phoenix", Kind: "architecture_qa", Task: "How does PageController reach DashboardLive?",
			Query: "PageController DashboardLive router", Symbol: "PageController",
			ExpectAny:      []string{"PageController", "DashboardLive", "router.ex", "page_controller.ex"},
			BaselineNeedle: "defmodule DemoWeb.PageController",
		},
		{
			Bed: "dart", Kind: "architecture_qa", Task: "How does Greeter.greet call format?",
			Query: "Greeter greet format shout Auditable Helpers.tag", Symbol: "greet",
			ExpectAny:      []string{"Greeter", "greet", "format", "shout", "Auditable", "Helpers.tag", "greeter.dart"},
			BaselineNeedle: "String greet",
		},
		{
			Bed: "flutter", Kind: "architecture_qa", Task: "How does GoRouter reach HomeScreen?",
			Query: "HomeScreen appRouter GoRoute GreetingCard", Symbol: "HomeScreen",
			ExpectAny:      []string{"HomeScreen", "GreetingCard", "appRouter", "route:home"},
			BaselineNeedle: "class HomeScreen",
		},
		{
			Bed: "react-native", Kind: "architecture_qa", Task: "How does RootNavigator reach HomeScreen?",
			Query: "RootNavigator HomeScreen createNativeStackNavigator Greeting", Symbol: "HomeScreen",
			ExpectAny:      []string{"HomeScreen", "RootNavigator", "Stack", "Greeting"},
			BaselineNeedle: "export function HomeScreen",
		},
		{
			Bed: "zig", Kind: "architecture_qa", Task: "How does Greeter.greet call format?",
			Query: "Greeter greet shout format helpers.upper Tone Stats", Symbol: "greet",
			ExpectAny:      []string{"Greeter", "greet", "format", "shout", "helpers.upper", "Tone", "greeter.zig"},
			BaselineNeedle: "fn greet",
		},
		{
			Bed: "solidity", Kind: "architecture_qa", Task: "How does Greeter.greet call Helpers.format via IGreeter?",
			Query: "Greeter greet Helpers.format IGreeter", Symbol: "greet",
			ExpectAny:      []string{"Greeter", "greet", "Helpers.format", "format", "IGreeter", "Greeter.sol"},
			BaselineNeedle: "function greet",
		},
		{
			Bed: "clojure", Kind: "architecture_qa", Task: "How does greet call format?",
			Query: "greet format demo.greeter", Symbol: "greet",
			ExpectAny:      []string{"greet", "format", "demo.greeter", "greeter.clj"},
			BaselineNeedle: "(defn greet",
		},
		{
			Bed: "erlang", Kind: "architecture_qa", Task: "How does greet call format?",
			Query: "greet format greeter", Symbol: "greet",
			ExpectAny:      []string{"greet", "format", "greeter", "greeter.erl"},
			BaselineNeedle: "greet(Name)",
		},
		{
			Bed: "fsharp", Kind: "architecture_qa", Task: "How does Greeter.Greet call format?",
			Query: "Greeter Greet format", Symbol: "Greet",
			ExpectAny:      []string{"Greeter", "Greet", "format", "Greeter.fs"},
			BaselineNeedle: "member _.Greet",
		},
		{
			Bed: "r", Kind: "architecture_qa", Task: "How does greet call format?",
			Query: "greet format helpers", Symbol: "greet",
			ExpectAny:      []string{"greet", "format", "greeter.R", "helpers.R"},
			BaselineNeedle: "greet <- function",
		},
		{
			Bed: "perl", Kind: "architecture_qa", Task: "How does greet call format?",
			Query: "greet format Greeter Helpers", Symbol: "greet",
			ExpectAny:      []string{"greet", "format", "Greeter", "Greeter.pm"},
			BaselineNeedle: "sub greet",
		},
		{
			Bed: "ocaml", Kind: "architecture_qa", Task: "How does Greeter.greet call format?",
			Query: "Greeter greet format", Symbol: "greet",
			ExpectAny:      []string{"Greeter", "greet", "format", "greeter.ml"},
			BaselineNeedle: "let greet",
		},
		{
			Bed: "haskell", Kind: "architecture_qa", Task: "How does greet call format?",
			Query: "greet format Greeter Helpers", Symbol: "greet",
			ExpectAny:      []string{"greet", "format", "Greeter", "Greeter.hs"},
			BaselineNeedle: "greet name =",
		},
		{
			Bed: "shaders", Kind: "architecture_qa", Task: "Where does main call tonemap?",
			Query: "main tonemap ApplyFog", Symbol: "main",
			ExpectAny:      []string{"main", "tonemap", "ApplyFog", "post.frag", "Water.hlsl"},
			BaselineNeedle: "tonemap",
		},
		{
			Bed: "terraform", Kind: "architecture_qa", Task: "How does aws_instance.web use module.vpc?",
			Query: "aws_instance.web module.vpc data.aws_ami", Symbol: "aws_instance.web",
			ExpectAny:      []string{"aws_instance.web", "module.vpc", "data.aws_ami", "main.tf"},
			BaselineNeedle: "resource \"aws_instance\" \"web\"",
		},
		{
			Bed: "devops", Kind: "architecture_qa", Task: "Where is the web compose service and runtime stage?",
			Query: "web api runtime builder test build", Symbol: "web",
			ExpectAny:      []string{"web", "api", "runtime", "builder", "docker-compose.yml", "Dockerfile", "Makefile"},
			BaselineNeedle: "web:",
		},
		{
			Bed: "kubernetes", Kind: "architecture_qa", Task: "Where does api-ingress reach the api service?",
			Query: "api-ingress api Deployment Service Ingress", Symbol: "api-ingress",
			ExpectAny:      []string{"api-ingress", "api", "deployment.yaml", "service.yaml", "ingress.yaml"},
			BaselineNeedle: "api-ingress",
		},
		{
			Bed: "ansible", Kind: "architecture_qa", Task: "How does Configure web use the web role?",
			Query: "Configure web Ensure nginx Install packages", Symbol: "Configure web",
			ExpectAny:      []string{"Configure web", "web", "Ensure nginx running", "Install packages", "site.yml"},
			BaselineNeedle: "Configure web",
		},
		{
			Bed: "powershell", Kind: "architecture_qa", Task: "How does Write-Info call Deploy-App?",
			Query: "Write-Info Deploy-App Prepare-Env", Symbol: "Write-Info",
			ExpectAny:      []string{"Write-Info", "Deploy-App", "Prepare-Env", "deploy.ps1"},
			BaselineNeedle: "function Write-Info",
		},
		{
			Bed: "protobuf", Kind: "architecture_qa", Task: "Where is UserService.GetUser rpc?",
			Query: "UserService GetUser ListUsers", Symbol: "GetUser",
			ExpectAny:      []string{"UserService", "GetUser", "ListUsers", "user.proto"},
			BaselineNeedle: "rpc GetUser",
		},
		{
			Bed: "prisma", Kind: "architecture_qa", Task: "How does listUsers reach User/Post via prisma include?",
			Query: "listUsers User Post Profile findMany prisma getUsers", Symbol: "listUsers",
			ExpectAny:      []string{"listUsers", "User", "Post", "Profile", "findMany", "getUsers", "users.ts", "schema.prisma"},
			BaselineNeedle: "prisma.user.findMany",
		},
		{
			Bed: "typeorm", Kind: "architecture_qa", Task: "How does listUsers reach User via getRepository relations?",
			Query: "listUsers User Post Profile getRepository getUsers", Symbol: "listUsers",
			ExpectAny:      []string{"listUsers", "User", "Post", "Profile", "getUsers", "users.service.ts"},
			BaselineNeedle: "getRepository(User)",
		},
		{
			Bed: "drizzle", Kind: "architecture_qa", Task: "How does listUsers reach users via db.query findMany?",
			Query: "listUsers users posts findMany db.query", Symbol: "listUsers",
			ExpectAny:      []string{"listUsers", "users", "posts", "findMany", "users.ts", "schema.ts"},
			BaselineNeedle: "db.query.users.findMany",
		},
		{
			Bed: "hibernate", Kind: "architecture_qa", Task: "How does OwnerService.find use OwnerRepository and EntityManager?",
			Query: "OwnerService find OwnerRepository EntityManager", Symbol: "OwnerService",
			ExpectAny:      []string{"OwnerService", "OwnerRepository", "EntityManager", "find", "OwnerService.java"},
			BaselineNeedle: "class OwnerService",
		},
		{
			Bed: "swiftui", Kind: "architecture_qa", Task: "How does HomeView reach DetailView?",
			Query: "HomeView DetailView GreetingView NavigationLink", Symbol: "HomeView",
			ExpectAny:      []string{"HomeView", "DetailView", "GreetingView", "HomeView.swift"},
			BaselineNeedle: "struct HomeView",
		},
		{
			Bed: "capacitor", Kind: "architecture_qa", Task: "How does AppRoutes reach HomePage?",
			Query: "AppRoutes HomePage SettingsPage", Symbol: "AppRoutes",
			ExpectAny:      []string{"AppRoutes", "HomePage", "SettingsPage", "AppRoutes.tsx"},
			BaselineNeedle: "export function AppRoutes",
		},
		{
			Bed: "lua", Kind: "architecture_qa", Task: "How does greet call format?",
			Query: "greet format", Symbol: "greet",
			ExpectAny:      []string{"greet", "format", "greeter.lua"},
			BaselineNeedle: "function greet",
		},
		{
			Bed: "scala", Kind: "architecture_qa", Task: "How does Greeter.greet work?",
			Query: "Greeter greet format LoggedGreeter", Symbol: "Greeter",
			ExpectAny:      []string{"Greeter", "greet", "format", "LoggedGreeter", "Greeter.scala"},
			BaselineNeedle: "object Greeter",
		},
		{
			Bed: "kotlin", Kind: "architecture_qa", Task: "How does OwnerController.greet use PetService?",
			Query: "OwnerController PetService greet", Symbol: "OwnerController",
			ExpectAny:      []string{"OwnerController", "PetService", "greet"},
			BaselineNeedle: "class OwnerController",
		},
		{
			Bed: "multi-repo-a", Kind: "architecture_qa", Task: "Where is InventoryClient.GetStock?",
			Query: "InventoryClient GetStock", Symbol: "GetStock",
			ExpectAny:      []string{"InventoryClient", "GetStock"},
			BaselineNeedle: "func (c *InventoryClient) GetStock",
		},
		{
			Bed: "multi-repo-b", Kind: "architecture_qa", Task: "Where is CheckoutService.PlaceOrder?",
			Query: "CheckoutService PlaceOrder", Symbol: "PlaceOrder",
			ExpectAny:      []string{"CheckoutService", "PlaceOrder"},
			BaselineNeedle: "func (s *CheckoutService) PlaceOrder",
		},
	}
}
