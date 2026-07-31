package mcpsvc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/workspacectx"
	"github.com/mark3labs/mcp-go/server"
)

// qualityBed is one query→context→impact→investigate (optional kickoff) probe for response UX.
type qualityBed struct {
	Name       string
	Query      string
	Symbol     string
	Kickoff    string // optional
	ExpectAny  []string
	ExpectPath string // if set, top query hit path should contain this (demotion honesty)
	SparseOK   bool   // PHP/Ruby/etc — expect sparse/self-only notes when BR thin
	HealthTrap bool   // must NOT expand HTTP health pack (Unity Health / Godot _ready)
	HardGate   bool   // nest/express/laravel — fail closed on locate/investigate fuse
}

type qualityDimScores struct {
	TokenSanity      int // /10
	RecoveryWhatNext int
	CollisionPath    int
	ProvenanceSparse int
	InvestigateFuse  int // /10 — investigate fused query→context→impact + what_next
	NoBleed          int
	Overall          int
}

type qualityBedResult struct {
	Bed              string           `json:"bed"`
	Scores           qualityDimScores `json:"scores"`
	QueryBytes       int              `json:"query_bytes"`
	ContextBytes     int              `json:"context_bytes"`
	ImpactBytes      int              `json:"impact_bytes"`
	InvestigateBytes int              `json:"investigate_bytes,omitempty"`
	KickoffBytes     int              `json:"kickoff_bytes,omitempty"`
	TotalBytes       int              `json:"total_bytes"`
	LocateHit        bool             `json:"locate_hit"`
	Notes            []string         `json:"notes,omitempty"`
	HasRecovery      bool             `json:"has_recovery_or_what_next"`
	HasCollision     bool             `json:"has_collision_or_path_hint"`
	HasProvenance    bool             `json:"has_provenance_or_sparse"`
	HasInvestigate   bool             `json:"has_investigate_fuse"`
	WrongRepoBleed   bool             `json:"wrong_repo_bleed"`
	HealthPackLeak   bool             `json:"health_pack_false_friend"`
	Ms               int64            `json:"ms"`
}

func TestMCPResponseQualityLiveBeds(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	root := findRepoRoot(t)
	bedsRootEnv := os.Getenv("CODEHELPER_TESTBEDS")

	beds := []qualityBed{
		{Name: "express", Query: "app.use middleware", Symbol: "createApplication", ExpectAny: []string{"createApplication", "application"}, ExpectPath: "lib/", Kickoff: "Where is app.use registered?", HardGate: true},
		{Name: "nest", Query: "ArticleService", Symbol: "ArticleService", ExpectAny: []string{"ArticleService", "article.service"}, ExpectPath: "src/", Kickoff: "What depends on ArticleService?", HardGate: true},
		{Name: "nest-starter", Query: "CatsService", Symbol: "CatsService", ExpectAny: []string{"CatsService", "cats.service"}, ExpectPath: "src/", Kickoff: "What depends on CatsService?", HardGate: true},
		{Name: "laravel", Query: "User model", Symbol: "User", ExpectAny: []string{"User", "Models"}, SparseOK: true, Kickoff: "Where is the User model?", HardGate: true},
		{Name: "fastapi", Query: "Depends list_users get_db", Symbol: "get_db", ExpectAny: []string{"list_users", "Depends", "get_db"}, ExpectPath: "docs_src/", SparseOK: true, Kickoff: "Where is Depends used with list_users and get_db?"},
		{Name: "godot", Query: "Player _ready", Symbol: "_ready", ExpectAny: []string{"player.gd", "_ready"}, HealthTrap: true, Kickoff: "Where is Player _ready?"},
		{Name: "unity", Query: "Health", Symbol: "Health", ExpectAny: []string{"Health", "Assets/Scripts"}, HealthTrap: true},
		{Name: "gin", Query: "Context.JSON", Symbol: "JSON", ExpectAny: []string{"JSON"}},
		{Name: "nextjs", Query: "useCounter CounterProvider", Symbol: "useCounter", ExpectAny: []string{"useCounter", "CounterProvider", "counter-context"}, ExpectPath: "app/context/", Kickoff: "How does useCounter work with CounterProvider?"},
		{Name: "nextjs-starter", Query: "Page greet", Symbol: "Page", ExpectAny: []string{"Page", "app/page", "greet"}, ExpectPath: "app/", Kickoff: "Where is the App Router Page?"},
		{Name: "spring", Query: "OwnerController PetService", Symbol: "OwnerController", ExpectAny: []string{"OwnerController", "PetService"}},
		{Name: "wordpress", Query: "ProbePlugin boot add_action init", Symbol: "boot", ExpectAny: []string{"ProbePlugin", "boot"}, SparseOK: true},
		{Name: "multi-repo-a", Query: "InventoryClient GetStock", Symbol: "GetStock", ExpectAny: []string{"InventoryClient", "GetStock"}},
		{Name: "svelte", Query: "Toggle toggle", Symbol: "toggle", ExpectAny: []string{"toggle", "Toggle"}},
	}

	reg, err := registry.Load()
	if err != nil {
		t.Fatal(err)
	}
	handlers := AllToolHandlers(reg)

	var results []qualityBedResult
	var bedsRootUsed string
	for _, b := range beds {
		bedPath, src := resolveQualityBedPath(root, bedsRootEnv, b.Name)
		if bedPath == "" {
			t.Logf("skip %s: not indexed", b.Name)
			continue
		}
		if bedsRootUsed == "" {
			bedsRootUsed = src
		}
		if b.Name == "nest" {
			b = nestQualityBedForRoot(bedPath, b)
			b.HardGate = true
		}
		if b.Name == "nextjs" {
			b = nextjsQualityBedForRoot(bedPath, b)
		}
		res := scoreQualityBed(t, handlers, bedPath, b)
		results = append(results, res)
		t.Logf("%s overall=%d/10 bytes=%d locate=%v investigate=%v notes=%v",
			b.Name, res.Scores.Overall, res.TotalBytes, res.LocateHit, res.HasInvestigate, res.Notes)
		if b.HardGate {
			assertPriorityQualityBed(t, b, res)
		}
	}
	if len(results) == 0 {
		t.Skip("no quality beds indexed (set CODEHELPER_TESTBEDS or stage .testbeds/active)")
	}
	if len(results) < 8 {
		t.Fatalf("need >=8 beds scored, got %d (set CODEHELPER_TESTBEDS or stage .testbeds/active)", len(results))
	}
	var priorityOK int
	for _, r := range results {
		switch r.Bed {
		case "express", "nest", "nest-starter", "laravel":
			priorityOK++
		}
	}
	if priorityOK < 2 {
		t.Fatalf("need >=2 of express/nest/nest-starter/laravel scored, got %d", priorityOK)
	}

	var sum float64
	for _, r := range results {
		sum += float64(r.Scores.Overall)
	}
	avg := sum / float64(len(results))
	t.Logf("average overall=%.2f over %d beds", avg, len(results))

	outDir := os.Getenv("CODEHELPER_QUALITY_REPORT")
	if outDir == "" {
		outDir = filepath.Join(root, ".testbeds", "reports", "mcp-response-quality-2026-07-25")
	}
	_ = os.MkdirAll(outDir, 0o755)
	payload := map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"beds_root":    bedsRootUsed,
		"average":      avg,
		"beds":         results,
	}
	raw, _ := json.MarshalIndent(payload, "", "  ")
	if err := os.WriteFile(filepath.Join(outDir, "scorecard.json"), raw, 0o644); err != nil {
		t.Fatalf("write scorecard: %v", err)
	}
	t.Logf("wrote %s", filepath.Join(outDir, "scorecard.json"))
}

func nestQualityBedForRoot(root string, b qualityBed) qualityBed {
	if _, err := os.Stat(filepath.Join(root, "src", "article", "article.service.ts")); err == nil {
		b.Query = "ArticleService"
		b.Symbol = "ArticleService"
		b.ExpectAny = []string{"ArticleService", "article.service"}
		b.Kickoff = "What depends on ArticleService?"
		return b
	}
	b.Query = "CatsService"
	b.Symbol = "CatsService"
	b.ExpectAny = []string{"CatsService", "cats.service"}
	b.Kickoff = "What depends on CatsService?"
	return b
}

func nextjsQualityBedForRoot(root string, b qualityBed) qualityBed {
	if _, err := os.Stat(filepath.Join(root, "lib", "greet.ts")); err == nil {
		b.Query = "Page greet"
		b.Symbol = "Page"
		b.ExpectAny = []string{"Page", "app/page", "greet"}
		b.ExpectPath = "app/"
		b.Kickoff = "Where is the App Router Page?"
		return b
	}
	b.Query = "useCounter CounterProvider"
	b.Symbol = "useCounter"
	b.ExpectAny = []string{"useCounter", "CounterProvider", "counter-context"}
	b.ExpectPath = "app/context/"
	b.Kickoff = "How does useCounter work with CounterProvider?"
	return b
}

// resolveQualityBedPath prefers CODEHELPER_TESTBEDS, then .testbeds/active,
// .eval-projects, and legacy .testbeds/real-oss. Requires a non-tiny graph.db
// so empty real-oss stubs do not score as live beds.
func resolveQualityBedPath(repoRoot, bedsRootEnv, name string) (absPath, source string) {
	aliases := []string{name}
	switch name {
	case "nest-starter":
		aliases = append(aliases, "nestjs-typescript-starter")
	case "spring":
		aliases = append(aliases, "spring-petclinic")
	}
	var roots []string
	if bedsRootEnv != "" {
		roots = append(roots, bedsRootEnv)
	}
	roots = append(roots,
		filepath.Join(repoRoot, ".testbeds", "active"),
		filepath.Join(repoRoot, ".eval-projects"),
		filepath.Join(repoRoot, ".testbeds", "real-oss"),
	)
	const minGraphBytes = 128 * 1024 // nest-starter stubs land ~180–240KiB
	for _, root := range roots {
		for _, alias := range aliases {
			cand := filepath.Join(root, alias)
			db := filepath.Join(cand, ".codehelper", "graph.db")
			fi, err := os.Stat(db)
			if err != nil || fi.Size() < minGraphBytes {
				continue
			}
			if _, err := os.Stat(filepath.Join(cand, ".codehelper", "meta.json")); err != nil {
				continue
			}
			return resolveBedRoot(cand), root
		}
	}
	return "", ""
}

func assertPriorityQualityBed(t *testing.T, b qualityBed, res qualityBedResult) {
	t.Helper()
	if !res.LocateHit {
		t.Errorf("%s hard gate: locate miss (expect any of %v)", b.Name, b.ExpectAny)
	}
	if !res.HasInvestigate {
		t.Errorf("%s hard gate: investigate did not fuse query→context→impact + what_next", b.Name)
	}
	if res.WrongRepoBleed {
		t.Errorf("%s hard gate: wrong-repo bleed", b.Name)
	}
	if res.Scores.Overall < 7 {
		t.Errorf("%s hard gate: overall=%d want >=7 notes=%v", b.Name, res.Scores.Overall, res.Notes)
	}
	if b.ExpectPath != "" && res.Scores.CollisionPath < 7 {
		t.Errorf("%s hard gate: collision/path score=%d notes=%v", b.Name, res.Scores.CollisionPath, res.Notes)
	}
}

func scoreQualityBed(t *testing.T, handlers map[string]server.ToolHandlerFunc, root string, b qualityBed) qualityBedResult {
	t.Helper()
	ctx := workspacectx.WithRoots(root)
	start := time.Now()
	out := qualityBedResult{Bed: b.Name}

	q := pairedCall(ctx, handlers, "query", map[string]any{
		"query": b.Query, "format": "json", "top_k": 8,
	})
	out.QueryBytes = len(q)

	c := pairedCall(ctx, handlers, "context", map[string]any{
		"name": b.Symbol, "format": "json",
	})
	out.ContextBytes = len(c)

	imp := pairedCall(ctx, handlers, "impact", map[string]any{
		"name": b.Symbol, "format": "json",
		"depth": 1, "max_candidates": 8, "include_tests": false,
	})
	out.ImpactBytes = len(imp)

	// investigate fuses query→context→impact(+test_impact). Prefer query= so the
	// locate step is exercised; target= alone would skip query.
	inv := pairedCall(ctx, handlers, "investigate", map[string]any{
		"query": b.Query, "format": "json",
	})
	out.InvestigateBytes = len(inv)

	var kick string
	if b.Kickoff != "" {
		kick = pairedCall(ctx, handlers, "kickoff", map[string]any{
			"task": b.Kickoff, "format": "json", "sections": "orient,reuse",
		})
		out.KickoffBytes = len(kick)
	}
	out.Ms = time.Since(start).Milliseconds()
	blob := q + "\n" + c + "\n" + imp + "\n" + inv + "\n" + kick
	out.TotalBytes = len(blob)
	out.LocateHit, _ = locateHits(blob, b.ExpectAny)

	ql := strings.ToLower(q)
	cl := strings.ToLower(c)
	il := strings.ToLower(imp)
	ivl := strings.ToLower(inv)
	kl := strings.ToLower(kick)
	all := strings.ToLower(blob)

	// --- health false friend ---
	if b.HealthTrap && strings.Contains(ql, "health/ready endpoint prompt expanded") {
		out.HealthPackLeak = true
		out.Notes = append(out.Notes, "health_pack_false_friend")
	}

	// --- wrong-repo bleed (expect bed symbols, not sym:codehelper: as sole hit) ---
	if out.LocateHit {
		strippedOK, _ := locateHits(stripCodehelperLeakLines(blob), b.ExpectAny)
		if !strippedOK {
			out.WrongRepoBleed = true
			out.Notes = append(out.Notes, "wrong_repo_bleed")
		}
	}
	if strings.Contains(all, "sym:codehelper:") && !strings.Contains(root, "codehelper"+string(filepath.Separator)+"internal") {
		// Mentions of host repo in nested bed responses are bleed if they displace bed gold.
		if out.WrongRepoBleed || (!out.LocateHit && strings.Count(all, "sym:codehelper:") > strings.Count(all, b.Name)) {
			out.WrongRepoBleed = true
		}
	}

	out.HasRecovery = strings.Contains(all, "recovery_hint") || strings.Contains(all, "what_next") ||
		strings.Contains(all, "recommended_next_tools") || strings.Contains(all, "suggested_fix")
	out.HasCollision = strings.Contains(ql, "collision") || strings.Contains(ql, "retrieval_note") ||
		strings.Contains(ql, "ambiguous") || strings.Contains(ql, "pass path=") ||
		strings.Contains(cl, "ambiguous") || strings.Contains(cl, "path=") ||
		(b.ExpectPath != "" && strings.Contains(ql, strings.ToLower(b.ExpectPath)))
	out.HasProvenance = strings.Contains(all, "provenance") || strings.Contains(all, "sparse") ||
		strings.Contains(all, "self-only") || strings.Contains(all, "self_only") ||
		strings.Contains(all, "name_only") || strings.Contains(all, "inferred") ||
		strings.Contains(il, "confidence") || strings.Contains(cl, "blast_radius")

	// investigate fuse: steps list + nested context/impact + agent what_next
	hasSteps := strings.Contains(ivl, `"steps"`) || strings.Contains(ivl, "steps:")
	hasCtxStep := strings.Contains(ivl, "context")
	hasImpStep := strings.Contains(ivl, "impact")
	hasWhatNext := strings.Contains(ivl, "what_next")
	invLocate, _ := locateHits(inv, b.ExpectAny)
	out.HasInvestigate = hasSteps && hasCtxStep && hasImpStep && hasWhatNext && invLocate && !strings.HasPrefix(inv, "tool_error:") && !strings.HasPrefix(inv, "missing tool:")
	if !out.HasInvestigate {
		out.Notes = append(out.Notes, "investigate_fuse_weak")
	}

	// Dimension scoring
	out.Scores.TokenSanity = scoreTokenSanity(out.QueryBytes, out.ContextBytes, out.ImpactBytes, out.InvestigateBytes, out.KickoffBytes)
	out.Scores.RecoveryWhatNext = scoreBoolBand(out.HasRecovery, true, 9, 4)
	if strings.Contains(kl, "what_next") || strings.Contains(cl, "what_next") || strings.Contains(il, "what_next") || strings.Contains(ivl, "what_next") {
		if out.Scores.RecoveryWhatNext < 10 {
			out.Scores.RecoveryWhatNext++
		}
	}
	out.Scores.CollisionPath = 7
	if b.ExpectPath != "" {
		if strings.Contains(ql, strings.ToLower(filepath.ToSlash(b.ExpectPath))) {
			out.Scores.CollisionPath = 9
		} else if out.HasCollision {
			out.Scores.CollisionPath = 7
			out.Notes = append(out.Notes, "path_hint_present_but_top_path_unclear")
		} else {
			out.Scores.CollisionPath = 5
			out.Notes = append(out.Notes, "missing_path_honesty")
		}
	} else if out.HasCollision {
		out.Scores.CollisionPath = 8
	}
	out.Scores.ProvenanceSparse = 6
	if out.HasProvenance {
		out.Scores.ProvenanceSparse = 8
	}
	if b.SparseOK {
		if strings.Contains(all, "sparse") || strings.Contains(all, "self-only") || strings.Contains(all, "confidence") {
			out.Scores.ProvenanceSparse = 9
		} else {
			out.Scores.ProvenanceSparse = 5
			out.Notes = append(out.Notes, "sparse_bed_missing_warning")
		}
	}
	out.Scores.InvestigateFuse = 4
	if out.HasInvestigate {
		out.Scores.InvestigateFuse = 9
		if strings.Contains(ivl, "test_impact") {
			out.Scores.InvestigateFuse = 10
		}
	} else if hasCtxStep || hasImpStep {
		out.Scores.InvestigateFuse = 6
	}
	out.Scores.NoBleed = 10
	if out.WrongRepoBleed {
		out.Scores.NoBleed = 2
	}
	if out.HealthPackLeak {
		out.Scores.CollisionPath = minInt(out.Scores.CollisionPath, 4)
		out.Scores.TokenSanity = minInt(out.Scores.TokenSanity, 5)
	}
	if !out.LocateHit {
		out.Notes = append(out.Notes, "locate_miss")
		for _, d := range []*int{&out.Scores.TokenSanity, &out.Scores.CollisionPath, &out.Scores.ProvenanceSparse, &out.Scores.InvestigateFuse} {
			*d = minInt(*d, 5)
		}
	}

	// Overall = weighted mean (investigate fuse is first-class for agent UX)
	out.Scores.Overall = (out.Scores.TokenSanity*2 + out.Scores.RecoveryWhatNext*2 +
		out.Scores.CollisionPath*2 + out.Scores.ProvenanceSparse + out.Scores.InvestigateFuse*2 +
		out.Scores.NoBleed*2) / 11
	return out
}

func scoreTokenSanity(q, c, imp, inv, kick int) int {
	// Soft budgets: query <12k, context <20k, impact <16k, investigate <28k, kickoff <18k; total <70k.
	score := 10
	if q > 14000 {
		score -= 3
	} else if q > 9000 {
		score -= 1
	}
	if c > 25000 {
		score -= 3
	} else if c > 16000 {
		score -= 1
	}
	if imp > 20000 {
		score -= 3
	} else if imp > 12000 {
		score -= 1
	}
	if inv > 32000 {
		score -= 3
	} else if inv > 22000 {
		score -= 1
	}
	if kick > 22000 {
		score -= 2
	} else if kick > 16000 {
		score -= 1
	}
	total := q + c + imp + inv + kick
	if total > 75000 {
		score -= 2
	} else if total > 55000 {
		score -= 1
	}
	if score < 1 {
		return 1
	}
	return score
}

func scoreBoolBand(ok, want bool, good, bad int) int {
	if ok == want {
		return good
	}
	return bad
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
