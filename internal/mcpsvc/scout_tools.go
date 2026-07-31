package mcpsvc

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/detect"
	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/mcpimpact"
	"github.com/VeyrForge/codehelper/internal/profile"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/internal/review"
	"github.com/VeyrForge/codehelper/internal/security"
	"github.com/VeyrForge/codehelper/pkg/types"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ---- test_impact -----------------------------------------------------------

type testImpactResponse struct {
	Seeds     []string     `json:"seeds"`               // changed/target symbols analyzed
	TestFiles []string     `json:"test_files"`          // distinct test files to run
	Tests     []compactSym `json:"tests"`               // test symbols that transitively reach the change
	Truncated int          `json:"truncated,omitempty"` // tests dropped past the cap
	Safety    string       `json:"safety"`
	Freshness string       `json:"freshness,omitempty"`
	Note      string       `json:"note,omitempty"`
}

// maxTestImpact bounds the per-test list. You run test FILES (test_files), not
// individual functions, so a long per-test list is mostly context bloat — the
// file list and a count are the actionable parts. Kept small to limit how much
// stays in the agent's context window across a session.
const maxTestImpact = 25

// testImpactHandler answers "which tests should I run for this change?" by walking
// the reverse call-graph closure from each changed/target symbol and collecting
// the test functions that reach it. This is a SAFE (over-approximating) selection:
// it never silently drops a test that could be affected, but may include a few
// extra — the documented correctness contract for static test-impact selection.
func testImpactHandler(reg *registry.Registry) server.ToolHandlerFunc {
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

		depth := int(mcp.ParseInt64(req, "depth", 0))
		if depth <= 0 {
			depth = 6 // tests are often several hops above a leaf symbol
		}

		// Seeds: an explicit target symbol, else the symbols changed vs base_ref.
		var seeds []string
		if t := argString(args, "target"); t != "" {
			seeds = []string{t}
		} else {
			base := argString(args, "base_ref")
			if base == "" {
				base = "HEAD~1"
			}
			ids, derr := detect.ChangedSymbols(ctx, repo.RootPath, repo.Name, base, st)
			if derr != nil {
				return mcp.NewToolResultError(derr.Error()), nil
			}
			seeds = ids
		}

		tests := map[string]types.ImpactNode{} // symbolID -> node (dedup)
		for _, seed := range seeds {
			res, aerr := mcpimpact.Analyze(ctx, st, repo.Name, seed, depth, "upstream")
			if aerr != nil || res == nil {
				continue // a seed may not resolve (e.g. deleted symbol); skip it
			}
			for _, n := range res.Nodes {
				if n.Depth == 0 {
					continue // the seed itself
				}
				if review.IsTestPath(n.Path) && isTestSymbolKind(n.Kind) {
					tests[n.SymbolID] = n
				}
			}
		}

		out := testImpactResponse{
			Seeds:  seeds,
			Safety: "SAFE over-approximation: every test that transitively reaches a changed symbol via the call graph is included (may over-select; never silently misses). Run the full suite periodically and after dependency/build changes.",
		}
		fresh := freshness.Inspect(repo.RootPath)
		if fresh.Stale {
			out.Freshness = "index may be stale (" + fresh.StaleReason + ") — re-run analyze for accurate test selection"
		}
		if len(seeds) == 0 {
			out.Note = "no changed symbols detected for the diff; nothing to select"
			return mustToolResultFormatted(out, resolveFormat(args))
		}

		fileSet := map[string]struct{}{}
		nodes := make([]types.ImpactNode, 0, len(tests))
		for _, n := range tests {
			nodes = append(nodes, n)
		}
		// Stable, useful ordering: shallowest (closest to the change) first.
		sort.Slice(nodes, func(i, j int) bool {
			if nodes[i].Depth != nodes[j].Depth {
				return nodes[i].Depth < nodes[j].Depth
			}
			return nodes[i].Path < nodes[j].Path
		})
		for _, n := range nodes {
			fileSet[n.Path] = struct{}{}
			if len(out.Tests) >= maxTestImpact {
				out.Truncated++
				continue
			}
			out.Tests = append(out.Tests, compactSym{
				Name: n.Name, Kind: n.Kind, Loc: locOf(n.Path, n.SymbolID),
			})
		}
		for f := range fileSet {
			out.TestFiles = append(out.TestFiles, f)
		}
		sort.Strings(out.TestFiles)
		if len(out.Tests) == 0 {
			primaryLang := ""
			if pr, perr := profile.ReadOrGenerate(repo.RootPath); perr == nil && pr != nil {
				primaryLang = pr.PrimaryLanguage
			}
			graphConf := callGraphConfidenceLang(ctx, st, repo.Name, primaryLang)
			if graphConf != "" || isDynamicSparseLanguage(primaryLang) {
				out.Safety = "ABSTAIN / LOW confidence — sparse or dynamic call graph; empty tests is NOT proof of zero coverage"
				out.Note = "ABSTAIN: no tests reach the changed symbols via the indexed call graph. " +
					"On sparse/dynamic stacks this is expected under-count — do NOT treat as isolation proof. " +
					"Run the package/suite tests, or use textual discovery. " + graphConf
				if graphConf == "" && isDynamicSparseLanguage(primaryLang) {
					out.Note = "ABSTAIN: no tests via call graph on primary_language=" + primaryLang +
						" (dynamic dispatch often invisible). Empty test_impact ≠ zero coverage — run suite / textual search."
				}
			} else {
				out.Note = "no tests reach the changed symbols via the indexed call graph. Either coverage is missing or the change is in untested code — add tests, or the index may lack edges (analyze --force)."
			}
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

func isTestSymbolKind(kind string) bool {
	return kind == "function" || kind == "method"
}

// locOf builds a path:line reference from a symbol ID when possible.
func locOf(path, symID string) string {
	if l := symIDLoc(symID); l != "" {
		return l
	}
	return path
}

// ---- scout -----------------------------------------------------------------

type reuseCandidate struct {
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	Loc       string  `json:"loc"`
	Recv      string  `json:"recv,omitempty"`
	Signature string  `json:"signature,omitempty"`
	Callers   int     `json:"callers"` // how load-bearing / proven this symbol is
	Score     float64 `json:"score"`
}

type scoutImpact struct {
	Target     string `json:"target"`
	RiskTier   string `json:"risk_tier"`
	Dependents int    `json:"dependents"`
	Tests      int    `json:"tests_covering"`
	Confidence string `json:"confidence,omitempty"`
}

// sparseGraphDensityThreshold is the call-edges-per-symbol floor below which
// MCP tools emit LOW confidence. Doctor uses a stricter WARN floor (0.05) for
// CLI health; agents need an earlier signal so empty blast_radius is never
// read as isolation proof on dynamic stacks.
const sparseGraphDensityThreshold = 0.5

// mediumGraphDensityThreshold is the floor below which MCP tools emit MEDIUM
// confidence (thinner than dense Go/TS apps, but denser than LOW). Agents still
// must not treat empty fanout as isolation — especially on Java/Kotlin/Ruby/PHP.
const mediumGraphDensityThreshold = 1.5

// dynamicSparseLanguages always get a LOW label when density is under the
// higher dynamic threshold (or any empty inbound call set on that language).
var dynamicSparseLanguages = map[string]struct{}{
	"php": {}, "ruby": {}, "c": {}, "cpp": {}, "c++": {}, "objc": {},
	// Spring / JVM DI still hides many callers behind containers.
	"java": {}, "kotlin": {},
	// Lite / heuristic extractors — call fanout is under-resolved.
	"elixir": {}, "dart": {}, "lua": {}, "swift": {}, "scala": {},
	"gdscript": {}, "bash": {}, "shell": {}, "sql": {}, "mdx": {},
	"zig": {}, "solidity": {}, "clojure": {}, "erlang": {}, "fsharp": {},
	"r": {}, "perl": {}, "ocaml": {}, "haskell": {},
	// HCL/Terraform: reads-heavy; impact via calls alone under-counts.
	"hcl": {}, "terraform": {},
	// DevOps lite: stages/services/targets + sparse reads; no call graph.
	"dockerfile": {}, "compose": {}, "makefile": {},
}

// callGraphConfidence labels how much to trust call-graph-derived signals
// (risk_tier, dependents, tests_covering). Static, heuristic resolution captures
// most edges in statically-dispatched code (Go, Rust) but few in dynamic
// frameworks (Laravel facades/DI, Rails, C macros), where dependents/tests are
// UNDER-counted. We measure resolved call-edge density (call edges ÷ symbols)
// and warn when it's low so a "0 tests / low risk" isn't read as ground truth.
// primaryLang (optional) tightens the threshold for PHP/Ruby/C/C++.
func callGraphConfidence(ctx context.Context, st *graph.Store, repoID string) string {
	return callGraphConfidenceLang(ctx, st, repoID, "")
}

func callGraphConfidenceLang(ctx context.Context, st *graph.Store, repoID, primaryLang string) string {
	symbols, _, _, err := st.Counts(ctx, repoID)
	if err != nil || symbols == 0 {
		return ""
	}
	deg, derr := st.InDegrees(ctx, repoID, "calls")
	if derr != nil {
		return ""
	}
	callEdges := 0
	for _, d := range deg {
		callEdges += d
	}
	density := float64(callEdges) / float64(symbols)
	lang := strings.ToLower(strings.TrimSpace(primaryLang))
	_, dynamic := dynamicSparseLanguages[lang]
	threshold := sparseGraphDensityThreshold
	if dynamic {
		// Dynamic stacks often sit just above 0.5 while still missing facades /
		// macros / require edges — warn earlier so agents never trust empty BR.
		threshold = 1.0
	}
	if density < threshold {
		langNote := "likely a dynamic framework or a parser without call resolution (PHP/Ruby/C/C++/Swift/Lua/Godot/Java)"
		if dynamic {
			langNote = fmt.Sprintf("primary_language=%q typically under-resolves calls (facades/macros/require/dynamic dispatch/DI)", lang)
		}
		return fmt.Sprintf("LOW — sparse call graph (%d call edges / %d symbols = %.2f/sym); %s. risk_tier & tests_covering & blast_radius are UNDER-counted. AGENT DIRECTIVE: do NOT treat 0 callers / empty blast_radius / risk_tier=low as isolation proof — confirm with tests + a textual search before changing.", callEdges, symbols, density, langNote)
	}
	medFloor := mediumGraphDensityThreshold
	if dynamic {
		medFloor = 2.0
	}
	if density < medFloor {
		langNote := "call graph thinner than dense Go/TS apps"
		if dynamic {
			langNote = fmt.Sprintf("primary_language=%q still under-counts facades/DI/macros vs dense Go", lang)
		}
		return fmt.Sprintf("MEDIUM — moderate call density (%d call edges / %d symbols = %.2f/sym); %s. Prefer path= / Type.Method; empty fanout still ≠ isolation — confirm with tests + textual search.", callEdges, symbols, density, langNote)
	}
	return ""
}

// isDynamicSparseLanguage reports languages whose static call graphs routinely
// under-count callers (used when attaching self-only blast_radius warnings).
func isDynamicSparseLanguage(lang string) bool {
	_, ok := dynamicSparseLanguages[strings.ToLower(strings.TrimSpace(lang))]
	return ok
}

// isLowCallGraphConfidence reports agent-facing confidence strings that start
// with LOW (kickoff/scout/plan/impact honesty path).
func isLowCallGraphConfidence(conf string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(conf)), "LOW")
}

// isMediumCallGraphConfidence reports MEDIUM band labels (thinner-than-dense
// honesty without the full LOW fail-closed path).
func isMediumCallGraphConfidence(conf string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(conf)), "MEDIUM")
}

// sanitizeScoutImpactForSparse forces risk_tier=unknown when confidence is LOW
// so kickoff/scout/plan never present risk_tier=low / empty dependents as
// safe-to-edit isolation (Swift→Lua band, C++/Godot, PHP/Ruby, …).
func sanitizeScoutImpactForSparse(imp *scoutImpact) {
	if imp == nil || !isLowCallGraphConfidence(imp.Confidence) {
		return
	}
	imp.RiskTier = "unknown"
}

// sparseWhatNextPrefix is prepended to kickoff/scout/plan/impact what_next when
// call_graph_confidence=LOW — short junior/vibe honesty, not a wall of agent jargon.
const sparseWhatNextPrefix = "Sparse call graph — empty blast_radius ≠ safe to edit; confirm with tests + search. "

// mediumWhatNextPrefix is prepended when confidence is MEDIUM (thinner than dense).
const mediumWhatNextPrefix = "Thinner call graph — empty fanout ≠ isolation; prefer path= / Type.Method. "

// applySparseWhatNextCaution prefixes what_next when confidence is LOW.
func applySparseWhatNextCaution(whatNext, conf string) string {
	if !isLowCallGraphConfidence(conf) {
		return whatNext
	}
	wn := strings.TrimSpace(whatNext)
	if wn == "" {
		return strings.TrimSpace(sparseWhatNextPrefix)
	}
	lower := strings.ToLower(wn)
	if strings.Contains(lower, "call_graph_confidence=low") ||
		strings.Contains(lower, "safe-to-edit isolation") ||
		strings.Contains(lower, "sparse call graph") ||
		strings.Contains(lower, "empty blast_radius") {
		return wn
	}
	return sparseWhatNextPrefix + wn
}

// applyMediumWhatNextCaution prefixes what_next when confidence is MEDIUM.
func applyMediumWhatNextCaution(whatNext, conf string) string {
	if !isMediumCallGraphConfidence(conf) {
		return whatNext
	}
	wn := strings.TrimSpace(whatNext)
	if wn == "" {
		return strings.TrimSpace(mediumWhatNextPrefix)
	}
	lower := strings.ToLower(wn)
	if strings.Contains(lower, "thinner call graph") ||
		strings.Contains(lower, "medium call graph") ||
		strings.Contains(lower, "empty fanout") {
		return wn
	}
	return mediumWhatNextPrefix + wn
}

// primaryLanguageOf returns the best primary language hint for sparse-threshold
// tightening (profile primary, else first listed language).
func primaryLanguageOf(primary string, languages []string) string {
	if p := strings.TrimSpace(primary); p != "" {
		return p
	}
	for _, lang := range languages {
		if l := strings.TrimSpace(lang); l != "" {
			return l
		}
	}
	return ""
}

// usageExample is a real call site of the top reuse candidate so the agent sees
// HOW to use it, not just that it exists — turning "reuse this" into copyable code.
type usageExample struct {
	Caller string `json:"caller"` // the symbol that calls the candidate
	Loc    string `json:"loc"`    // path:line of the actual call
	Code   string `json:"code"`   // the source line of the call, trimmed
}

type scoutResponse struct {
	Task                 string           `json:"task"`
	ReuseCandidates      []reuseCandidate `json:"reuse_candidates"`
	ImpactOfTop          *scoutImpact     `json:"impact_of_top,omitempty"`
	UsageOfTop           *usageExample    `json:"usage_of_top,omitempty"`
	Freshness            string           `json:"freshness,omitempty"`
	CollisionNote        string           `json:"collision_note,omitempty"`
	FindingsMode         string           `json:"findings_mode,omitempty"`
	Abstain              string           `json:"abstain,omitempty"`
	WhatNext             string           `json:"what_next,omitempty"`
	NextQueries          []string         `json:"next_queries,omitempty"`
	RecommendedNextTools []string         `json:"recommended_next_tools,omitempty"`
	ParamCorrection      string           `json:"param_correction,omitempty"`
	Note                 string           `json:"note"`
}

// scoutHandler pre-assembles the context needed to implement a change: existing
// symbols that already do something similar (reuse candidates, ranked, with caller
// counts so the agent sees what is load-bearing) plus the blast radius and test
// coverage of the single best match. The goal is for the LLM to spend its thinking
// on the change, not on rediscovering what already exists.
func scoutHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		task, taskNote := resolveKickoffTask(args)
		if task == "" {
			return emptyTaskRecovery("scout"), nil
		}
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		st, err := openGraph(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer st.Close()

		topK := int(mcp.ParseInt64(req, "top_k", 0))
		if topK <= 0 {
			topK = 8
		}
		// Centrality ranking matters most here: scout's whole premise is "prefer
		// the load-bearing reuse candidate", so the most-called existing symbol
		// that matches the task should surface first, not just the closest lexical
		// match. Mirrors the boost applied in the query tool.
		// Rank by the subject, not the imperative verb / casual filler ("i wanna show"),
		// so the reuse candidate matches the noun the user means. Same as plan.
		scoutQuery, scoutTokens := task, strings.Fields(strings.ToLower(task))
		if subj := taskSubjectTokens(task); len(subj) > 0 {
			scoutQuery, scoutTokens = strings.Join(subj, " "), subj
		}
		hits, err := retrieval.QueryHybridWithOptions(ctx, st, repo.Name, scoutQuery, reuseHitPool(topK), retrieval.MCPQueryOptions(
			repo.RootPath, "", scoutTokens, nil,
		))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		hits, demoted := demoteNoiseForQuery(task, hits)
		hits, _ = dropHostBleedHits(hits, repo.Name)
		hits = demoteIntentMismatchedHits(task, hits)
		if hasAuthLocateIntent(task) {
			hits = preferSecurityHits(hits, task)
		}
		hits = capRankedHits(hits, topK)
		upgradeHitDefLines(repo.RootPath, hits)

		out := scoutResponse{Task: task, CollisionNote: collisionHonestyNote(hits, demoted)}
		for _, h := range hits {
			out.ReuseCandidates = append(out.ReuseCandidates, reuseCandidate{
				Name: h.Symbol.Name, Kind: string(h.Symbol.Kind),
				Loc:       fmt.Sprintf("%s:%d", h.Symbol.Path, h.Symbol.LineStart),
				Recv:      h.Symbol.ParentID,
				Signature: h.Symbol.Signature,
				Callers:   callerCountOf(ctx, st, repo.Name, h.Symbol.ID),
				Score:     round3(h.Score),
			})
		}
		out.ReuseCandidates = dropStyleReuseCandidates(out.ReuseCandidates)
		out.ReuseCandidates = preferFeatureReuse(out.ReuseCandidates, task)
		out.ReuseCandidates = seedHealthRouteCandidates(repo.RootPath, out.ReuseCandidates, task)
		if note := featureEndpointAbstainNote(task, security.DetectProjectShape(repo.RootPath), repo.RootPath, out.ReuseCandidates); note != "" {
			out.Note = note
			out.Abstain = note
			out.FindingsMode = "abstain"
			out.ReuseCandidates = clearNonHealthReuseForAbstain(out.ReuseCandidates, note)
			out.ReuseCandidates = seedNonHTTPHealthEquivalent(repo.RootPath, out.ReuseCandidates)
		}

		// Blast radius + test coverage of the single best reuse candidate, so the
		// agent immediately knows what changing it would touch.
		if len(out.ReuseCandidates) > 0 {
			// Prefer graph hit for impact when still available; else skip.
			if len(hits) > 0 && out.ReuseCandidates[0].Name == hits[0].Symbol.Name {
				top := hits[0].Symbol
				if res, aerr := mcpimpact.Analyze(ctx, st, repo.Name, top.ID, 4, "upstream"); aerr == nil && res != nil {
					tests := 0
					for _, n := range res.Nodes {
						if n.Depth > 0 && review.IsTestPath(n.Path) && isTestSymbolKind(n.Kind) {
							tests++
						}
					}
					out.ImpactOfTop = &scoutImpact{
						Target: top.Name, RiskTier: res.RiskTier,
						Dependents: len(res.Nodes) - 1, Tests: tests,
						Confidence: callGraphConfidence(ctx, st, repo.Name),
					}
				}
				out.UsageOfTop = usageExampleFor(ctx, st, repo, top)
			}
		}

		fresh := freshness.Inspect(repo.RootPath)
		if fresh.Stale {
			out.Freshness = "index may be stale (" + fresh.StaleReason + ")"
		}
		if out.Note == "" {
			if len(out.ReuseCandidates) == 0 {
				out.Note = "no existing symbols match this task — likely new functionality; check imports/conventions before writing."
			} else {
				out.Note = "Prefer extending the high-caller reuse_candidates over writing new code. usage_of_top shows a real call site (copy its calling convention); impact_of_top shows what depends on the closest match and how many tests cover it — run test_impact before changing it."
			}
		}
		var topCand *reuseCandidate
		if len(out.ReuseCandidates) > 0 {
			topCand = &out.ReuseCandidates[0]
		}
		out.NextQueries = featureEndpointNextQueries(task, out.Abstain != "")
		out.WhatNext = buildWhatNext("feature", topCand, nil, out.Abstain, out.NextQueries)
		graphConf := ""
		if out.ImpactOfTop != nil {
			graphConf = out.ImpactOfTop.Confidence
		}
		if graphConf == "" {
			graphConf = callGraphConfidence(ctx, st, repo.Name)
		}
		if out.Abstain == "" {
			out.WhatNext = applySparseWhatNextCaution(out.WhatNext, graphConf)
			out.WhatNext = applyMediumWhatNextCaution(out.WhatNext, graphConf)
		}
		if isMediumCallGraphConfidence(graphConf) {
			medNote := "Thinner call graph (MEDIUM) — callers may be incomplete; prefer path= / Type.Method; empty fanout ≠ isolation."
			if out.Note == "" {
				out.Note = medNote
			} else if !strings.Contains(strings.ToLower(out.Note), "thinner call graph") &&
				!strings.Contains(strings.ToLower(out.Note), "medium call graph") {
				out.Note = medNote + " " + out.Note
			}
		} else if isLowCallGraphConfidence(graphConf) {
			sparseNote := "SPARSE CALL GRAPH — call_graph_confidence=LOW; do NOT treat impact_of_top risk_tier/dependents as safe-to-edit isolation."
			if out.Note == "" {
				out.Note = sparseNote
			} else if !strings.Contains(strings.ToLower(out.Note), "safe-to-edit isolation") &&
				!strings.Contains(strings.ToLower(out.Note), "call_graph_confidence=low") {
				out.Note = sparseNote + " " + out.Note
			}
		}
		if taskNote != "" {
			out.ParamCorrection = taskNote
			out.Note = taskNote + " " + out.Note
		}
		out.RecommendedNextTools = vibeRecommendedTools("feature", topCand, out.Abstain != "")
		return mustToolResultFormatted(scrubHostBleedPayload(out, repo.Name), resolveFormat(args))
	}
}

// callerCountOf returns how many resolved calls target this symbol — a proxy for
// how load-bearing (and therefore reuse-worthy / risky-to-change) it is.
func callerCountOf(ctx context.Context, st *graph.Store, repoID, symID string) int {
	in, err := st.EdgesTo(ctx, repoID, symID, "calls")
	if err != nil {
		return 0
	}
	return len(in)
}

// usageExampleFor finds a real call site of sym and returns the exact source line
// that invokes it, so the agent sees the calling convention. It prefers a
// non-test caller (more representative), reads the caller's body, and locates the
// first line that names the symbol. Best-effort: returns nil if no resolved
// caller exists or the source can't be read.
func usageExampleFor(ctx context.Context, st *graph.Store, repo registry.Entry, sym types.Symbol) *usageExample {
	callers, err := st.CallersOf(ctx, repo.Name, sym.ID)
	if err != nil || len(callers) == 0 {
		return nil
	}
	// Prefer a non-test caller; fall back to the first if all are tests.
	chosen := callers[0]
	for _, c := range callers {
		if !review.IsTestPath(c.Path) {
			chosen = c
			break
		}
	}
	line, lineNo := callSiteLine(repo.RootPath, chosen, sym.Name)
	if line == "" {
		return nil
	}
	return &usageExample{
		Caller: chosen.Name,
		Loc:    fmt.Sprintf("%s:%d", chosen.Path, lineNo),
		Code:   line,
	}
}

// callSiteLine reads caller's source between its start/end lines and returns the
// first line (and its 1-based number) that mentions name — the actual call site.
func callSiteLine(root string, caller types.Symbol, name string) (string, int) {
	abs, err := absPathUnderRepo(root, caller.Path)
	if err != nil {
		return "", 0
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", 0
	}
	lines := strings.Split(string(b), "\n")
	start := caller.LineStart
	if start < 1 {
		start = 1
	}
	end := caller.LineEnd
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	for i := start - 1; i < end && i < len(lines); i++ {
		if strings.Contains(lines[i], name) {
			trimmed := strings.TrimSpace(lines[i])
			if len(trimmed) > 200 {
				trimmed = trimmed[:200] + "…"
			}
			return trimmed, i + 1
		}
	}
	return "", 0
}
