package mcpsvc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/workspacectx"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// pairedProbe is one methodology-lite locate task (mcp-eval-methodology §1.1 arms A/B).
type pairedProbe struct {
	Bed        string
	Kind       string // architecture_qa | fix_bug_orient | feature_orient
	Task       string
	Query      string
	Symbol     string
	ExpectAny  []string // path/name substrings; hit if any appear in arm output
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
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"methodology":  "mcp-eval-methodology.md §1.1 lite (arms A vs B, objective locate)",
		"beds_run":     ran,
		"pairs":        len(pairs),
		"wins_mcp":     winsMCP,
		"wins_baseline": winsBase,
		"ties":         ties,
		"results":      pairs,
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
	LocateHit  bool     `json:"locate_hit"`
	Ms         int64    `json:"ms"`
	ToolCalls  int      `json:"tool_calls"`
	RespBytes  int      `json:"resp_bytes"`
	Hits       []string `json:"hits,omitempty"`
	Preview    string   `json:"preview,omitempty"`
	Error      string   `json:"error,omitempty"`
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

	q := pairedCall(ctx, handlers, "query", map[string]any{
		"q": p.Query, "format": "json",
	})
	calls++
	parts = append(parts, q)

	c := pairedCall(ctx, handlers, "context", map[string]any{
		"name": p.Symbol, "format": "json",
	})
	calls++
	parts = append(parts, c)

	imp := pairedCall(ctx, handlers, "impact", map[string]any{
		"name": p.Symbol, "format": "json",
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
	if expectRepo != "" && expectRepo != "codehelper" {
		if strings.Contains(blob, "sym:codehelper:") ||
			strings.Contains(blob, `"repo":"codehelper"`) {
			m.Error = "wrong_repo_leak"
			m.LocateHit = false
		}
	}
	return m
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
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".php", ".rb", ".java", ".rs",
			".svelte", ".vue", ".kt", ".cs", ".ex", ".exs", ".md":
		default:
			return nil
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
	for _, e := range expect {
		if strings.Contains(e, "/") || strings.Contains(e, ".") {
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

func defaultPairedProbes() []pairedProbe {
	return []pairedProbe{
		{
			Bed: "nest", Kind: "architecture_qa", Task: "What depends on CatsService?",
			Query: "CatsService", Symbol: "CatsService",
			ExpectAny: []string{"CatsService", "cats.service", "cats.controller", "cats.module"},
			BaselineNeedle: "CatsService",
		},
		{
			Bed: "laravel", Kind: "feature_orient", Task: "Where is the User model?",
			Query: "User model", Symbol: "User",
			ExpectAny: []string{"User", "Models/User", "app/Models"},
			BaselineNeedle: "class User",
		},
		{
			Bed: "svelte", Kind: "architecture_qa", Task: "How does mount attach a component?",
			Query: "mount", Symbol: "mount",
			ExpectAny: []string{"mount"},
			BaselineNeedle: "mount",
		},
		{
			Bed: "express", Kind: "fix_bug_orient", Task: "Where is app.use middleware registered?",
			Query: "app.use middleware", Symbol: "createApplication",
			ExpectAny: []string{"app.use", "application.js", "createApplication"},
			BaselineNeedle: "app.use",
		},
		{
			Bed: "fastapi", Kind: "architecture_qa", Task: "Where is Depends used?",
			Query: "Depends", Symbol: "Depends",
			ExpectAny: []string{"Depends"},
			BaselineNeedle: "Depends",
		},
		{
			Bed: "axum", Kind: "architecture_qa", Task: "What depends on Router?",
			Query: "Router", Symbol: "Router",
			ExpectAny: []string{"Router"},
			BaselineNeedle: "Router",
		},
		{
			Bed: "fiber", Kind: "feature_orient", Task: "How does App.Use register middleware?",
			Query: "App.Use middleware", Symbol: "Listen",
			ExpectAny: []string{"Use", "Listen", "app.go"},
			BaselineNeedle: "func (app *App) Use",
		},
		{
			Bed: "gin", Kind: "architecture_qa", Task: "How does Context.JSON write a response?",
			Query: "Context.JSON", Symbol: "JSON",
			ExpectAny: []string{"JSON"},
			BaselineNeedle: "func (c *Context) JSON",
		},
		{
			Bed: "flask", Kind: "architecture_qa", Task: "How does Flask application boot?",
			Query: "Flask application class", Symbol: "Flask",
			ExpectAny: []string{"Flask"},
			BaselineNeedle: "class Flask",
		},
		{
			Bed: "djangorest", Kind: "architecture_qa", Task: "Where is APIView defined?",
			Query: "APIView", Symbol: "APIView",
			ExpectAny: []string{"APIView"},
			BaselineNeedle: "class APIView",
		},
		{
			Bed: "sinatra", Kind: "feature_orient", Task: "Where is Sinatra::Base?",
			Query: "Sinatra Base", Symbol: "Base",
			ExpectAny: []string{"Base", "sinatra"},
			BaselineNeedle: "class Base",
		},
		{
			Bed: "spring-petclinic", Kind: "architecture_qa", Task: "How does PetClinicApplication boot?",
			Query: "PetClinicApplication", Symbol: "PetClinicApplication",
			ExpectAny: []string{"PetClinicApplication"},
			BaselineNeedle: "PetClinicApplication",
		},
	}
}
