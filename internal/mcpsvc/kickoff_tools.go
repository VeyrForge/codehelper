package mcpsvc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/connections"
	"github.com/VeyrForge/codehelper/internal/docs"
	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/hints"
	"github.com/VeyrForge/codehelper/internal/mcpimpact"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/profile"
	"github.com/VeyrForge/codehelper/internal/projcfg"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/research"
	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/internal/review"
	"github.com/VeyrForge/codehelper/internal/security"
	"github.com/VeyrForge/codehelper/internal/setupsuggest"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// kickoff is the ONE-CALL task starter. Beginning any feature/fix used to mean
// chaining project_context (orient) -> scout (reuse) -> docs (library API) ->
// plan (decisions/steps) -> profile (verify commands): four-plus round trips
// before writing a line. kickoff returns all of it in a single call, grounded in
// the real index, so the LLM spends its budget on the change, not on rediscovery.
// It is a SUPERSET of plan + scout + the orient half of project_context.
type kickoffResponse struct {
	Task                 string              `json:"task"`
	Role                 string              `json:"role"`
	Orient               kickoffOrient       `json:"orient"`
	ReuseCandidates      []reuseCandidate    `json:"reuse_candidates,omitempty"`
	UsageOfTop           *usageExample       `json:"usage_of_top,omitempty"`
	ImpactOfTop          *scoutImpact        `json:"impact_of_top,omitempty"`
	DuplicationRisk      []string            `json:"duplication_risk,omitempty"`
	Placement            []string            `json:"placement,omitempty"`
	RelevantDocs         []string            `json:"relevant_docs,omitempty"`
	DocPreviews          []kickoffDocPreview `json:"doc_previews,omitempty"` // inlined top doc chunk when network allowed
	DecisionPoints       []string            `json:"decision_points"`
	Considerations       []string            `json:"considerations"`
	Gotchas              []string            `json:"gotchas,omitempty"` // stack pitfalls (curated) + learned hints, so the agent avoids known mistakes before writing code
	Steps                []string            `json:"steps"`
	Verification         []string            `json:"verification,omitempty"`
	Findings             []auditFinding      `json:"findings,omitempty"`
	FindingsMode         string              `json:"findings_mode,omitempty"`
	Abstain              string              `json:"abstain,omitempty"`
	WhatNext             string              `json:"what_next,omitempty"`
	NextQueries          []string            `json:"next_queries,omitempty"`
	RecommendedNextTools []string            `json:"recommended_next_tools,omitempty"`
	ProjectShape         string              `json:"project_shape,omitempty"`
	Freshness            string              `json:"freshness,omitempty"`
	CollisionNote        string              `json:"collision_note,omitempty"` // sample/test/fixture demotion for Locate/Vibe
	ParamCorrection      string              `json:"param_correction,omitempty"`
	Note                 string              `json:"note"`
}

// kickoffDocPreview is a compact library-docs snippet inlined into kickoff when
// network fetch is permitted — so the agent gets graph orient/reuse AND a top
// doc chunk without a separate docs round trip (Context7 hybrid UX).
type kickoffDocPreview struct {
	Library string `json:"library"`
	Version string `json:"version,omitempty"`
	Heading string `json:"heading,omitempty"`
	Text    string `json:"text,omitempty"`
	Source  string `json:"source,omitempty"`
	Note    string `json:"note,omitempty"`
}

type kickoffOrient struct {
	ProjectType      string               `json:"project_type"`
	Languages        []string             `json:"languages,omitempty"`
	Frameworks       []string             `json:"frameworks,omitempty"`
	KeyDeps          []string             `json:"key_dependencies,omitempty"`
	Summary          string               `json:"summary,omitempty"`
	SetupSuggestions *setupsuggest.Report `json:"setup_suggestions,omitempty"`
}

func kickoffHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		task, taskNote := resolveKickoffTask(args)
		if task == "" {
			return emptyTaskRecovery("kickoff"), nil
		}
		role := strings.ToLower(strings.TrimSpace(argString(args, "role")))
		role = inferRoleFromTask(role, task)
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		st, err := openGraph(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer st.Close()

		out := kickoffResponse{Task: task, Role: role}
		primaryLang := ""

		// --- ORIENT: stack/frameworks/deps/summary (the project_context half) ---
		frameworks, deps, summary := projectBrief(repo.RootPath)
		out.Orient = kickoffOrient{Frameworks: frameworks, KeyDeps: deps, Summary: summary}
		if pp, perr := profile.Generate(repo.RootPath); perr == nil {
			out.Orient.ProjectType = pp.ProjectType
			out.Orient.Languages = pp.Languages
			primaryLang = primaryLanguageOf(pp.PrimaryLanguage, pp.Languages)
			out.Verification = uniqueTrimmedStrings(pp.LintCommands, pp.TestCommands)
			// Surface stack pitfalls + learned hints right where the agent is about to
			// implement, so known mistakes are avoided before the first edit.
			out.Gotchas = pp.Gotchas
			depNames := make([]string, 0, len(pp.Dependencies))
			for _, d := range pp.Dependencies {
				depNames = append(depNames, d.Name)
			}
			if lh, herr := hints.MatchingFor(pp.Framework, pp.ProjectType, pp.Languages, depNames); herr == nil {
				out.Gotchas = uniqueTrimmedStrings(out.Gotchas, lh)
			}
			conn, _ := connections.Load(repo.RootPath)
			pcfg, _ := projcfg.Load(repo.RootPath)
			sug := setupsuggest.Build(setupsuggest.Input{
				RepoRoot:    repo.RootPath,
				ProjectType: pp.ProjectType,
				Framework:   pp.Framework,
				Frameworks:  frameworks,
				Connections: conn,
				Projcfg:     pcfg,
			})
			out.Orient.SetupSuggestions = &sug
		}

		// Simple vibe asks (health/security/perf/tiny feature): suppress browser/CMS
		// setup tax that derails agents away from grounded code work (vibe P1).
		if shouldSuppressSetupTax(task, role) && out.Orient.SetupSuggestions != nil {
			out.Orient.SetupSuggestions = nil
		}

		// --- FIND: reuse candidates ranked by subject (same path as scout/plan) ---
		query, tokens := task, strings.Fields(strings.ToLower(task))
		if subj := taskSubjectTokens(task); len(subj) > 0 {
			query, tokens = strings.Join(subj, " "), subj
		}
		hits, err := retrieval.QueryHybridWithOptions(ctx, st, repo.Name, query, reuseHitPool(5), retrieval.MCPQueryOptions(
			repo.RootPath, task, tokens, nil,
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
		hits = filterAuditHits(hits, role)
		hits = capRankedHits(hits, 5)
		upgradeHitDefLines(repo.RootPath, hits)
		if note := collisionHonestyNote(hits, demoted); note != "" {
			out.CollisionNote = note
		}
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
		// Always drop CSS/#selectors — vibe kickoff without role= must not rank stylesheets.
		out.ReuseCandidates = dropStyleReuseCandidates(out.ReuseCandidates)
		if role == "" || role == "feature" || role == "refactor" {
			out.ReuseCandidates = preferFeatureReuse(out.ReuseCandidates, task)
			out.ReuseCandidates = seedHealthRouteCandidates(repo.RootPath, out.ReuseCandidates, task)
		}
		var top *reuseCandidate
		if len(out.ReuseCandidates) > 0 {
			top = &out.ReuseCandidates[0]
		}

		// --- UNDERSTAND: usage example + blast radius/tests of the closest match ---
		sig := taskSignals{Top: top}
		graphConf := ""
		if len(hits) > 0 {
			t := hits[0].Symbol
			out.UsageOfTop = usageExampleFor(ctx, st, repo, t)
			if res, aerr := mcpimpact.Analyze(ctx, st, repo.Name, t.ID, 4, "upstream"); aerr == nil && res != nil {
				tests := 0
				for _, n := range res.Nodes {
					if n.Depth > 0 && review.IsTestPath(n.Path) && isTestSymbolKind(n.Kind) {
						tests++
					}
				}
				graphConf = callGraphConfidenceLang(ctx, st, repo.Name, primaryLang)
				imp := &scoutImpact{
					Target: t.Name, RiskTier: res.RiskTier,
					Dependents: len(res.Nodes) - 1, Tests: tests, Confidence: graphConf,
				}
				sanitizeScoutImpactForSparse(imp)
				sig.RiskTier = imp.RiskTier
				sig.Dependents = imp.Dependents
				sig.TestsOnTop = tests
				sig.PkgsSpanned = distinctPkgs(res.Nodes)
				sig.SparseGraph = isLowCallGraphConfidence(graphConf) || isMediumCallGraphConfidence(graphConf)
				out.ImpactOfTop = imp
			}
		}

		// --- DOCS: which indexed dependencies the task likely needs API docs for ---
		out.RelevantDocs = relevantDocs(task, deps)
		// When network is allowed, optionally inline a top doc chunk for the most
		// relevant libs so kickoff covers graph + library API in one call.
		approveNet := argBool(args, "approve_network", false)
		inlineDocs := argBool(args, "inline_docs", true)
		network := approveNet ||
			research.NetworkEnabled(repo.RootPath) ||
			os.Getenv("CODEHELPER_DOCS_NETWORK") == "1"
		if inlineDocs && network && len(out.RelevantDocs) > 0 {
			out.DocPreviews = fetchKickoffDocPreviews(ctx, repo.RootPath, task, deps, out.RelevantDocs)
		}

		// --- DESIGN: task-specific decisions/placement/duplication/considerations ---
		candPaths := make([]string, 0, len(out.ReuseCandidates))
		for _, c := range out.ReuseCandidates {
			candPaths = append(candPaths, c.Loc)
		}
		sig.Domains = detectDomains(task, candPaths)
		out.DuplicationRisk = deriveDuplication(task, out.ReuseCandidates)
		out.Placement = derivePlacement(sig)
		out.DecisionPoints = deriveDecisionPoints(role, sig)
		out.Considerations = deriveConsiderations(role, sig)
		out.Steps = planSteps(role, top)

		if fresh := freshness.Inspect(repo.RootPath); fresh.Stale {
			fix := strings.TrimSpace(fresh.SuggestedFix)
			if fix == "" {
				fix = "codehelper analyze"
				reason := strings.ToLower(fresh.StaleReason)
				if strings.Contains(reason, "working tree") || strings.Contains(reason, "uncommitted") {
					fix = "codehelper analyze --force"
				}
			}
			out.Freshness = "index may be stale (" + fresh.StaleReason + ") — run `" + fix + "` for accurate reuse/impact"
		} else if fresh.IndexLag == "possible" {
			out.Freshness = "index_lag=possible (working tree changed; watch may still be catching up) — if a symbol misses, use disk_matches / read_workspace_file or `codehelper analyze --force`"
		}
		out.Note = "One-shot task starter (orient + reuse + docs + decisions + verify). Resolve the decision_points (ask the user when they're genuine choices), confirm the top reuse_candidate with `context` before extending, use doc_previews (or call `docs`) for library APIs, then implement and run the verification commands. After edits: diagnostics → review_diff → verify → finish_check (can_claim_done)."
		if role == "architect" {
			out.Note = "Architect mode: answer with cited symbols/paths from reuse_candidates + impact_of_top; resolve decision_points/placement with the user. Do NOT apply patches until the design is accepted — then switch to role=feature (or investigate recipe=architecture → change_kit). Prefer method/fn targets if type hubs look leaf-only."
		}
		if role == "feature" || role == "" {
			topHint := "top reuse_candidate"
			if top != nil {
				topHint = "`" + top.Name + "`"
			}
			out.Note = "Feature mode: scout/reuse → `change_kit` on " + topHint +
				" (source+callers+tests) → smallest patch → diagnostics → review_diff → verify → finish_check. " +
				"Prefer extending an existing handler/middleware over new files. " + out.Note
		}
		if taskNote != "" {
			out.ParamCorrection = taskNote
			out.Note = taskNote + " " + out.Note
		}
		if network && len(out.DocPreviews) > 0 {
			out.Note += " doc_previews inlined (network on); call `docs` for deeper chunks."
		} else if len(out.RelevantDocs) > 0 && !network {
			out.Note += " Set research.enabled or approve_network=true to inline top doc chunks into kickoff."
		}

		// Health abstain AFTER note is finalized so it is not wiped by the starter blurb.
		if role == "" || role == "feature" || role == "refactor" {
			if note := featureEndpointAbstainNote(task, security.DetectProjectShape(repo.RootPath), repo.RootPath, out.ReuseCandidates); note != "" {
				out.Abstain = note
				out.Note = note + " " + out.Note
				out.ReuseCandidates = clearNonHealthReuseForAbstain(out.ReuseCandidates, note)
				out.ReuseCandidates = seedNonHTTPHealthEquivalent(repo.RootPath, out.ReuseCandidates)
				if len(out.ReuseCandidates) == 0 {
					top = nil
					out.UsageOfTop, out.ImpactOfTop = nil, nil
				} else {
					top = &out.ReuseCandidates[0]
				}
			}
		}

		// role=security|performance: findings mode (grounded sinks) or clear abstain.
		findings, mode, abstain := applyFindingsMode(role, repo.RootPath, task, &out.ReuseCandidates, nil, &out.Note, &out.Steps)
		out.Findings, out.FindingsMode = findings, mode
		// Do not wipe a prior feature health abstain with an empty findings-mode abstain.
		if abstain != "" {
			out.Abstain = abstain
		}
		shape := security.DetectProjectShape(repo.RootPath)
		out.ProjectShape = string(shape)
		if role == "security" && len(findings) > 0 {
			locs := make([]string, 0, 3)
			for i, f := range findings {
				if i >= 3 {
					break
				}
				locs = append(locs, fmt.Sprintf("%s:%d (%s)", f.File, f.Line, f.Rule))
			}
			out.CollisionNote = strings.TrimSpace(out.CollisionNote + " " +
				"Security findings mode: prefer sink candidates " + strings.Join(locs, "; ") +
				" over CSS/DI/Schema reuse hits.")
		}
		if len(out.ReuseCandidates) == 0 {
			out.UsageOfTop, out.ImpactOfTop = nil, nil
			top = nil
		}

		// Vibe UX: always emit what_next + 3 copy-pasteable next_queries for LLM consumers.
		out.NextQueries = vibeNextQueries(role, shape, out.Abstain != "", task, repo.RootPath)
		if role == "performance" && len(out.Findings) > 0 {
			out.NextQueries = preferPerfFindingNextQueries(out.NextQueries, out.Findings)
		}
		out.WhatNext = buildWhatNext(role, top, out.Findings, out.Abstain, out.NextQueries)
		if graphConf == "" && out.ImpactOfTop != nil {
			graphConf = out.ImpactOfTop.Confidence
		}
		if graphConf == "" {
			graphConf = callGraphConfidenceLang(ctx, st, repo.Name, primaryLang)
		}
		// Abstain what_next is already the junior-facing answer — do not bury it
		// under sparse/MEDIUM call-graph prefixes (honesty stays on Note).
		if out.Abstain == "" {
			out.WhatNext = applySparseWhatNextCaution(out.WhatNext, graphConf)
			out.WhatNext = applyMediumWhatNextCaution(out.WhatNext, graphConf)
		}
		if isLowCallGraphConfidence(graphConf) {
			sparseNote := "SPARSE CALL GRAPH — call_graph_confidence=LOW; do NOT treat impact_of_top risk_tier/dependents as safe-to-edit isolation."
			if out.Note == "" {
				out.Note = sparseNote
			} else if !strings.Contains(strings.ToLower(out.Note), "safe-to-edit isolation") &&
				!strings.Contains(strings.ToLower(out.Note), "call_graph_confidence=low") {
				out.Note = sparseNote + " " + out.Note
			}
		} else if isMediumCallGraphConfidence(graphConf) {
			medNote := "Thinner call graph (MEDIUM) — callers may be incomplete; prefer path= / Type.Method; empty fanout ≠ isolation."
			if out.Note == "" {
				out.Note = medNote
			} else if !strings.Contains(strings.ToLower(out.Note), "thinner call graph") &&
				!strings.Contains(strings.ToLower(out.Note), "medium call graph") {
				out.Note = medNote + " " + out.Note
			}
		}
		out.RecommendedNextTools = vibeRecommendedTools(role, top, out.Abstain != "")

		// Section opt-in: `sections=reuse,decisions` returns only those.
		// Empty sections + simple vibe feature ask → default orient+reuse+docs+steps (light).
		sel := parseSections(argString(args, "sections"))
		if sel == nil {
			sel = defaultVibeSections(task, role)
		}
		if sel != nil {
			if !sel["orient"] {
				out.Orient = kickoffOrient{}
			}
			if !sel["reuse"] {
				out.ReuseCandidates, out.UsageOfTop, out.ImpactOfTop, out.DuplicationRisk = nil, nil, nil, nil
			}
			if !sel["docs"] {
				out.RelevantDocs = nil
				out.DocPreviews = nil
			}
			if !sel["decisions"] {
				out.DecisionPoints, out.Considerations, out.Placement = nil, nil, nil
			}
			if !sel["steps"] {
				out.Steps = nil
			}
			if !sel["verify"] {
				out.Verification = nil
			}
			if !sel["findings"] {
				out.Findings, out.FindingsMode = nil, ""
				// Keep Abstain / WhatNext / NextQueries — vibe safety net.
			}
		}
		return mustToolResultFormatted(scrubHostBleedPayload(out, repo.Name), resolveFormat(args))
	}
}

// parseSections parses a comma-separated section allowlist (orient,reuse,docs,
// decisions,steps,verify). Returns nil when empty so the caller returns all.
func parseSections(s string) map[string]bool {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return nil
	}
	out := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out[p] = true
		}
	}
	return out
}

// relevantDocs returns "lib@ver — call docs" pointers for indexed dependencies
// whose name appears in the task, so the LLM knows WHICH library docs to pull
// (version-correct) without a separate discovery round trip. Deterministic;
// bounded to the top few matches.
func relevantDocs(task string, deps []string) []string {
	subj := taskSubjectTokens(task)
	if len(subj) == 0 {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, dep := range deps {
		name := dep
		if i := strings.Index(dep, "@"); i > 0 {
			name = dep[:i]
		}
		lower := strings.ToLower(name)
		// match on any path segment of the dependency name (golang.org/x/time -> "time").
		segs := strings.FieldsFunc(lower, func(r rune) bool { return r == '/' || r == '.' || r == '-' })
		for _, s := range subj {
			if len(s) < 4 {
				continue
			}
			for _, seg := range segs {
				if seg == s && !seen[dep] {
					seen[dep] = true
					out = append(out, fmt.Sprintf("%s — call `docs` for version-correct API before coding against it", dep))
				}
			}
		}
		if len(out) >= 4 {
			break
		}
	}
	return out
}

// fetchKickoffDocPreviews inlines one compact chunk per relevant library (max 2)
// using the same docs.Engine path as the docs tool. Failures are soft — kickoff
// still returns relevant_docs pointers.
func fetchKickoffDocPreviews(ctx context.Context, repoRoot, task string, deps, relevant []string) []kickoffDocPreview {
	libs := librariesFromRelevantDocs(relevant, deps)
	if len(libs) == 0 {
		return nil
	}
	if len(libs) > 2 {
		libs = libs[:2]
	}
	topic := strings.Join(taskSubjectTokens(task), " ")
	eng := &docs.Engine{
		Fetcher: docs.NewHTTPFetcher(8*time.Second, nil),
	}
	if repoRoot != "" {
		cacheDir := filepath.Join(paths.RepoIndexDir(repoRoot), "docs-cache")
		eng.Cache = docs.NewCache(cacheDir, docsCacheTTL)
	}
	var out []kickoffDocPreview
	for _, lib := range libs {
		ver := ""
		if repoRoot != "" {
			ver, _ = docs.ResolveVersion(repoRoot, lib)
		}
		res, err := eng.Lookup(ctx, docs.LookupOptions{
			RepoRoot:  repoRoot,
			Library:   lib,
			Version:   ver,
			Topic:     topic,
			MaxTokens: 600,
			Network:   true,
			// Don't follow every llms.txt link in kickoff — keep the payload small.
			FollowLinks: false,
		})
		if err != nil || res == nil {
			continue
		}
		prev := kickoffDocPreview{Library: lib, Version: res.Version, Source: res.SourceUsed}
		if len(res.Chunks) > 0 {
			c := res.Chunks[0]
			prev.Heading = c.Heading
			prev.Text = truncateRunes(c.Text, 900)
		} else if res.Note != "" {
			prev.Note = res.Note
		} else if res.Resolved.DocBase != "" {
			prev.Note = "resolved " + res.Resolved.DocBase + " (no chunk body yet)"
			prev.Source = res.Resolved.DocBase
		}
		if prev.Text == "" && prev.Note == "" && prev.Source == "" {
			continue
		}
		out = append(out, prev)
	}
	return out
}

func librariesFromRelevantDocs(relevant, deps []string) []string {
	var libs []string
	seen := map[string]bool{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[strings.ToLower(name)] {
			return
		}
		seen[strings.ToLower(name)] = true
		libs = append(libs, name)
	}
	for _, line := range relevant {
		// "name@version — call docs…" or bare name (scoped npm: @scope/pkg@1.0.0)
		head := line
		if i := strings.Index(line, " — "); i > 0 {
			head = line[:i]
		}
		add(depNameOnly(head))
	}
	if len(libs) == 0 {
		for _, dep := range deps {
			add(depNameOnly(dep))
			if len(libs) >= 2 {
				break
			}
		}
	}
	return libs
}

// depNameOnly strips a trailing @version, preserving scoped npm names (@scope/pkg).
func depNameOnly(dep string) string {
	dep = strings.TrimSpace(dep)
	if dep == "" {
		return ""
	}
	if strings.HasPrefix(dep, "@") {
		if i := strings.LastIndex(dep, "@"); i > 0 {
			return dep[:i]
		}
		return dep
	}
	if i := strings.Index(dep, "@"); i > 0 {
		return dep[:i]
	}
	return dep
}

func truncateRunes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	// Byte-safe-ish truncate for ASCII-heavy docs; avoid mid-codepoint panic
	// by walking runes when needed.
	if max > len(s) {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// resolveKickoffTask accepts the canonical `task` param and common LLM aliases
// (agents often confuse kickoff with the query tool). When only an alias is set,
// it is used and a param_correction note is returned (same wording as context/impact).
func resolveKickoffTask(args map[string]any) (task, note string) {
	task = strings.TrimSpace(argString(args, "task"))
	if task != "" {
		return task, ""
	}
	if q := strings.TrimSpace(argFirst(args, "query", "q", "prompt", "request")); q != "" {
		return q, argAliasCorrection(args, "task", "query", "q", "prompt", "request")
	}
	return "", ""
}

// RegisterKickoffTools registers the one-shot task starter.
func RegisterKickoffTools(s *server.MCPServer, reg *registry.Registry) {
	s.AddTool(mcp.NewTool("kickoff",
		mcp.WithDescription("WORKFLOW slot (PREFERRED starter) — ONE-CALL task kickoff for Cursor/Claude/Codex: orient + reuse + docs + decision_points + steps + verify + what_next + next_queries. Prefer this over chaining project_context→query→scout→plan. When local orchestration is enabled and you want a multi-tool chain + memory, prefer orchestrate instead. PARAM: task= (alias query= accepted with a correction note). role=architect|security|performance|refactor|feature. After edits: change_kit→apply_patch→verify→finish_check."),
		mcp.WithString("task", mcp.Description("What you want to build/change/investigate, in natural language (preferred)")),
		mcp.WithString("query", mcp.Description("Alias for task (accepted; prefer task=)")),
		mcp.WithString("role", mcp.Description("Expert lens: architect (design Q&A, no edit yet) | security | performance | refactor | feature (default)")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("sections", mcp.Description("Optional comma list to return ONLY these sections (cheaper payload): orient,reuse,docs,decisions,steps,verify,findings. Empty = all, except simple vibe feature asks auto-default to orient,reuse,docs,steps,findings (what_next/next_queries always kept).")),
		mcp.WithBoolean("approve_network", mcp.Description("Allow inlining top library doc chunks via network (same gate as docs)"), mcp.DefaultBool(false)),
		mcp.WithBoolean("inline_docs", mcp.Description("When network is allowed, fetch top doc chunks into doc_previews (default true)"), mcp.DefaultBool(true)),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotReadOnlyOpenWorld(),
	), timedTool("kickoff", kickoffHandler(reg)))
}
