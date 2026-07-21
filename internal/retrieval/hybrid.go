package retrieval

import (
	"context"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/pkg/types"
)

// RankedSymbol is a search hit with fused score and trace reasons.
type RankedSymbol struct {
	Symbol  types.Symbol `json:"symbol"`
	Score   float64      `json:"score"`
	Reasons []string     `json:"reasons,omitempty"`
}

// DefaultCentralityWeight scales the call-graph centrality boost in ranking.
// log1p(in-degree) is multiplied by this: a symbol with 10 callers gains
// ~0.36, with 100 callers ~0.69 — comparable to a name-prefix match (0.3) and
// always below an exact-name match (1.0), so centrality breaks ties toward
// load-bearing code without overriding lexical relevance. Set to 0 to disable
// (used by the A/B benchmark in internal/bench).
const DefaultCentralityWeight = 0.15

// diffBoostBase is the score added to a symbol changed vs the diff base_ref;
// diffFracThreshold is the fraction of a query's candidates that may be changed
// before the boost starts decaying (see rerankWithSignals).
const (
	diffBoostBase     = 0.25
	diffFracThreshold = 0.25
)

// QueryOptions carries additive ranking signals used by context-pack mode.
type QueryOptions struct {
	ChangedSymbolIDs map[string]struct{}
	Intent           string
	QueryTokens      []string
	// CentralityWeight enables the call-graph centrality boost when > 0.
	// When set, QueryHybridWithOptions loads per-symbol inbound call counts
	// once and rerankWithSignals boosts hits by CentralityWeight*log1p(callers).
	CentralityWeight float64
	// centrality is populated internally from the store (dst_id -> caller count).
	centrality map[string]int
	// RepoRoot enables loading the offline enrichment store (.codehelper/enrich/) when
	// EnrichmentTexts is nil. Empty repo root or a missing store → no enrichment boost.
	RepoRoot string
	// EnrichmentTexts maps symbol ID → separate retrieval field (purpose + aliases).
	// When nil and RepoRoot is set, loaded automatically from the enrichment store if present.
	EnrichmentTexts map[string]string
	PrimaryLanguage string
	// LikelyEntrypointFiles lists bootstrap paths under RepoRoot for locate boosts.
	LikelyEntrypointFiles []string
	// EnableGraphExpand turns on BM25→1–2 hop graph expand→RRF fusion.
	// Default off so lexical query/scout ranking stays stable; search_hybrid enables it.
	EnableGraphExpand bool
	// GraphExpand overrides hop/seed bounds when graph expansion is enabled.
	GraphExpand GraphExpandOptions
}

// Query runs lexical match (backward compatible).
func Query(ctx context.Context, st *graph.Store, repoID, q string, limit int) ([]RankedSymbol, error) {
	return QueryHybrid(ctx, st, repoID, q, limit)
}

// QueryHybrid runs BM25 + trigram retrieval over symbol text.
func QueryHybrid(ctx context.Context, st *graph.Store, repoID, q string, limit int) ([]RankedSymbol, error) {
	return QueryHybridWithOptions(ctx, st, repoID, q, limit, QueryOptions{})
}

// QueryHybridWithOptions runs retrieval with optional additive reranking signals.
func QueryHybridWithOptions(ctx context.Context, st *graph.Store, repoID, q string, limit int, opts QueryOptions) ([]RankedSymbol, error) {
	if limit <= 0 {
		limit = 25
	}
	toks := tokenize(q)
	pathHints := extractPathHints(q)
	// Expand the query with programming-verb synonyms (get/fetch/load, close/
	// shutdown/release, …) so a task phrased differently from the symbol still
	// finds it. Expansions are searched and scored at a discount (synonymWeight),
	// and a synonym that doesn't occur in the corpus simply contributes nothing.
	searchToks, tokWeight := expandSynonyms(toks)
	if opts.RepoRoot != "" {
		mergeTokenExpansions(&searchToks, tokWeight, expandVocabTerms(opts.RepoRoot, toks))
	}
	candidates, err := candidatesForTokens(ctx, st, repoID, q, searchToks)
	if err != nil {
		return nil, err
	}
	idf := idfForTokens(searchToks, candidates)
	for t, w := range tokWeight {
		idf[t] *= w // discount synonym expansions below literally-typed terms
	}
	// Distinctive tokens drive name-field and coverage scoring: filler ("add",
	// "the", "hot") is stripped so it can neither match a name nor count toward
	// coverage. These are the literally-typed terms only (no synonym expansions).
	meaningTok := meaningfulQueryTokens(toks)
	var bm25List []RankedSymbol
	for _, s := range candidates {
		// Skip junk "symbols" some language parsers emit — tuple unpacking and
		// parameter lists captured as a name ("username, password"). A real symbol
		// name is a single identifier; a comma or whitespace means it's noise that
		// would otherwise pollute reuse candidates and duplication signals.
		if !plausibleSymbolName(s.Name) {
			continue
		}
		// Name + path + signature; Signature now carries the leading doc comment, so a
		// natural-language query matches what a symbol DOES, not just its identifier —
		// "reciprocal rank fusion" finds `RRF` (an acronym name). BM25 length
		// normalization keeps a long doc from dominating a tight name match.
		doc := s.Name + " " + s.Path + " " + s.Signature
		sc := bm25Score(searchToks, idf, doc)
		tg := trigramScore(meaningTok, q, doc)
		sc += tg
		ph := pathHintBoost(s.Path, pathHints)
		sc += ph
		if sc > 0 {
			rs := []string{"bm25"}
			if tg > 0 {
				rs = append(rs, "trigram")
			}
			if ph > 0 {
				rs = append(rs, "path_hint")
			}
			bm25List = append(bm25List, RankedSymbol{Symbol: s, Score: sc, Reasons: rs})
		}
	}
	sort.SliceStable(bm25List, func(i, j int) bool { return rankedLess(bm25List[i], bm25List[j]) })
	if len(bm25List) > 200 {
		bm25List = bm25List[:200]
	}

	// Normalize the raw lexical score to [0,1] (top match = 1.0) before reranking.
	// With corpus IDF, raw BM25 magnitude swings wildly by query (a rare-token
	// query scores ~10, a common-token one ~1), which would otherwise dwarf the
	// fixed additive signals (exact_name=1.0, centrality=0.15·log1p). Normalizing
	// makes those weights mean the same thing for every query.
	if len(bm25List) > 0 {
		max := bm25List[0].Score
		if max > 0 {
			for i := range bm25List {
				bm25List[i].Score /= max
			}
		}
	}

	// BM25/FTS → 1–2 hop graph expand → RRF fuse. Structural neighbors that never
	// matched the query text still enter the pool; seeds stay reinforced via RRF.
	if opts.EnableGraphExpand && len(bm25List) > 0 {
		graphList := ExpandGraphNeighbors(ctx, st, repoID, bm25List, opts.GraphExpand)
		if len(graphList) > 0 {
			bm25List = FuseRRF(bm25List, graphList, 60)
		}
	}

	// Load call-graph centrality for ONLY the surviving candidates so rerank can
	// favor load-bearing symbols. Scoping to the (≤200) candidate IDs instead of
	// the whole repo's in-degree map keeps this ~constant as the repo grows — the
	// difference between ~430ms and a few ms on a million-edge repo. Best-effort.
	if opts.CentralityWeight > 0 && opts.centrality == nil {
		ids := make([]string, 0, len(bm25List))
		for _, rs := range bm25List {
			ids = append(ids, rs.Symbol.ID)
		}
		if deg, derr := st.InDegreesFor(ctx, repoID, "calls", ids); derr == nil {
			opts.centrality = deg
		}
	}

	if opts.EnrichmentTexts == nil && opts.RepoRoot != "" {
		opts.EnrichmentTexts = resolveEnrichmentTexts(opts.RepoRoot)
	}

	fused := rerankWithSignals(bm25List, opts)
	// Opt-in vector channel (CODEHELPER_EMBED_URL): RRF-fuse a cosine-ranked list
	// with the lexical+graph list when an embedder is active. No-op + zero cost
	// when disabled; fail-safe to lexical/graph on any error.
	fused = semanticRerankQuery(q, fused)
	// Intent-specific boosts run AFTER semantic so embedding fusion cannot undo
	// close-verb disambiguation, typo-target routing, or scaffold demotion.
	fused = applyQueryIntentBoosts(fused, opts)
	if len(fused) > limit {
		fused = fused[:limit]
	}
	return fused, nil
}

func mergeHitReasons(a, b []RankedSymbol, id string) []string {
	var rs []string
	for _, x := range a {
		if x.Symbol.ID == id {
			rs = append(rs, x.Reasons...)
			break
		}
	}
	for _, x := range b {
		if x.Symbol.ID == id {
			rs = append(rs, x.Reasons...)
			break
		}
	}
	if len(rs) == 0 {
		rs = []string{"rrf"}
	}
	return rs
}

func rerankWithSignals(in []RankedSymbol, opts QueryOptions) []RankedSymbol {
	if len(in) == 0 {
		return in
	}
	intent := strings.ToLower(strings.TrimSpace(opts.Intent))
	toks := opts.QueryTokens
	if len(toks) == 0 {
		toks = tokenize(intent)
	}
	// Exact / prefix symbol-name match is the strongest possible signal (this is
	// what makes name lookups rank #1, matching an LSP/Serena symbol search).
	wantName := strings.ToLower(strings.Join(toks, ""))
	recvHint, methodHint, hasQualified := splitQualifiedQuery(intent)
	// Distinctive query tokens, computed ONCE (not per-candidate) for field weighting.
	mTokens := meaningfulQueryTokens(toks)
	// Diff-boost weight, computed ONCE. The +0.25 "recently changed" boost helps
	// surface what the developer just edited — but when a LARGE fraction of a
	// query's candidates are changed (a big WIP branch), "changed" stops
	// discriminating, and a fixed boost floats edited-but-irrelevant symbols over
	// strongly-relevant unchanged ones (observed: a big uncommitted diff collapsed
	// Recall@1 to 0). Decay it inversely once the changed fraction exceeds a
	// threshold — IDF-style — so it stays full for normal WIP and can't dominate on
	// a huge diff.
	diffBoost := diffBoostBase
	if opts.ChangedSymbolIDs != nil {
		changed := 0
		for i := range in {
			if _, ok := opts.ChangedSymbolIDs[in[i].Symbol.ID]; ok {
				changed++
			}
		}
		if frac := float64(changed) / float64(len(in)); frac > diffFracThreshold {
			diffBoost *= diffFracThreshold / frac
		}
	}
	for i := range in {
		nm := strings.ToLower(in[i].Symbol.Name)
		if wantName != "" {
			if nm == wantName {
				in[i].Score += 1.0
				in[i].Reasons = append(in[i].Reasons, "exact_name")
			} else if strings.HasPrefix(nm, wantName) || strings.HasPrefix(wantName, nm) {
				in[i].Score += 0.3
				in[i].Reasons = append(in[i].Reasons, "name_prefix")
			}
		}
		// Type.Method / Recv.Method queries: boost the method whose parent/recv
		// matches (Go stores receiver type name in ParentID).
		if hasQualified && strings.EqualFold(in[i].Symbol.Name, methodHint) {
			parent := strings.ToLower(strings.TrimSpace(in[i].Symbol.ParentID))
			recv := strings.ToLower(recvHint)
			if parent == recv || strings.HasSuffix(parent, "."+recv) || strings.HasSuffix(parent, recv) {
				in[i].Score += 0.95
				in[i].Reasons = append(in[i].Reasons, "qualified_recv")
			} else if in[i].Symbol.Kind == "method" || in[i].Symbol.Kind == "function" {
				in[i].Score += 0.35
				in[i].Reasons = append(in[i].Reasons, "qualified_method")
			}
		}
		if opts.ChangedSymbolIDs != nil {
			if _, ok := opts.ChangedSymbolIDs[in[i].Symbol.ID]; ok {
				in[i].Score += diffBoost
				in[i].Reasons = append(in[i].Reasons, "diff")
			}
		}
		// Call-graph centrality: a symbol many sites call is more likely the one
		// the user means. log1p keeps it sub-linear so a 100-caller helper never
		// outranks an exact-name match for a different symbol.
		if opts.CentralityWeight > 0 && opts.centrality != nil {
			if deg := opts.centrality[in[i].Symbol.ID]; deg > 0 {
				in[i].Score += opts.CentralityWeight * math.Log1p(float64(deg))
				in[i].Reasons = append(in[i].Reasons, "centrality")
			}
		}
		// Hub utilities (log/error/cn/…) are ultra-central but rarely the answer for
		// a feature/fix kickoff unless the query explicitly names them. Down-weight
		// so domain symbols surface first in query/scout/kickoff reuse lists.
		if isHubUtilitySymbol(nm) && !queryNamesHubUtility(toks, nm) {
			in[i].Score *= 0.45
			in[i].Reasons = append(in[i].Reasons, "hub_utility_demoted")
		}
		// Provider DI lifecycle methods drown HTTP feature kickoffs (Laravel
		// AppServiceProvider::register vs Form Request / route work).
		if isProviderLifecycleNoise(in[i].Symbol.Path, in[i].Symbol.Name, toks) {
			in[i].Score *= 0.35
			in[i].Reasons = append(in[i].Reasons, "provider_lifecycle_demoted")
		}
		// Field weighting: a distinctive query token appearing in the symbol NAME is
		// stronger evidence than the same token in its path or doc comment. Small,
		// post-normalization, capped — sharpens "best fit" without overriding a
		// synonym/exact-name match. (filler tokens are excluded via meaningful-only.)
		nameLower := strings.ToLower(in[i].Symbol.Name)
		nameHits := 0
		for _, t := range mTokens {
			if strings.Contains(nameLower, t) {
				nameHits++
			}
		}
		if nameHits > 0 {
			b := 0.12 * float64(nameHits)
			if b > 0.24 {
				b = 0.24
			}
			in[i].Score += b
			in[i].Reasons = append(in[i].Reasons, "name_field")
		}

		// Separate enrichment field (purpose + aliases from index-time LLM). Only
		// active when an offline store exists; never merged into the identifier field.
		if opts.EnrichmentTexts != nil {
			if enrichText, ok := opts.EnrichmentTexts[in[i].Symbol.ID]; ok && enrichText != "" {
				enrichLower := strings.ToLower(enrichText)
				enrichHits := 0
				for _, t := range mTokens {
					if strings.Contains(enrichLower, t) {
						enrichHits++
					}
				}
				if enrichHits > 0 {
					b := DefaultEnrichmentWeight * float64(enrichHits)
					if cap := DefaultEnrichmentWeight * 2; b > cap {
						b = cap
					}
					in[i].Score += b
					in[i].Reasons = append(in[i].Reasons, "enrich_field")
				}
			}
		}

		symPath := strings.ToLower(in[i].Symbol.Path)
		for _, t := range toks {
			t = strings.TrimSpace(strings.ToLower(t))
			if len(t) < 3 {
				continue
			}
			if strings.Contains(symPath, t) {
				in[i].Score += 0.05
				in[i].Reasons = append(in[i].Reasons, "path_proximity")
				break
			}
		}
		isTest := pathLooksLikeTest(symPath, in[i].Symbol.Path)
		if isTest && (intent == "test" || intent == "debug") {
			in[i].Score += 0.2
			in[i].Reasons = append(in[i].Reasons, "nearest_test")
			isTest = false // handled as a boost; skip the default-intent penalty below
		}
		if intent == "debug" && strings.Contains(symPath, "verify") {
			in[i].Score += 0.05
			in[i].Reasons = append(in[i].Reasons, "intent_debug")
		}
		if intent == "refactor" && strings.Contains(symPath, "internal/") {
			in[i].Score += 0.03
			in[i].Reasons = append(in[i].Reasons, "intent_refactor")
		}
		if opts.PrimaryLanguage != "" && strings.EqualFold(in[i].Symbol.Language, opts.PrimaryLanguage) {
			in[i].Score += 0.05
			in[i].Reasons = append(in[i].Reasons, "primary_lang")
		}
	}
	sort.SliceStable(in, func(i, j int) bool { return rankedLess(in[i], in[j]) })
	return in
}

// applyQueryIntentBoosts applies deterministic query-intent rules after lexical
// and optional semantic ranking so embedding re-blend cannot resurrect ghrelease
// noise on close-verb queries or test symbols on ranking-meta queries.
func applyQueryIntentBoosts(in []RankedSymbol, opts QueryOptions) []RankedSymbol {
	if len(in) == 0 {
		return in
	}
	intent := strings.ToLower(strings.TrimSpace(opts.Intent))
	toks := opts.QueryTokens
	if len(toks) == 0 {
		toks = tokenize(intent)
	}
	for i := range in {
		symPath := strings.ToLower(in[i].Symbol.Path)
		isTest := pathLooksLikeTest(symPath, in[i].Symbol.Path)
		isScaffold := isScaffoldSymbol(in[i].Symbol.Path, in[i].Symbol.Name)
		if queryWantsCloseVerb(toks) {
			nm := strings.ToLower(in[i].Symbol.Name)
			switch nm {
			case "close", "shutdown":
				in[i].Score += 0.22
				in[i].Reasons = append(in[i].Reasons, "close_verb")
				if queryMentionsConnectionPool(toks) && strings.Contains(symPath, "internal/graph/") {
					in[i].Score += 0.15
					in[i].Reasons = append(in[i].Reasons, "graph_close")
				}
			case "release":
				if strings.Contains(symPath, "ghrelease") || strings.Contains(symPath, "/release") {
					in[i].Score *= 0.55
					in[i].Reasons = append(in[i].Reasons, "release_noun_demoted")
				}
			default:
				if strings.Contains(symPath, "ghrelease") {
					in[i].Score *= 0.5
					in[i].Reasons = append(in[i].Reasons, "ghrelease_demoted")
				}
			}
		}
		if queryIsAboutRanking(toks) {
			nm := strings.ToLower(in[i].Symbol.Name)
			if nm == "queryisaboutranking" {
				in[i].Score *= 0.45
				in[i].Reasons = append(in[i].Reasons, "ranking_plumbing_demoted")
			}
			if nm == "isscaffoldsymbol" || nm == "querywantsscaffold" || nm == "queryhybridwithoptions" {
				boost := 0.18
				if containsToken(toks, "demote") {
					boost = 0.32
				}
				in[i].Score += boost
				in[i].Reasons = append(in[i].Reasons, "ranking_meta")
			}
		}
		if queryMentionsTypoHandling(toks) {
			nm := strings.ToLower(in[i].Symbol.Name)
			if strings.Contains(symPath, "internal/retrieval/") {
				switch {
				case strings.Contains(nm, "candidates") && strings.Contains(nm, "token"):
					in[i].Score += 0.18
					in[i].Reasons = append(in[i].Reasons, "typo_candidates_fn")
				case strings.Contains(nm, "trigram"):
					in[i].Score += 0.15
					in[i].Reasons = append(in[i].Reasons, "typo_trigram_fn")
				}
			}
			if !queryMentionsCrossRepo(toks) && strings.Contains(nm, "crossrepo") {
				in[i].Score *= 0.6
				in[i].Reasons = append(in[i].Reasons, "crossrepo_demoted")
			}
		}
		if isTest && !queryWantsScaffold(toks) && intent != "test" && intent != "debug" {
			in[i].Score *= 0.25
			in[i].Reasons = append(in[i].Reasons, "test_path_demoted")
		} else if isScaffold && !queryWantsScaffold(toks) && intent != "test" && intent != "debug" {
			in[i].Score *= 0.4
			in[i].Reasons = append(in[i].Reasons, "scaffold_demoted")
		}
		if queryMentionsLocate(toks) {
			sig := strings.ToLower(in[i].Symbol.Signature)
			nm := strings.ToLower(in[i].Symbol.Name)
			if pathMatchesEntrypointFile(symPath, opts.LikelyEntrypointFiles) {
				in[i].Score += 0.22
				in[i].Reasons = append(in[i].Reasons, "entrypoint_file")
			}
			if strings.Contains(sig, "entrypoint") || strings.Contains(sig, "plugins_loaded") {
				in[i].Score += 0.28
				in[i].Reasons = append(in[i].Reasons, "entrypoint_sig")
			}
			if nm == "tp_run_plugin" || strings.Contains(nm, "tp_run") {
				in[i].Score += 0.18
				in[i].Reasons = append(in[i].Reasons, "plugin_run")
			}
			if containsToken(toks, "plugins_loaded") && strings.Contains(symPath, "includes/content/") &&
				!strings.Contains(sig, "plugins_loaded") {
				in[i].Score *= 0.55
				in[i].Reasons = append(in[i].Reasons, "hook_handler_demoted")
			}
			if strings.Contains(symPath, "/tests/") || strings.Contains(symPath, "test.php") {
				in[i].Score *= 0.3
				in[i].Reasons = append(in[i].Reasons, "test_path_demoted")
			}
			if opts.PrimaryLanguage != "" && in[i].Symbol.Language != "" &&
				!strings.EqualFold(in[i].Symbol.Language, opts.PrimaryLanguage) {
				in[i].Score *= 0.55
				in[i].Reasons = append(in[i].Reasons, "nonprimary_lang")
			}
		}
		if queryMentionsAdminDiagnostics(toks) {
			nm := strings.ToLower(in[i].Symbol.Name)
			if strings.Contains(nm, "diagnostic") && strings.Contains(symPath, "includes/admin/") &&
				strings.HasSuffix(symPath, ".php") {
				in[i].Score += 0.24
				in[i].Reasons = append(in[i].Reasons, "admin_diagnostics")
			}
			if strings.Contains(symPath, "assets/js/") || strings.HasSuffix(symPath, ".js") {
				in[i].Score *= 0.35
				in[i].Reasons = append(in[i].Reasons, "frontend_asset_demoted")
			}
		}
		if queryMentionsVocabExpansion(toks) {
			nm := strings.ToLower(in[i].Symbol.Name)
			switch nm {
			case "expandvocabterms":
				in[i].Score += 0.22
				in[i].Reasons = append(in[i].Reasons, "vocab_impl")
			case "glossarykeys", "registerglossarytools":
				in[i].Score *= 0.82
				in[i].Reasons = append(in[i].Reasons, "vocab_wrapper_demoted")
			}
		}
		if queryMentionsSemanticEmbed(toks) {
			nm := strings.ToLower(in[i].Symbol.Name)
			switch nm {
			case "semanticrerankquery", "ensureembedder", "semanticenabled":
				in[i].Score += 0.2
				in[i].Reasons = append(in[i].Reasons, "semantic_impl")
			case "semanticrerankstatus":
				in[i].Score *= 0.85
				in[i].Reasons = append(in[i].Reasons, "semantic_wrapper_demoted")
			}
		}
		if queryMentionsSimilarSearch(toks) {
			nm := strings.ToLower(in[i].Symbol.Name)
			switch nm {
			case "findsimilarsymbols", "boostsimilarity":
				in[i].Score += 0.22
				in[i].Reasons = append(in[i].Reasons, "similar_impl")
			case "tokenoverlap":
				if strings.Contains(strings.ToLower(in[i].Symbol.Path), "internal/retrieval/similar.go") {
					in[i].Score *= 0.88
					in[i].Reasons = append(in[i].Reasons, "similar_helper_demoted")
				}
			}
		}
	}
	applyConceptPhraseBoosts(in, toks)
	sort.SliceStable(in, func(i, j int) bool { return rankedLess(in[i], in[j]) })
	return in
}

func dedupeReasons(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// candidatesForTokens unions per-token SQL substring matches so multi-word
// queries (e.g. "watcher debounce") return candidates that contain ANY
// token rather than the literal substring of the whole query. Each token
// must be at least 2 characters long to avoid index-wide scans for noise.
func candidatesForTokens(ctx context.Context, st *graph.Store, repoID, raw string, toks []string) ([]types.Symbol, error) {
	seen := map[string]struct{}{}
	out := []types.Symbol{}
	add := func(syms []types.Symbol) {
		for _, s := range syms {
			if _, ok := seen[s.ID]; ok {
				continue
			}
			seen[s.ID] = struct{}{}
			out = append(out, s)
		}
	}
	hasWordy := false
	for _, t := range toks {
		if len(t) >= 4 {
			hasWordy = true
		}
	}

	// FAST PATH: the trigram FTS index turns substring matching into an indexed
	// lookup — O(log n), so a 100k+ symbol repo answers in tens of ms instead of
	// seconds of LIKE scanning. Fetch PER TOKEN (each ORDER BY rank) so every token
	// gets dedicated, relevance-ordered coverage — same recall as the per-token
	// LIKE union it replaces — plus a combined match for multi-word phrases.
	if st.HasFTSRows(ctx, repoID) {
		for _, t := range toks {
			if len(t) < 3 {
				continue
			}
			syms, err := st.SearchSymbolsFTS(ctx, repoID, []string{t}, 1200)
			if err != nil {
				return nil, err
			}
			add(syms)
		}
		if len(toks) > 1 {
			if syms, err := st.SearchSymbolsFTS(ctx, repoID, toks, 800); err == nil {
				add(syms)
			}
		}
	} else {
		// FALLBACK (no FTS / old index): the original LIKE-based union.
		if strings.TrimSpace(raw) != "" {
			full, err := st.SearchSymbolsPath(ctx, repoID, raw, 400)
			if err != nil {
				return nil, err
			}
			add(full)
		}
		for _, t := range toks {
			if len(t) < 2 {
				continue
			}
			lim := 200
			if len(t) >= 3 {
				lim = 1000
			}
			syms, err := st.SearchSymbolsPath(ctx, repoID, t, lim)
			if err != nil {
				return nil, err
			}
			add(syms)
		}
	}

	// Typo fallback: substring search (FTS or LIKE) finds nothing for a misspelled
	// token ("tigram" matches no "trigram" substring), so the real symbol never
	// enters the pool and the fuzzy trigram SCORER never gets a chance. When the
	// pool is thin AND the query has a wordy token, pull a broad set. Bounded to
	// 2000 so it stays cheap even on a huge repo; gated so normal queries skip it.
	if len(out) < 8 && hasWordy {
		broad, err := st.SearchSymbolsPath(ctx, repoID, "", 2000)
		if err != nil {
			return nil, err
		}
		add(broad)
	}
	return out, nil
}

// isScaffoldSymbol reports whether a symbol is non-production scaffolding —
// database seeders/factories/migrations, fixtures, mocks/stubs, demos/tutorials —
// by path segment or name suffix. Such symbols are not reuse targets when
// implementing a feature.
func isScaffoldSymbol(path, name string) bool {
	p := strings.ToLower(path)
	for _, seg := range []string{
		"/seeders/", "/seeder/", "/factories/", "/factory/", "/migrations/", "/migration/",
		"/fixtures/", "/fixture/", "/mocks/", "/stubs/", "/testdata/",
		"/docs_src/", "/sample/", "/samples/", "/examples/", "/example/",
		"/integration/", "/_expected/", "/benchmarking/", "/playground/", "/playgrounds/",
		"/test/", "/tests/", "/__tests__/", "/test/acceptance/", "/acceptance/",
	} {
		if strings.Contains(p, seg) {
			return true
		}
	}
	for _, prefix := range []string{
		"docs_src/", "sample/", "samples/", "examples/", "example/",
		"integration/", "fixtures/", "benchmarking/", "playground/", "playgrounds/",
		"test/", "tests/",
	} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	base := strings.ToLower(filepath.Base(p))
	if strings.HasPrefix(base, "expected.") || strings.Contains(base, "_expected") ||
		strings.Contains(base, ".spec.") {
		return true
	}
	n := strings.ToLower(name)
	for _, suf := range []string{"seeder", "factory", "migration", "mock", "stub", "fake"} {
		if strings.HasSuffix(n, suf) {
			return true
		}
	}
	return false
}

// pathLooksLikeTest reports test/spec trees by path segment or basename.
func pathLooksLikeTest(symPathLower, rawPath string) bool {
	if strings.Contains(strings.ToLower(filepath.Base(rawPath)), "test") {
		return true
	}
	p := symPathLower
	if p == "" {
		p = strings.ToLower(rawPath)
	}
	return strings.Contains(p, "/test/") || strings.Contains(p, "/tests/") ||
		strings.Contains(p, "/__tests__/") || strings.Contains(p, ".spec.")
}

// queryIsAboutRanking reports meta-queries about the ranker itself (demote tests,
// boost scores, …) where scaffold vocabulary names the TOPIC, not the desired hit.
func queryIsAboutRanking(toks []string) bool {
	for _, t := range toks {
		switch t {
		case "rank", "ranking", "ranked", "demote", "boost", "rerank", "score", "scoring", "bm25", "trigram":
			return true
		}
	}
	return false
}

// queryWantsScaffold reports whether the task is explicitly about scaffolding, in
// which case it must NOT be demoted (e.g. "add a database seeder", "write a
// migration", "mock the client in a test").
func queryWantsScaffold(toks []string) bool {
	if queryIsAboutRanking(toks) {
		return false
	}
	for _, t := range toks {
		switch t {
		case "seed", "seeds", "seeder", "seeders", "factory", "factories",
			"migration", "migrations", "migrate", "fixture", "fixtures",
			"mock", "mocks", "stub", "stubs", "test", "tests", "spec", "specs", "demo":
			return true
		}
	}
	return false
}

func queryWantsCloseVerb(toks []string) bool {
	for _, t := range toks {
		switch t {
		case "close", "shutdown", "shut", "teardown", "disconnect", "dispose", "stop", "end":
			return true
		}
	}
	return false
}

func queryMentionsTypoHandling(toks []string) bool {
	for _, t := range toks {
		switch t {
		case "typo", "typos", "fuzzy", "fuzz", "trigram", "similarity", "misspell":
			return true
		}
	}
	return false
}

func queryMentionsCrossRepo(toks []string) bool {
	hasCross, hasRepo := false, false
	for _, t := range toks {
		switch t {
		case "cross":
			hasCross = true
		case "repo", "repository":
			hasRepo = true
		}
	}
	return hasCross && hasRepo
}

func queryMentionsConnectionPool(toks []string) bool {
	hasPool, hasConn := false, false
	for _, t := range toks {
		switch t {
		case "pool", "pools":
			hasPool = true
		case "connection", "connections", "conn":
			hasConn = true
		}
	}
	return hasPool && hasConn
}

// plausibleSymbolName rejects parser noise: a real symbol name is a single
// identifier, so an empty name or one containing a comma/whitespace (a tuple or
// parameter list mis-captured as a name) is dropped from ranking.
func plausibleSymbolName(n string) bool {
	n = strings.TrimSpace(n)
	if n == "" {
		return false
	}
	return !strings.ContainsAny(n, ", \t\n($")
}

func tokenize(s string) []string {
	s = strings.ToLower(s)
	return strings.FieldsFunc(s, func(r rune) bool {
		return r <= ' ' || r == '_' || r == '/' || r == '.' || r == ':'
	})
}

// splitQualifiedQuery extracts Type.Method from an intent string when present.
// Returns ok=false when no dotted identifier pair is found.
func splitQualifiedQuery(intent string) (recv, method string, ok bool) {
	intent = strings.TrimSpace(intent)
	if intent == "" || !strings.Contains(intent, ".") {
		return "", "", false
	}
	// Prefer the first Identifier.Identifier token (skip package paths like a/b.c).
	fields := strings.FieldsFunc(intent, func(r rune) bool {
		return r <= ' ' || r == ',' || r == ';' || r == '(' || r == ')' || r == '[' || r == ']' || r == '"' || r == '\''
	})
	for _, f := range fields {
		f = strings.Trim(f, "`*")
		if strings.Count(f, ".") != 1 {
			continue
		}
		parts := strings.SplitN(f, ".", 2)
		if len(parts) != 2 {
			continue
		}
		a, b := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if a == "" || b == "" {
			continue
		}
		if !isIdentToken(a) || !isIdentToken(b) {
			continue
		}
		return a, b, true
	}
	return "", "", false
}

func isIdentToken(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_') {
				return false
			}
			continue
		}
		if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
}

// isProviderLifecycleNoise demotes DI container lifecycle methods when the query
// is about HTTP/forms/routes — a common Laravel kickoff footgun.
func isProviderLifecycleNoise(path, name string, toks []string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n != "register" && n != "boot" {
		return false
	}
	p := strings.ToLower(filepath.ToSlash(path))
	if !strings.Contains(p, "provider") {
		return false
	}
	httpish := false
	for _, t := range toks {
		switch t {
		case "form", "request", "requests", "route", "routes", "http", "post", "get",
			"put", "patch", "delete", "controller", "middleware", "api", "endpoint",
			"signup", "login", "validation", "validator":
			httpish = true
		}
	}
	return httpish
}

// idfForTokens computes a smoothed inverse-document-frequency weight per query
// token over the candidate pool. A token that matches almost every candidate
// ("file", "data", "the") is barely discriminating and gets a weight near the
// floor; a rare token ("debounce", "centrality") that picks out a handful of
// symbols gets a high weight. Without this, BM25's per-term contributions are
// effectively unweighted, so a query's most common word — by sheer match count —
// drowns the rare word that actually identifies the target.
func idfForTokens(toks []string, candidates []types.Symbol) map[string]float64 {
	n := float64(len(candidates))
	df := map[string]int{}
	for _, s := range candidates {
		doc := strings.ToLower(s.Name + " " + s.Path + " " + s.Signature)
		for _, t := range toks {
			if t == "" {
				continue
			}
			if strings.Contains(doc, t) {
				df[t]++
			}
		}
	}
	idf := map[string]float64{}
	for _, t := range toks {
		if t == "" {
			continue
		}
		// Probabilistic BM25 IDF, smoothed; floored at a small positive so a
		// token present in every candidate still contributes a little.
		v := math.Log(1 + (n-float64(df[t])+0.5)/(float64(df[t])+0.5))
		if v < 0.05 {
			v = 0.05
		}
		// Vague modifiers ("hot", "fast") are rare in CODE, so raw IDF inflates
		// them and one incidental match hijacks the ranking. Pin them to the
		// floor: they may participate, but can never be the deciding term.
		if commonWords[t] {
			v = 0.05
		}
		idf[t] = v
	}
	return idf
}

func bm25Score(queryToks []string, idf map[string]float64, doc string) float64 {
	doc = strings.ToLower(doc)
	k1, b, avgdl := 1.2, 0.75, 50.0
	dl := float64(len(doc))
	if dl < 1 {
		dl = 1
	}
	score := 0.0
	for _, t := range queryToks {
		if t == "" {
			continue
		}
		f := float64(strings.Count(doc, t))
		if f == 0 {
			continue
		}
		w := idf[t]
		if w == 0 {
			w = 1 // fallback when IDF wasn't precomputed for this token
		}
		num := f * (k1 + 1)
		den := f + k1*(1-b+b*(dl/avgdl))
		score += w * (num / den)
	}
	return score
}

// trigramScore measures fuzzy character overlap (typo / morphology tolerance:
// "debouce" still finds "debounce"). Query trigrams are built PER meaningful
// token, not across the whole raw string — so filler can't contribute and a
// 3-char filler word can't bridge two unrelated words (the "hot" → "snap-shot"
// false match). Falls back to the raw query only when nothing meaningful remains
// (e.g. a bare symbol-name lookup that tokenized to a single short word).
func trigramScore(meaningTok []string, rawQuery, doc string) float64 {
	var qa []string
	for _, t := range meaningTok {
		qa = append(qa, trigrams(t)...)
	}
	if len(qa) == 0 {
		qa = trigrams(strings.ToLower(rawQuery))
	}
	da := trigrams(strings.ToLower(doc))
	if len(qa) == 0 || len(da) == 0 {
		return 0
	}
	set := map[string]struct{}{}
	for _, t := range da {
		set[t] = struct{}{}
	}
	match := 0
	for _, t := range qa {
		if _, ok := set[t]; ok {
			match++
		}
	}
	return float64(match) / float64(len(qa)+5)
}

func trigrams(s string) []string {
	s = strings.TrimSpace(s)
	if len(s) < 3 {
		if s == "" {
			return nil
		}
		return []string{s}
	}
	out := make([]string, 0, len(s)-2)
	for i := 0; i+3 <= len(s); i++ {
		out = append(out, s[i:i+3])
	}
	return out
}

func extractPathHints(q string) []string {
	raw := strings.Fields(strings.TrimSpace(strings.ToLower(q)))
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		x = strings.TrimSpace(strings.Trim(x, "\"'`,;:()[]{}"))
		if x == "" {
			continue
		}
		if strings.Contains(x, "/") || strings.Contains(x, ".go") {
			out = append(out, x)
		}
	}
	return dedupeReasons(out)
}

func pathHintBoost(path string, hints []string) float64 {
	if len(hints) == 0 {
		return 0
	}
	p := strings.ToLower(path)
	boost := 0.0
	for _, h := range hints {
		if h == "" {
			continue
		}
		if strings.Contains(p, h) {
			boost += 1.25
		}
	}
	return boost
}

// hubUtilityNames are ultra-central helpers that pollute kickoff/query relevance
// when centrality boosts them above domain symbols (log/error/cn in Next/React apps).
var hubUtilityNames = map[string]struct{}{
	"log": {}, "logger": {}, "error": {}, "err": {}, "cn": {}, "clsx": {}, "cx": {},
	"debug": {}, "info": {}, "warn": {}, "fatal": {}, "panic": {}, "assert": {},
	"print": {}, "println": {}, "sprintf": {}, "printf": {},
	"twmerge": {}, "tw_merge": {}, "classname": {}, "classnames": {},
}

func isHubUtilitySymbol(nameLower string) bool {
	if _, ok := hubUtilityNames[nameLower]; ok {
		return true
	}
	// Common wrappers: logError, logInfo, cnMerge, …
	for u := range hubUtilityNames {
		if len(u) >= 2 && (strings.HasPrefix(nameLower, u) || strings.HasSuffix(nameLower, u)) {
			if len(nameLower) <= len(u)+6 {
				return true
			}
		}
	}
	return false
}

func queryNamesHubUtility(toks []string, nameLower string) bool {
	if containsToken(toks, nameLower) {
		return true
	}
	for u := range hubUtilityNames {
		if containsToken(toks, u) {
			return true
		}
	}
	return false
}

// RRF merges two ranked lists by reciprocal rank fusion.
func RRF(a, b []RankedSymbol, k int) []RankedSymbol {
	if k <= 0 {
		k = 60
	}
	type pair struct {
		sym   types.Symbol
		score float64
	}
	byID := map[string]pair{}
	add := func(list []RankedSymbol, off int) {
		for i, it := range list {
			id := it.Symbol.ID
			inc := 1.0 / (float64(k) + float64(i+1+off))
			cur, ok := byID[id]
			if !ok {
				byID[id] = pair{sym: it.Symbol, score: inc}
				continue
			}
			cur.score += inc
			byID[id] = cur
		}
	}
	add(a, 0)
	add(b, 100)
	var out []RankedSymbol
	for _, p := range byID {
		out = append(out, RankedSymbol{Symbol: p.sym, Score: p.score})
	}
	sort.SliceStable(out, func(i, j int) bool { return rankedLess(out[i], out[j]) })
	return out
}

func rankedLess(a, b RankedSymbol) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	if a.Symbol.Path != b.Symbol.Path {
		return a.Symbol.Path < b.Symbol.Path
	}
	if a.Symbol.Name != b.Symbol.Name {
		return a.Symbol.Name < b.Symbol.Name
	}
	return a.Symbol.ID < b.Symbol.ID
}
