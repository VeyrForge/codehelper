package mcpsvc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/cochange"
	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/mcpimpact"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/review"
	"github.com/VeyrForge/codehelper/internal/security"
	"github.com/VeyrForge/codehelper/pkg/types"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ---- api_surface -----------------------------------------------------------

type apiSym struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Loc       string `json:"loc"`
	Recv      string `json:"recv,omitempty"`
	Signature string `json:"signature,omitempty"`
}

type apiSurfaceResponse struct {
	Path      string   `json:"path"`
	Exported  []apiSym `json:"exported"`
	Count     int      `json:"count"`
	Truncated int      `json:"truncated,omitempty"`
	Note      string   `json:"note"`
}

// maxAPISurface bounds the listed public symbols. 200 rows is ~5k tokens that
// then linger in the agent's context all session; 60 + a truncated count is
// enough to grasp a package's surface, and the agent can narrow the path or pass
// include_unexported for more.
const maxAPISurface = 60

// apiSurfaceHandler returns the PUBLIC API of a package/directory — its exported
// symbols with signatures — so the agent learns what a package exposes without
// reading every file in it. Deterministic; one query.
func apiSurfaceHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		path := strings.TrimSpace(argString(args, "path"))
		if path == "" {
			return mcp.NewToolResultError("path is required — a package/directory or file prefix (e.g. \"internal/retrieval\")"), nil
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

		includeUnexported := argBool(args, "include_unexported", false)
		syms, err := st.SymbolsByPathPrefix(ctx, repo.Name, path, 4000)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out := apiSurfaceResponse{Path: path}
		for _, s := range syms {
			if review.IsTestPath(s.Path) {
				continue
			}
			if !includeUnexported && !isExportedSymbol(s) {
				continue
			}
			out.Count++
			if len(out.Exported) >= maxAPISurface {
				out.Truncated++
				continue
			}
			out.Exported = append(out.Exported, apiSym{
				Name: s.Name, Kind: string(s.Kind),
				Loc:  fmt.Sprintf("%s:%d", s.Path, s.LineStart),
				Recv: s.ParentID, Signature: s.Signature,
			})
		}
		switch {
		case out.Count == 0:
			out.Note = fmt.Sprintf("no %s symbols under %q. Check the prefix (it's repo-relative, e.g. \"internal/foo\"), or the path may be unindexed — run `codehelper analyze`.",
				map[bool]string{true: "", false: "exported"}[includeUnexported], path)
		default:
			out.Note = "Exported symbols of this package (its public API). Each row's signature may carry the leading doc comment. Use `context`/`trace` to see how a symbol connects; pass include_unexported=true for internals."
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

// isExportedSymbol reports whether a symbol is part of its package's public API,
// by language convention: Go exports by an uppercase initial; languages without a
// case rule (Python/JS/Ruby/…) treat a leading underscore as private.
func isExportedSymbol(s types.Symbol) bool {
	if strings.HasPrefix(s.Name, "_") {
		return false
	}
	if s.Language == "go" {
		return startsWithUpper(s.Name)
	}
	return true
}

// ---- change_kit ------------------------------------------------------------

type callSite struct {
	Caller string `json:"caller"`
	Loc    string `json:"loc"`
	Code   string `json:"code,omitempty"`
}

type changeKitResponse struct {
	Target               apiSym          `json:"target"`
	Definition           string          `json:"definition,omitempty"`
	Callers              []callSite      `json:"callers"`
	CallerOver           int             `json:"callers_truncated,omitempty"`
	Tests                []compactSym    `json:"tests_covering"`
	RiskTier             string          `json:"risk_tier,omitempty"`
	CallGraphConfidence  string          `json:"call_graph_confidence,omitempty"`
	CoChanges            []cochange.Rule `json:"co_changes,omitempty"`
	Checklist            []string        `json:"checklist"`
	EditHint             string          `json:"edit_hint,omitempty"`
	Note                 string          `json:"note"`
	WhatNext             string          `json:"what_next,omitempty"`
	RecommendedNextTools []string        `json:"recommended_next_tools,omitempty"`
}

const changeKitMaxCallers = 25

// changeKitHandler assembles everything needed to change one symbol safely in a
// SINGLE call: its definition source, every call site (with the calling line),
// the tests that cover it, and a consistency checklist — so the agent edits
// without a flurry of read/grep round-trips. The token-saving extension of scout,
// aimed at the edit moment.
func changeKitHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		target := argFirst(args, "target", "name", "symbol", "sym")
		if target == "" {
			return toolResultRecoveryError(agentRecovery{
				ErrorCategory: ErrCategoryValidation,
				IsRetryable:   true,
				RecoveryHint:  RecoveryCheckInput,
				Message: "target is required — wrong/missing arg. Expected: {\"target\":\"SymbolName\"} (or sym: id from query). " +
					"Canonical key is target= — context/context_bundle use name= instead. " +
					"Aliases accepted: name, symbol, sym — retry once with target=….",
				Expected: "{\"target\":\"SymbolName\"}",
				Example:  map[string]any{"target": "LoginHandler"},
				WhatNext: "Retry change_kit with target=<exact name or sym: id from query>",
				RecommendedNextTools: []string{"query", "change_kit", "context"},
			}), nil
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

		// Bare vibe targets (health|ready|/healthz) and synthetic placement_*
		// seeds skip graph resolve — fuzzy hits like isClusterHealthy /
		// replacement_link must not win (LIVE-BROAD-V6: kickoff→change_kit).
		if isBareHealthishTarget(target) || isPlacementTarget(target) {
			if res, ok := changeKitHealthishFallback(repo.RootPath, target, resolveFormat(args)); ok {
				return res, nil
			}
		}

		// Prefer shared resolver (fixture demotion + fan-in auto-pick) over trace
		// ranking so change_kit and context/impact agree on ambiguous short names.
		wantPath := strings.TrimSpace(argFirst(args, "path", "file"))
		wantLine := int(argFloat(args, "line", 0))
		sym, cands, rerr := resolveSymbolByName(ctx, st, repo.Name, target, wantPath, wantLine)
		if rerr != nil || sym == nil {
			if sym == nil && len(cands) > 0 {
				// Fall back to most-connected exact match (trace policy) when fan-in
				// race was too close for auto-pick — still better than hard miss.
				if ts, terr := resolveTraceSymbol(ctx, st, repo.Name, target); terr == nil && ts != nil {
					sym = ts
				}
			}
			if sym == nil {
				if isHealthishTarget(target) {
					if res, ok := changeKitHealthishFallback(repo.RootPath, target, resolveFormat(args)); ok {
						return res, nil
					}
				}
				hint := fmt.Sprintf(
					"no indexed symbol named %q. Call `query` with that name (or a shorter unique fragment) to get the exact symbol / sym: id, then retry change_kit with target=<exact name or sym:…>. If the symbol is new, run `codehelper analyze --force` first.",
					target,
				)
				if len(cands) > 0 {
					locs := make([]string, 0, len(cands))
					for i, c := range cands {
						if i >= 5 {
							break
						}
						locs = append(locs, c.Name+" @ "+c.Loc+" ("+c.ID+")")
					}
					return toolResultRecoveryError(agentRecovery{
						ErrorCategory: ErrCategoryAmbiguous,
						IsRetryable:   true,
						RecoveryHint:  RecoveryDisambiguate,
						Message: fmt.Sprintf(
							"ambiguous symbol %q — pass path= (and optional line=) or target=<sym: id>. Candidates: %s",
							target, strings.Join(locs, "; "),
						),
						Expected: "path= or target=sym:…",
						Example:  map[string]any{"target": target, "path": "path/from/candidate"},
						WhatNext: "Retry change_kit with path= or a sym: id from candidates",
						RecommendedNextTools: []string{"query", "change_kit", "context"},
					}), nil
				}
				rec := agentRecovery{
					ErrorCategory: ErrCategoryNotFound,
					IsRetryable:   true,
					RecoveryHint:  RecoveryTryAlternative,
					Message:       hint,
					Expected:      "exact indexed symbol or sym: id",
					Example:       map[string]any{"target": target},
					WhatNext:      fmt.Sprintf("query: %s — then change_kit target=<exact>", target),
					RecommendedNextTools: []string{"query", "ast_query"},
					NextQueries: []string{
						fmt.Sprintf("query: %s", target),
						"codehelper analyze --force",
					},
				}
				if sugg := suggestSimilarSymbols(ctx, st, repo.Name, target, 5); len(sugg) > 0 {
					rec.Message += " Close indexed names: " + strings.Join(sugg, ", ") + "."
					rec.RecoveryHint = RecoveryDisambiguate
					rec.Example["close_names"] = sugg
				}
				return toolResultRecoveryError(rec), nil
			}
		}

		out := changeKitResponse{Target: apiSym{
			Name: sym.Name, Kind: string(sym.Kind),
			Loc:  fmt.Sprintf("%s:%d", sym.Path, sym.LineStart),
			Recv: sym.ParentID, Signature: sym.Signature,
		}}
		out.Definition = readSymbolDefinition(repo.RootPath, *sym, 40)

		callers, _ := st.CallersOf(ctx, repo.Name, sym.ID)
		sort.Slice(callers, func(i, j int) bool {
			ti, tj := review.IsTestPath(callers[i].Path), review.IsTestPath(callers[j].Path)
			if ti != tj {
				return !ti // non-test callers first
			}
			return callers[i].Path < callers[j].Path
		})
		for _, c := range callers {
			if len(out.Callers) >= changeKitMaxCallers {
				out.CallerOver++
				continue
			}
			line, lineNo := callSiteLine(repo.RootPath, c, sym.Name)
			loc := fmt.Sprintf("%s:%d", c.Path, c.LineStart)
			if lineNo > 0 {
				loc = fmt.Sprintf("%s:%d", c.Path, lineNo)
			}
			out.Callers = append(out.Callers, callSite{Caller: c.Name, Loc: loc, Code: line})
		}

		// Covering tests + risk tier via the upstream impact closure.
		tests := 0
		if res, aerr := mcpimpact.Analyze(ctx, st, repo.Name, sym.ID, 4, "upstream"); aerr == nil && res != nil {
			out.RiskTier = res.RiskTier
			for _, n := range res.Nodes {
				if n.Depth > 0 && review.IsTestPath(n.Path) && isTestSymbolKind(n.Kind) {
					if tests < maxTestImpact {
						out.Tests = append(out.Tests, compactSym{Name: n.Name, Kind: n.Kind, Loc: locOf(n.Path, n.SymbolID)})
					}
					tests++
				}
			}
		}

		// Evolutionary coupling: files that historically change WITH this symbol's
		// file (git history), surfacing architectural "don't forget to also edit Y"
		// dependencies the call graph can't see. Deterministic, best-effort.
		out.CoChanges = cochange.ForFile(repo.RootPath, sym.Path, cochange.Options{})

		out.Checklist = buildChangeChecklist(sym, len(callers)+out.CallerOver, tests)
		if conf := callGraphConfidence(ctx, st, repo.Name); conf != "" {
			out.CallGraphConfidence = conf
			out.Checklist = append(out.Checklist,
				"SPARSE CALL GRAPH — callers/tests_covering are under-counted; confirm with a textual search + the test suite before treating empty as safe.")
		}
		if len(out.CoChanges) > 0 {
			top := out.CoChanges[0]
			out.Checklist = append(out.Checklist, fmt.Sprintf(
				"In this repo's history, %s usually changes together with this file (%.0f%% of the time) — check whether it also needs updating.",
				top.File, top.Confidence*100))
		}
		out.Checklist = append(out.Checklist,
			"Edit via apply_patch_workspace_file (dry_run=true first) using the definition below — do not rewrite the whole file.")
		relPath := sym.Path
		out.EditHint = fmt.Sprintf(
			"apply_patch_workspace_file path=%s dry_run=true hunks=[{old_string:<copy from definition>, new_string:<edit>}]; then omit dry_run to write",
			relPath,
		)
		out.Note = "Everything to change this symbol in one shot: its definition (bounded), every call site (with the calling line), covering tests, historically co-changing files, and a consistency checklist. Prefer apply_patch dry_run → apply → diagnostics over whole-file rewrites."
		out.WhatNext = fmt.Sprintf(
			"Preview: apply_patch_workspace_file path=%s dry_run=true — then apply, then diagnostics → review_diff → verify → finish_check",
			relPath,
		)
		out.RecommendedNextTools = []string{"apply_patch_workspace_file", "diagnostics", "review_diff", "verify", "finish_check"}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

// changeKitHealthishFallback resolves bare health/ready targets via on-disk
// /health seed, HTTP framework placement, or a clear ABSTAIN. ok=false only when
// the target is not healthish (caller should continue with normal miss handling).
func changeKitHealthishFallback(root, target, format string) (*mcp.CallToolResult, bool) {
	if !isHealthishTarget(target) {
		return nil, false
	}
	readDef := func(loc string) (pathOnly string, lineNo int, def string) {
		pathOnly = loc
		if i := strings.LastIndex(loc, ":"); i > 0 {
			pathOnly = loc[:i]
			fmt.Sscanf(loc[i+1:], "%d", &lineNo)
		}
		if b, rerr := os.ReadFile(filepath.Join(root, filepath.FromSlash(pathOnly))); rerr == nil {
			lines := strings.Split(string(b), "\n")
			start := lineNo - 1
			if start < 0 {
				start = 0
			}
			end := start + 12
			if end > len(lines) {
				end = len(lines)
			}
			if start < len(lines) {
				def = strings.Join(lines[start:end], "\n")
			}
		}
		return pathOnly, lineNo, def
	}
	// Named placement_* from kickoff/scout — match that seed before generic health.
	if isPlacementTarget(target) {
		want := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(target, "sym:")))
		for _, s := range scanHTTPPlacementOnDisk(root, 24) {
			if !strings.EqualFold(s.Name, want) {
				continue
			}
			_, _, def := readDef(s.Loc)
			out := changeKitResponse{
				Target:     apiSym{Name: s.Name, Kind: "placement", Loc: s.Loc, Signature: s.Signature},
				Definition: def,
				Checklist: []string{
					"Synthetic placement seed from kickoff/scout (not an indexed symbol) — add GET /health near THIS router/app API.",
					"Do not invent a second HTTP stack; extend the existing framework entrypoint.",
					"Edit via apply_patch_workspace_file (dry_run=true first).",
					"After editing: diagnostics → review_diff → verify → finish_check.",
				},
				Note: "change_kit resolved placement target `" + s.Name + "` via HTTP placement seed. " +
					"what_next: add route near `" + s.Loc + "` — Next: diagnostics after patch.",
				WhatNext:             "Add GET /health near `" + s.Loc + "` via apply_patch dry_run=true — then diagnostics → review_diff → verify",
				RecommendedNextTools: []string{"apply_patch_workspace_file", "diagnostics", "review_diff", "verify", "finish_check"},
			}
			res, _ := mustToolResultFormatted(out, format)
			return res, true
		}
		// Named placement missing on disk — fall through to health / any placement.
	}
	if seeded := scanHealthRoutesOnDisk(root, 1); len(seeded) > 0 {
		s := seeded[0]
		_, _, def := readDef(s.Loc)
		out := changeKitResponse{
			Target:     apiSym{Name: s.Name, Kind: "route", Loc: s.Loc, Signature: s.Signature},
			Definition: def,
			Checklist: []string{
				"Disk-seeded /health|/ready route (may be anonymous / unindexed) — extend THIS file:line.",
				"Do not invent a second health stack; keep the existing path contract.",
				"Edit via apply_patch_workspace_file (dry_run=true first).",
				"After editing: diagnostics → review_diff → verify → finish_check.",
			},
			Note: "change_kit resolved healthish target via on-disk /health|/ready seed (graph miss). " +
				"what_next: extend `" + s.Loc + "` — Next: diagnostics after patch.",
			WhatNext:             "Extend `" + s.Loc + "` via apply_patch dry_run=true — then diagnostics → review_diff → verify",
			RecommendedNextTools: []string{"apply_patch_workspace_file", "diagnostics", "review_diff", "verify", "finish_check"},
		}
		res, _ := mustToolResultFormatted(out, format)
		return res, true
	}
	shape := security.DetectProjectShape(root)
	surface := security.DetectHTTPSurface(root)
	if surface.Capable {
		if place := scanHTTPPlacementOnDisk(root, 1); len(place) > 0 {
			s := place[0]
			_, _, def := readDef(s.Loc)
			out := changeKitResponse{
				Target:     apiSym{Name: s.Name, Kind: "placement", Loc: s.Loc, Signature: s.Signature},
				Definition: def,
				Checklist: []string{
					"HTTP-capable project (" + surface.Reason + ") — no existing /health|/ready found.",
					"Create GET /health (optional /ready) via this router/app API or an examples/ server — do not invent a second stack.",
					"Edit via apply_patch_workspace_file (dry_run=true first).",
					"After editing: diagnostics → review_diff → verify → finish_check.",
				},
				Note: "change_kit resolved healthish target via HTTP placement seed (no /health literal yet). " +
					"what_next: add route near `" + s.Loc + "` — Next: diagnostics after patch.",
				WhatNext:             "Add GET /health near `" + s.Loc + "` via apply_patch dry_run=true — then diagnostics → review_diff → verify",
				RecommendedNextTools: []string{"apply_patch_workspace_file", "diagnostics", "review_diff", "verify", "finish_check"},
			}
			res, _ := mustToolResultFormatted(out, format)
			return res, true
		}
	}
	next := vibeNextQueries("feature", shape, true, "add GET /health", root)
	msg := "ABSTAIN: no indexed or on-disk /health|/ready route for target=" + target +
		" (project_shape=" + string(shape) + "). "
	if surface.Capable {
		msg += "HTTP-capable (" + surface.Reason + ") — create path: add GET /health next to the router/app entry or examples/ server. "
		next = []string{
			"query: Router GET route add_url_rule APIRouter path HealthJSON",
			"change_kit target=<top router/placement from query> — add GET /health there",
			"diagnostics → review_diff → verify → finish_check after the patch",
		}
	} else if surface.Kind == "non_http" || shape == security.ShapeLibrary || shape == security.ShapeFrameworkCore {
		reason := surface.Reason
		if reason == "" {
			reason = "library/framework core may not expose HTTP"
		}
		msg += reason + " — do not invent /health. "
	} else {
		msg += "Create path: add a GET /health (and optional /ready) next to the existing router/server entrypoint. "
	}
	msg += "Next queries: (1) " + next[0] + "; (2) " + next[1] + "; (3) " + next[2]
	return toolResultRecoveryError(agentRecovery{
		ErrorCategory: ErrCategoryNotFound,
		IsRetryable:   true,
		RecoveryHint:  RecoveryReportToUser,
		Message:       msg,
		WhatNext:      "Follow next_queries — do not invent /health on non-HTTP cores",
		RecommendedNextTools: []string{"query", "kickoff", "change_kit"},
		NextQueries:   next,
	}), true
}

func buildChangeChecklist(sym *types.Symbol, callers, tests int) []string {
	var c []string
	if callers == 0 {
		c = append(c, "No resolved callers/readers — check for dynamic/reflective/cross-repo use and a textual search of the name before treating this as safe.")
	} else {
		c = append(c, fmt.Sprintf("%d caller/reader(s) — a signature change breaks them; update every call site below or keep it backward-compatible.", callers))
	}
	if tests == 0 {
		c = append(c, "No covering tests found — add a test for the new behavior before relying on it.")
	} else {
		c = append(c, fmt.Sprintf("%d test(s) cover this — run them after editing (use `test_impact`).", tests))
	}
	if review.IsTestPath(sym.Path) {
		c = append(c, "This symbol is itself in a test file.")
	}
	c = append(c, "After editing, run `diagnostics` to confirm it still builds/vets.")
	return c
}

// readSymbolDefinition returns the symbol's source lines (LineStart..LineEnd),
// capped at maxLines, for the agent to edit against.
func readSymbolDefinition(root string, sym types.Symbol, maxLines int) string {
	b, err := os.ReadFile(filepath.Join(root, sym.Path))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(b), "\n")
	start := sym.LineStart - 1
	if start < 0 {
		start = 0
	}
	end := sym.LineEnd
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	if end-start > maxLines {
		end = start + maxLines
	}
	if start >= len(lines) {
		return ""
	}
	return strings.Join(lines[start:end], "\n")
}

// ---- find_implementations --------------------------------------------------

type implCandidate struct {
	Type    string   `json:"type"`
	Loc     string   `json:"loc"`
	Missing []string `json:"missing,omitempty"` // interface methods the type lacks (partial impls)
}

type findImplResponse struct {
	Interface       string          `json:"interface"`
	Methods         []string        `json:"methods"`
	Implementations []implCandidate `json:"implementations"`
	Note            string          `json:"note"`
}

var ifaceMethodRe = regexp.MustCompile(`^\s*([A-Z]\w*)\s*\(`)

// findImplementationsHandler is a heuristic interface→implementation map for Go
// without go/types: it reads the interface's method set from source, then reports
// every type whose indexed methods cover that set (structural typing). Best-effort
// — it can't see methods on embedded types it didn't resolve — so partial matches
// are surfaced with the missing methods rather than hidden.
func findImplementationsHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		iface := strings.TrimSpace(argString(args, "interface"))
		if iface == "" {
			return mcp.NewToolResultError("interface is required — the Go interface type name (e.g. \"Reader\")."), nil
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

		sym, err := resolveTraceSymbol(ctx, st, repo.Name, iface)
		if err != nil || sym == nil {
			return mcp.NewToolResultError(fmt.Sprintf("no indexed type named %q. Use `query` to find the interface name.", iface)), nil
		}
		methods := interfaceMethods(repo.RootPath, *sym)
		out := findImplResponse{Interface: sym.Name, Methods: methods}
		if len(methods) == 0 {
			out.Note = fmt.Sprintf("%q has no extractable exported methods (it may be empty like `any`, an alias, or not an interface). Nothing to match.", sym.Name)
			return mustToolResultFormatted(out, resolveFormat(args))
		}

		byRecv, err := st.MethodNamesByReceiver(ctx, repo.Name)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		type cand struct {
			recv    string
			missing []string
		}
		var full, partial []cand
		for recv, set := range byRecv {
			if recv == sym.Name { // the interface's own (none, usually); skip self
				continue
			}
			var missing []string
			for _, m := range methods {
				if _, ok := set[m]; !ok {
					missing = append(missing, m)
				}
			}
			if len(missing) == 0 {
				full = append(full, cand{recv, nil})
			} else if len(missing) < len(methods) { // covers at least one method
				partial = append(partial, cand{recv, missing})
			}
		}
		sort.Slice(full, func(i, j int) bool { return full[i].recv < full[j].recv })
		sort.Slice(partial, func(i, j int) bool { return len(partial[i].missing) < len(partial[j].missing) })
		add := func(c cand) {
			loc := ""
			if defs, _ := st.SymbolsByName(ctx, repo.Name, c.recv, 1); len(defs) > 0 {
				loc = fmt.Sprintf("%s:%d", defs[0].Path, defs[0].LineStart)
			}
			out.Implementations = append(out.Implementations, implCandidate{Type: c.recv, Loc: loc, Missing: c.missing})
		}
		for _, c := range full {
			add(c)
		}
		for i, c := range partial {
			if i >= 10 {
				break
			}
			add(c)
		}
		switch {
		case len(full) == 0 && len(partial) == 0:
			out.Note = fmt.Sprintf("no type implements %s by structural method-name match. Implementations may live in dependencies, or use embedding the static graph didn't resolve.", sym.Name)
		case len(full) == 0:
			out.Note = "no full implementer found; the listed types match SOME interface methods (see `missing`). A partial match often means embedding — check the embedded type."
		default:
			out.Note = fmt.Sprintf("%d type(s) implement %s (method-name structural match; heuristic — verify receiver pointer-ness and signatures). Partial matches (with missing methods) follow.", len(full), sym.Name)
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

// interfaceMethods extracts exported method names from a Go interface's source.
func interfaceMethods(root string, sym types.Symbol) []string {
	b, err := os.ReadFile(filepath.Join(root, sym.Path))
	if err != nil {
		return nil
	}
	lines := strings.Split(string(b), "\n")
	start, end := sym.LineStart-1, sym.LineEnd
	if start < 0 {
		start = 0
	}
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	seen := map[string]struct{}{}
	var out []string
	for i := start; i < end; i++ {
		if m := ifaceMethodRe.FindStringSubmatch(lines[i]); m != nil {
			if _, ok := seen[m[1]]; !ok {
				seen[m[1]] = struct{}{}
				out = append(out, m[1])
			}
		}
	}
	return out
}

// suggestSimilarSymbols returns a few nearby indexed names when change_kit misses,
// so agents can recover without a blind re-query. Uses SymbolsByName's prefix/fuzzy
// LIKE over camelCase fragments of the requested target.
func suggestSimilarSymbols(ctx context.Context, st *graph.Store, repoID, target string, limit int) []string {
	if st == nil || strings.TrimSpace(target) == "" || limit <= 0 {
		return nil
	}
	frags := symbolNameFragments(target)
	seen := map[string]bool{}
	var out []string
	for _, frag := range frags {
		if len(frag) < 3 {
			continue
		}
		syms, err := st.SymbolsByName(ctx, repoID, frag, limit*3)
		if err != nil {
			continue
		}
		for _, s := range syms {
			if strings.EqualFold(s.Name, target) || seen[s.Name] {
				continue
			}
			seen[s.Name] = true
			out = append(out, fmt.Sprintf("%s @ %s:%d", s.Name, s.Path, s.LineStart))
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

// symbolNameFragments yields the full name plus camelCase/snake pieces for fuzzy lookup.
func symbolNameFragments(name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	out := []string{name}
	var cur strings.Builder
	flush := func() {
		if cur.Len() >= 3 {
			out = append(out, cur.String())
		}
		cur.Reset()
	}
	for i, r := range name {
		if r == '_' || r == '-' || r == '.' {
			flush()
			continue
		}
		if i > 0 && r >= 'A' && r <= 'Z' {
			flush()
		}
		cur.WriteRune(r)
	}
	flush()
	return out
}
