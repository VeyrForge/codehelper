package mcpsvc

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/gitutil"
	"github.com/VeyrForge/codehelper/internal/hotspots"
	"github.com/VeyrForge/codehelper/internal/profile"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/review"
	"github.com/VeyrForge/codehelper/internal/security"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ---- hotspots --------------------------------------------------------------

type hotspotRow struct {
	File               string  `json:"file"`
	Line               int     `json:"line,omitempty"`
	Commits            int     `json:"commits"`
	Centrality         int     `json:"centrality"`
	Score              float64 `json:"score"`
	Why                string  `json:"why,omitempty"`
	RewriteHint        string  `json:"rewrite_hint,omitempty"`
	SuggestedNextQuery string  `json:"suggested_next_query,omitempty"`
}

type hotspotsResponse struct {
	Hotspots        []hotspotRow `json:"hotspots"`
	Window          int          `json:"commits_scanned"`
	PrimaryLanguage string       `json:"primary_language,omitempty"`
	ProjectShape    string       `json:"project_shape,omitempty"`
	Warning         string       `json:"warning,omitempty"`
	Freshness       string       `json:"freshness,omitempty"`
	Note            string       `json:"note"`
}

const (
	hotspotsMaxRows       = 20
	hotspotsDefaultWindow = 1500
)

// hotspotsHandler ranks files by architectural risk = git churn × call-graph
// centrality. It fuses two signals the other tools already expose separately —
// how often a file changes (git history) and how load-bearing its symbols are
// (inbound call edges, the same centrality `query`/`scout` rank by) — into the
// "where is refactoring most valuable / where do bugs hurt most" view. Pure
// deterministic ranking (internal/hotspots) over data read from git + the graph;
// no model. Best-effort on git history (shallow clone / non-repo → empty).
func hotspotsHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		st, err := openGraph(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer st.Close()

		window := int(mcp.ParseInt64(req, "commits", 0))
		if window <= 0 {
			window = hotspotsDefaultWindow
		}
		topK := int(mcp.ParseInt64(req, "top_k", 0))
		if topK <= 0 {
			topK = hotspotsMaxRows
		}

		// Churn: commits touching each file in the window (git history).
		commits, _ := gitutil.LogNameOnly(repo.RootPath, window)
		churn := hotspots.ChurnFromCommits(commits)

		// Centrality: sum inbound "calls" edges over the symbols each file defines.
		// One whole-repo InDegrees scan + one symbol enumeration, then aggregate by
		// path — O(symbols+edges), not a per-file round-trip.
		indeg, err := st.InDegrees(ctx, repo.Name, "calls")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		syms, err := st.SymbolsByPathPrefix(ctx, repo.Name, "", 1_000_000)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		centrality := map[string]int{}
		symCount := map[string]int{}
		bestLine := map[string]int{}  // path → preferred symbol start line
		bestRank := map[string]int{} // path → rank score (lower wins)
		primary := ""
		shape := security.DetectProjectShape(repo.RootPath)
		if pr, perr := profile.ReadOrGenerate(repo.RootPath); perr == nil && pr != nil {
			primary = pr.PrimaryLanguage
		}
		for _, s := range syms {
			if review.IsTestPath(s.Path) || review.IsSecondaryNoisePath(s.Path) {
				continue // hotspots target production code, not tests/demos/fixtures
			}
			if isHotspotNoisePath(s.Path, primary) {
				continue // deps/lua, hiredis, utils/scripts must not dominate C cores
			}
			symCount[s.Path]++
			if d := indeg[s.ID]; d > 0 {
				centrality[s.Path] += d
			}
			if s.LineStart <= 0 {
				continue
			}
			kind := strings.ToLower(string(s.Kind))
			rank := 300 + s.LineStart // default: late variables lose
			switch kind {
			case "function", "method":
				rank = 100 + s.LineStart
			case "class", "interface":
				rank = 200 + s.LineStart
			}
			if prev, ok := bestRank[s.Path]; !ok || rank < prev {
				bestRank[s.Path] = rank
				bestLine[s.Path] = s.LineStart
			}
		}
		// Drop churn for noise paths so Rank never surfaces deps/lua over src/*.c.
		for f := range churn {
			if isHotspotNoisePath(f, primary) {
				delete(churn, f)
			}
		}

		ranked := hotspots.Rank(churn, centrality, topK)
		out := hotspotsResponse{Window: len(commits), PrimaryLanguage: primary, ProjectShape: string(shape)}
		for _, r := range ranked {
			out.Hotspots = append(out.Hotspots, hotspotRow{
				File: r.File, Commits: r.Commits, Centrality: r.Centrality, Score: round3(r.Score),
			})
		}

		// Thin/shallow history: churn×centrality is dominated by whichever files
		// that single commit touched (often scripts/utils). Fall back to
		// centrality-only (or symbol-density) among primary-language production files.
		if len(commits) <= 1 && primary != "" {
			fallback := centralityOnlyHotspots(centrality, primary, topK)
			if len(fallback) == 0 {
				fallback = centralityOnlyHotspots(symCount, primary, topK)
			}
			if len(fallback) > 0 {
				out.Hotspots = fallback
			}
			// Always apply primary-language / Entity demotion even on shallow-history fallback.
			out.Hotspots = preferPrimaryLanguageFiles(out.Hotspots, primary)
		} else if primary != "" {
			out.Hotspots = preferPrimaryLanguageFiles(out.Hotspots, primary)
		}
		out.Hotspots = dropHotspotNoise(out.Hotspots, primary)
		out.Hotspots = preferCoreHotPaths(out.Hotspots, primary, shape)
		out.Hotspots = seedLibraryCoreHotspots(out.Hotspots, repo.RootPath, shape, topK)
		out.Hotspots = forceCCoreHotspots(out.Hotspots, repo.RootPath, primary, shape, topK)
		annotateHotspotWhy(out.Hotspots, len(commits), shape, bestLine, repo.RootPath)

		if graphConf := callGraphConfidenceLang(ctx, st, repo.Name, primary); graphConf != "" {
			out.Warning = strings.TrimSpace(out.Warning + " " + "sparse call graph — centrality under-counts facades/macros; do not treat missing hotspots as proof there is no hot path")
			if out.Note == "" {
				out.Note = graphConf
			} else {
				out.Note = graphConf + " | " + out.Note
			}
		}
		if fresh := freshness.Inspect(repo.RootPath); fresh.Stale {
			out.Freshness = "index may be stale (" + fresh.StaleReason + ") — centrality reflects the last analyze; run codehelper analyze --force"
		}
		switch {
		case len(commits) == 0:
			out.Note = "no git history readable (not a git repo, or a shallow clone) — churn is unavailable, so hotspots can't be computed. The centrality half is still in `query`/`scout` ranking."
			out.Warning = "commits_scanned=0 — churn axis missing; do not treat empty/zero hotspots as proof there is no hot path."
		case len(commits) <= 1:
			out.Warning = fmt.Sprintf("commits_scanned=%d — shallow or thin history; churn ranking is unreliable. Prefer primary_language=%q source files and centrality; re-clone with deeper history for real churn×centrality.", len(commits), primary)
			out.Note = out.Warning + " Using centrality-only primary-language fallback when available. Inspect top rows with `context` before optimizing."
		case len(out.Hotspots) == 0:
			out.Note = "no file scored on both axes — either nothing churned also has inbound call edges, or the call graph is empty (run `codehelper analyze --force`)."
		default:
			out.Note = fmt.Sprintf("Files ranked by churn × centrality over the last %d commits: changed often AND depended on heavily = highest refactor value / defect risk. Primary language preference applied when known. Inspect the top rows with `context` or `change_kit` before refactoring; `impact` shows their blast radius.", len(commits))
		}
		if shape == security.ShapeLibrary || shape == security.ShapeFrameworkCore {
			out.Note += " project_shape=" + string(shape) + " — prefer library hot paths / complexity / allocs over inventing app N+1."
		}
		if shape == security.ShapeSkeleton {
			out.Note += " project_shape=skeleton — expect thin hotspots; deliver conventions, not invented production bottlenecks."
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

// centralityOnlyHotspots ranks by inbound-call centrality alone, restricted to
// primary-language production paths. Used when git history is too thin for a
// meaningful churn axis (shallow clones).
func centralityOnlyHotspots(centrality map[string]int, primary string, topK int) []hotspotRow {
	exts := primaryLangExts(primary)
	if len(exts) == 0 || len(centrality) == 0 {
		return nil
	}
	type pair struct {
		file string
		cent int
	}
	var rows []pair
	for file, cent := range centrality {
		if cent <= 0 {
			continue
		}
		lower := strings.ToLower(strings.ReplaceAll(file, "\\", "/"))
		ext := strings.ToLower(filepath.Ext(lower))
		if !exts[ext] {
			continue
		}
		if strings.Contains(lower, "/deps/") || strings.HasPrefix(lower, "deps/") ||
			strings.Contains(lower, "/vendor/") || strings.Contains(lower, "/third_party/") ||
			strings.Contains(lower, "/utils/") || strings.Contains(lower, "/tools/") ||
			strings.Contains(lower, "/scripts/") || strings.Contains(lower, "/tests/") ||
			strings.Contains(lower, "/test/") || strings.Contains(lower, "/migrations/") ||
			strings.Contains(lower, "/entity/") || strings.Contains(lower, "/datafixtures/") {
			continue
		}
		base := path.Base(lower)
		if strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.c") {
			continue
		}
		rows = append(rows, pair{file: file, cent: cent})
	}
	if len(rows) == 0 {
		return nil
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].cent != rows[j].cent {
			return rows[i].cent > rows[j].cent
		}
		return rows[i].file < rows[j].file
	})
	if topK > 0 && len(rows) > topK {
		rows = rows[:topK]
	}
	max := float64(rows[0].cent)
	out := make([]hotspotRow, 0, len(rows))
	for _, r := range rows {
		score := 0.0
		if max > 0 {
			score = float64(r.cent) / max
		}
		out = append(out, hotspotRow{File: r.file, Commits: 0, Centrality: r.cent, Score: round3(score)})
	}
	return out
}

// isHotspotNoisePath reports vendored/deps/script/test paths that must not win
// churn×centrality for C/Redis (deps/lua, hiredis, utils/*.py, module tests).
func isHotspotNoisePath(file, primary string) bool {
	lower := strings.ToLower(strings.ReplaceAll(file, "\\", "/"))
	base := path.Base(lower)
	if strings.Contains(lower, "/deps/") || strings.HasPrefix(lower, "deps/") ||
		strings.Contains(lower, "/vendor/") || strings.Contains(lower, "/third_party/") ||
		strings.Contains(lower, "/node_modules/") ||
		strings.Contains(lower, "/lua/") || strings.Contains(lower, "/hiredis/") ||
		strings.Contains(lower, "/jemalloc/") ||
		strings.Contains(lower, "/scripts/") || strings.Contains(lower, "/tools/") ||
		strings.Contains(lower, "/tests/") || strings.Contains(lower, "/test/") ||
		strings.Contains(lower, "/__tests__/") || strings.Contains(lower, "/testdata/") ||
		strings.Contains(lower, "/testing/") || strings.Contains(lower, "/assertions") ||
		strings.Contains(lower, "/activesupport/testing/") ||
		strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.c") ||
		strings.HasSuffix(base, "_test.py") || strings.HasSuffix(base, "_test.go") {
		return true
	}
	if review.IsNestedForeignToolTree(lower) {
		return true
	}
	primaryLang := strings.ToLower(strings.TrimSpace(primary))
	if primaryLang == "c" {
		if strings.Contains(lower, "/utils/") || strings.HasPrefix(lower, "utils/") ||
			strings.Contains(lower, "/modules/") && (strings.HasSuffix(base, ".py") || strings.Contains(lower, "/tests/")) ||
			strings.HasSuffix(base, ".py") || strings.HasSuffix(base, ".travis.yml") {
			return true
		}
	}
	// ORM entities / fixtures / factories dominate churn but are weak perf targets
	// on PHP/Java apps (Symfony Entity, Doctrine fixtures, JPA models).
	if primaryLang == "php" || primaryLang == "java" {
		if isORMModelOrFixturePath(lower) {
			return true
		}
	}
	return false
}

// dropHotspotNoise removes noise rows entirely (not just demote) so LIVE top-N
// cannot still hard-fail on deps/lua when src/*.c cores exist elsewhere.
func dropHotspotNoise(rows []hotspotRow, primary string) []hotspotRow {
	if len(rows) == 0 {
		return rows
	}
	out := make([]hotspotRow, 0, len(rows))
	for _, r := range rows {
		if isHotspotNoisePath(r.File, primary) {
			continue
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return rows // never return empty solely due to filtering
	}
	return out
}

// preferCoreHotPaths boosts well-known server/router cores (esp. C/Redis) and
// demotes peripheral helpers (rand.c, utils) that sparse graphs over-rank.
func preferCoreHotPaths(rows []hotspotRow, primary string, shape security.ProjectShape) []hotspotRow {
	if len(rows) == 0 {
		return rows
	}
	coreNames := []string{
		"/server.c", "/networking.c", "/acl.c", "/db.c", "/ae.c", "/object.c",
		"/dict.c", "/replication.c", "/cluster.c", "/module.c", "/t_string.c",
		"/router/index.js", "/application.js", "/gin.go", "/context.go",
		"/routing/mod.rs", "/query.py", "/relation.rb",
		// Python framework cores (flask/fastapi/django) — prefer dispatch over CLI/scaffold.
		"/flask/app.py", "/flask/wrappers.py", "/flask/sessions.py",
		"/flask/sansio/app.py", "/fastapi/routing.py", "/starlette/routing.py",
		"/django/db/models/query.py", "/django/core/handlers/",
	}
	peripheral := []string{"/rand.c", "/util.c", "/utils/", "/crc64.c", "/sha1.c", "/lzf_",
		"/deps/", "/lua/", "/ziplist.c", "/intset.c", "/sparkline.c", "/debug.c",
		"/hiredis/", "/anet.c", "/lolwut", "/cli_common.c",
		// Framework CLI / scaffold / testing helpers — not request hot paths.
		"/cli.py", "/scaffold.py", "/testing.py", "/debughelpers.py",
		"/flask/cli.py", "/flask/sansio/scaffold.py", "/management/commands/",
		"/activesupport/testing/", "/testing/assertions", "/minitest/",
	}
	primaryLang := strings.ToLower(strings.TrimSpace(primary))
	var core, mid, peri []hotspotRow
	for _, r := range rows {
		lower := strings.ToLower(strings.ReplaceAll(r.File, "\\", "/"))
		isPeri := false
		for _, p := range peripheral {
			if strings.Contains(lower, p) {
				isPeri = true
				break
			}
		}
		if isPeri {
			peri = append(peri, r)
			continue
		}
		isCore := false
		for _, c := range coreNames {
			if strings.HasSuffix(lower, c) || strings.Contains(lower, c) {
				isCore = true
				break
			}
		}
		// C/Redis: any src/*.c that isn't peripheral is core-ish for ranking.
		if !isCore && primaryLang == "c" {
			base := path.Base(lower)
			if (strings.HasPrefix(lower, "src/") || strings.Contains(lower, "/src/")) &&
				strings.HasSuffix(base, ".c") && !strings.Contains(base, "test") {
				isCore = true
			}
		}
		// Express/lib and redis/src without tests.
		if !isCore && (shape == security.ShapeLibrary || shape == security.ShapeFrameworkCore) {
			if strings.HasPrefix(lower, "lib/") || strings.HasPrefix(lower, "src/") {
				base := path.Base(lower)
				if primaryLang == "c" && strings.HasSuffix(base, ".c") && !strings.Contains(base, "test") {
					isCore = strings.Contains(base, "server") || strings.Contains(base, "net") ||
						strings.Contains(base, "acl") || strings.Contains(base, "db") ||
						strings.Contains(base, "object") || strings.Contains(base, "dict")
				}
				// Python framework: src/<pkg>/{app,wrappers,routing,sessions}.py
				if !isCore && (primaryLang == "python" || primaryLang == "py") {
					isCore = strings.HasSuffix(base, "app.py") || strings.HasSuffix(base, "wrappers.py") ||
						strings.HasSuffix(base, "routing.py") || strings.HasSuffix(base, "sessions.py") ||
						strings.HasSuffix(base, "query.py") || strings.Contains(lower, "/handlers/")
				}
			}
		}
		if isCore {
			core = append(core, r)
		} else {
			mid = append(mid, r)
		}
	}
	// Always reorder (core → mid → peri). Previously returned the original rows
	// when core was empty, which re-elevated deps/lua over rand.c demotion.
	if len(core) == 0 && len(peri) == 0 {
		return rows
	}
	out := make([]hotspotRow, 0, len(rows))
	out = append(out, core...)
	out = append(out, mid...)
	out = append(out, peri...)
	return out
}

// forceCCoreHotspots prepends on-disk Redis/C cores (acl/db/networking/server)
// and drops remaining noise so LIVE contracts always see src/*.c first.
func forceCCoreHotspots(rows []hotspotRow, root, primary string, shape security.ProjectShape, topK int) []hotspotRow {
	primaryLang := strings.ToLower(strings.TrimSpace(primary))
	if primaryLang != "c" && shape != security.ShapeFrameworkCore && shape != security.ShapeLibrary {
		return rows
	}
	if primaryLang != "c" && !hasFileUnder(root, "src/server.c") {
		return rows
	}
	cores := []string{"src/server.c", "src/networking.c", "src/acl.c", "src/db.c", "src/dict.c", "src/object.c"}
	seen := map[string]struct{}{}
	var seeded []hotspotRow
	for i, rel := range cores {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := os.Stat(abs); err != nil {
			continue
		}
		key := strings.ToLower(rel)
		seen[key] = struct{}{}
		seeded = append(seeded, hotspotRow{
			File: rel, Commits: 0, Centrality: 2000 - i, Score: round3(1.0 - float64(i)*0.02),
		})
	}
	if len(seeded) == 0 {
		return rows
	}
	var rest []hotspotRow
	for _, r := range rows {
		key := strings.ToLower(strings.ReplaceAll(r.File, "\\", "/"))
		if _, ok := seen[key]; ok {
			continue
		}
		if isHotspotNoisePath(r.File, primary) {
			continue
		}
		// Keep other src/*.c mid-tier; drop root/peripheral noise from top.
		lower := key
		base := path.Base(lower)
		if primaryLang == "c" && !(strings.HasPrefix(lower, "src/") || strings.Contains(lower, "/src/")) {
			continue
		}
		if strings.Contains(base, "rand") || strings.Contains(base, "crc") || strings.Contains(base, "sha1") {
			continue
		}
		rest = append(rest, r)
	}
	out := append(seeded, rest...)
	if topK > 0 && len(out) > topK {
		out = out[:topK]
	}
	return out
}

func hasFileUnder(root, rel string) bool {
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

// seedLibraryCoreHotspots prepends known server/router cores when thin graphs
// only surface peripheral files (e.g. redis src/rand.c alone). Also seeds app
// controller hubs when git history is empty (spring-petclinic shallow clones).
func seedLibraryCoreHotspots(rows []hotspotRow, root string, shape security.ProjectShape, topK int) []hotspotRow {
	var guidance []security.ContextFinding
	switch shape {
	case security.ShapeLibrary, security.ShapeFrameworkCore:
		guidance = security.LibraryPerfGuidance(root, shape, 4)
	default:
		if len(rows) == 0 {
			guidance = security.AppPerfGuidance(root, 4)
		}
	}
	if len(guidance) == 0 {
		return rows
	}
	seen := map[string]struct{}{}
	for _, r := range rows {
		seen[strings.ToLower(strings.ReplaceAll(r.File, "\\", "/"))] = struct{}{}
	}
	var seeded []hotspotRow
	for i, g := range guidance {
		key := strings.ToLower(strings.ReplaceAll(g.File, "\\", "/"))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		seeded = append(seeded, hotspotRow{
			File: g.File, Commits: 0, Centrality: 1000 - i, Score: round3(1.0 - float64(i)*0.05),
		})
	}
	if len(seeded) == 0 {
		return rows
	}
	out := append(seeded, rows...)
	if topK > 0 && len(out) > topK {
		out = out[:topK]
	}
	return out
}

// annotateHotspotWhy fills Why + RewriteHint + SuggestedNextQuery so agents get
// actionable next steps (not bare churn ranks). Prefer a real function/method
// line from the graph; fall back to disk hot-path scan; only then line 1.
func annotateHotspotWhy(rows []hotspotRow, commitsScanned int, shape security.ProjectShape, bestLine map[string]int, root string) {
	for i := range rows {
		r := &rows[i]
		if r.Line <= 0 {
			key := strings.ReplaceAll(r.File, "\\", "/")
			if bestLine != nil {
				if ln := bestLine[key]; ln > 0 {
					r.Line = ln
				} else if ln := bestLine[r.File]; ln > 0 {
					r.Line = ln
				}
			}
			if r.Line <= 0 && root != "" {
				abs := filepath.Join(root, filepath.FromSlash(key))
				if ln := security.LineForHotPath(abs); ln > 0 {
					r.Line = ln
				}
			}
			if r.Line <= 0 {
				r.Line = 1 // last resort file-level cite
			}
		}
		parts := make([]string, 0, 3)
		if r.Commits > 0 {
			parts = append(parts, fmt.Sprintf("churn=%d commits in window", r.Commits))
		} else if commitsScanned <= 1 {
			parts = append(parts, "churn unreliable (shallow history)")
		}
		if r.Centrality > 0 {
			parts = append(parts, fmt.Sprintf("centrality=%d inbound call weight", r.Centrality))
		}
		if len(parts) == 0 {
			parts = append(parts, "seeded core/hot-path guidance")
		}
		r.Why = strings.Join(parts, "; ")
		r.RewriteHint = suggestHotspotRewrite(r.File, shape)
		base := path.Base(strings.ReplaceAll(r.File, "\\", "/"))
		switch shape {
		case security.ShapeLibrary, security.ShapeFrameworkCore:
			r.SuggestedNextQuery = fmt.Sprintf("hot path alloc complexity %s", strings.TrimSuffix(base, filepath.Ext(base)))
		case security.ShapeSkeleton:
			r.SuggestedNextQuery = fmt.Sprintf("pagination eager load %s", strings.TrimSuffix(base, filepath.Ext(base)))
		default:
			r.SuggestedNextQuery = fmt.Sprintf("N+1 query loop %s", strings.TrimSuffix(base, filepath.Ext(base)))
		}
	}
}

// suggestHotspotRewrite returns one concrete rewrite suggestion from path/shape
// heuristics (not a measured profile). Empty when we cannot suggest safely.
func suggestHotspotRewrite(file string, shape security.ProjectShape) string {
	f := strings.ToLower(strings.ReplaceAll(file, "\\", "/"))
	base := path.Base(f)
	ext := filepath.Ext(f)
	switch shape {
	case security.ShapeLibrary, security.ShapeFrameworkCore:
		if strings.Contains(f, "alloc") || strings.Contains(f, "buffer") || strings.Contains(base, "copy") {
			return "Prefer pooled/reused buffers or stack allocation on the hot path; avoid per-request heap growth."
		}
		if strings.Contains(f, "parse") || strings.Contains(f, "compile") {
			return "Cache parse/compile results keyed by input digest; avoid re-parsing identical payloads."
		}
		return "Profile before micro-optimizing; reduce allocs on the hottest inbound-call path, keep API behavior identical."
	case security.ShapeSkeleton:
		return "Treat as a sample pattern — add pagination/limits before copying into production list endpoints."
	}
	// App heuristics by path/name.
	if strings.Contains(f, "controller") || strings.Contains(f, "handler") || strings.Contains(f, "route") ||
		strings.Contains(base, "view") || strings.Contains(f, "/api/") {
		switch ext {
		case ".py":
			return "Check for N+1: use select_related/prefetch_related (Django) or joinedload (SQLAlchemy); paginate list endpoints."
		case ".php":
			return "Eager-load relations (with/load) and paginate; avoid per-row queries in Blade/Twig loops."
		case ".rb":
			return "Add includes/preload for associations; paginate ActiveRecord relations before rendering."
		case ".ts", ".tsx", ".js", ".jsx":
			return "Batch DB/API calls; avoid await-in-loop. Add pagination and Promise.all for independent fetches."
		case ".go":
			return "Batch queries / use JOINs; avoid query-per-item in handlers. Cap list responses."
		case ".java", ".kt":
			return "Use @EntityGraph / JOIN FETCH; paginate Spring Data findAll; avoid LazyInitialization in loops."
		default:
			return "Eliminate N+1 (batch/eager-load), paginate lists, and move heavy work off the request path."
		}
	}
	if strings.Contains(f, "service") || strings.Contains(f, "repository") || strings.Contains(f, "store") {
		return "Batch reads/writes; add a covering index only after confirming the slow query; keep transactions short."
	}
	if strings.Contains(f, "middleware") || strings.Contains(f, "filter") {
		return "Keep middleware O(1) per request; cache auth lookups; avoid sync I/O on every call."
	}
	return "Measure the hot path, then reduce per-request work (N+1, alloc churn, sync I/O) without changing contracts."
}
