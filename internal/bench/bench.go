// Package bench provides a reproducible, local benchmark for codehelper's code
// intelligence (the Serena-equivalent surface) and its docs engine (the
// Context7-equivalent surface). It measures codehelper directly and frames the
// numbers against Serena's LSP model and Context7's hosted-docs model.
//
// Code-intelligence metric: "find the files that call symbol X". Ground truth
// is computed by a textual scan of the repository (the same signal a developer
// gets from grep); codehelper's answer comes from its resolved call graph. We
// report file-granularity precision/recall/F1 plus query latency. Textual
// ground truth includes comment/string mentions, so precision is a lower bound.
package bench

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/pkg/types"
)

// CallerMetric is the per-symbol outcome of a caller-lookup benchmark.
type CallerMetric struct {
	Symbol    string   `json:"symbol"`
	GTFiles   []string `json:"ground_truth_files"`
	GotFiles  []string `json:"codehelper_files"`
	TP        int      `json:"true_positives"`
	FP        int      `json:"false_positives"`
	FN        int      `json:"false_negatives"`
	Precision float64  `json:"precision"`
	Recall    float64  `json:"recall"`
	F1        float64  `json:"f1"`
	LatencyMs float64  `json:"latency_ms"`
}

// CodeIntelReport aggregates caller-lookup metrics. Precision/recall means are
// computed over "informative" symbols only — those with at least one caller in
// the repo. Recall is undefined for symbols with no callers (standard IR
// practice), so those are reported separately as NoCallerAgreement when
// codehelper also returns none (a correct true-negative).
type CodeIntelReport struct {
	RepoID            string  `json:"repo_id"`
	Symbols           int     `json:"symbols_tested"`
	Informative       int     `json:"informative_symbols"`
	NoCallerAgreement int     `json:"no_caller_agreement"`
	MeanPrecision     float64 `json:"mean_precision"`
	MeanRecall        float64 `json:"mean_recall"`
	MeanF1            float64 `json:"mean_f1"`
	P50LatencyMs      float64 `json:"p50_latency_ms"`
	P95LatencyMs      float64 `json:"p95_latency_ms"`
	CallEdges         int     `json:"concrete_call_edges"`
	SymrefEdges       int     `json:"unresolved_symref_edges"`
	// Of the unresolved symref edges, how many target a name that IS defined in
	// the repo (a genuine local recall gap) vs a name that is not (a call into
	// the stdlib or a dependency, which is unresolvable by design — codehelper
	// indexes the repo, not its imports). The headline symref count is dominated
	// by the external bucket, so InternalUnresolved is the number to optimize.
	InternalUnresolved int            `json:"internal_unresolved_edges"`
	ExternalUnresolved int            `json:"external_unresolved_edges"`
	Metrics            []CallerMetric `json:"metrics"`
}

// CodeIntel runs the caller-lookup benchmark for the given target symbol names
// against the indexed graph. repoRoot is scanned for textual ground truth.
func CodeIntel(ctx context.Context, st *graph.Store, repoID, repoRoot string, targets []string) (CodeIntelReport, error) {
	rep := CodeIntelReport{RepoID: repoID}
	var lats []float64
	for _, name := range targets {
		gt := groundTruthCallerFiles(repoRoot, name)
		start := time.Now()
		got, err := codehelperCallerFiles(ctx, st, repoID, name)
		lat := float64(time.Since(start).Microseconds()) / 1000.0
		if err != nil {
			return rep, err
		}
		m := scoreFiles(name, gt, got)
		m.LatencyMs = lat
		rep.Metrics = append(rep.Metrics, m)
		lats = append(lats, lat)
	}
	rep.Symbols = len(rep.Metrics)
	for _, m := range rep.Metrics {
		if len(m.GTFiles) == 0 {
			// No callers in the repo: recall is undefined. Count it as a correct
			// true-negative when codehelper also returns nothing.
			if len(m.GotFiles) == 0 {
				rep.NoCallerAgreement++
			}
			continue
		}
		rep.Informative++
		rep.MeanPrecision += m.Precision
		rep.MeanRecall += m.Recall
		rep.MeanF1 += m.F1
	}
	if rep.Informative > 0 {
		rep.MeanPrecision /= float64(rep.Informative)
		rep.MeanRecall /= float64(rep.Informative)
		rep.MeanF1 /= float64(rep.Informative)
	}
	rep.P50LatencyMs = percentile(lats, 50)
	rep.P95LatencyMs = percentile(lats, 95)
	rep.CallEdges, rep.SymrefEdges = edgeCounts(ctx, st, repoID)
	rep.InternalUnresolved, rep.ExternalUnresolved = unresolvedSplit(ctx, st, repoID)
	return rep, nil
}

// RetrievalReport holds ranking quality for the `query` retrieval tool, using
// the standard code-search metrics (Recall@k, MRR). Each target name is queried
// and we record the rank at which the matching symbol appears.
type RetrievalReport struct {
	Queries      int     `json:"queries"`
	Recall1      float64 `json:"recall_at_1"`
	Recall5      float64 `json:"recall_at_5"`
	Recall10     float64 `json:"recall_at_10"`
	MRR          float64 `json:"mrr"`
	P50LatencyMs float64 `json:"p50_latency_ms"`
	P95LatencyMs float64 `json:"p95_latency_ms"`
}

// Retrieval runs self-retrieval ranking: querying for a symbol's name should
// surface that symbol at or near rank 1. Uses BM25-only (no vectors) so it is
// reproducible without an embedding backend.
func Retrieval(ctx context.Context, st *graph.Store, repoID string, targets []string) (RetrievalReport, error) {
	rep := RetrievalReport{}
	var lats []float64
	var hit1, hit5, hit10 int
	var rrSum float64
	for _, name := range targets {
		start := time.Now()
		hits, err := retrieval.QueryHybridWithOptions(ctx, st, repoID, name, 10, retrieval.QueryOptions{
			QueryTokens: strings.Fields(strings.ToLower(name)),
		})
		lats = append(lats, float64(time.Since(start).Microseconds())/1000.0)
		if err != nil {
			return rep, err
		}
		rank := 0
		for i, h := range hits {
			if h.Symbol.Name == name {
				rank = i + 1
				break
			}
		}
		if rank == 0 {
			continue
		}
		rep.Queries++
		rrSum += 1.0 / float64(rank)
		if rank <= 1 {
			hit1++
		}
		if rank <= 5 {
			hit5++
		}
		if rank <= 10 {
			hit10++
		}
	}
	n := float64(len(targets))
	if n > 0 {
		rep.Recall1 = float64(hit1) / n
		rep.Recall5 = float64(hit5) / n
		rep.Recall10 = float64(hit10) / n
		rep.MRR = rrSum / n
	}
	rep.Queries = len(targets)
	rep.P50LatencyMs = percentile(lats, 50)
	rep.P95LatencyMs = percentile(lats, 95)
	return rep, nil
}

// ResolutionReport measures call-graph resolution quality that a textual /
// grep-based ground truth structurally cannot: how many calls to methods whose
// bare name is shared by multiple receiver types were nonetheless attributed to
// the correct type (via receiver-type inference) or to a promoted method (via
// struct embedding). These are exactly the cases where name-only resolution is
// ambiguous, so this is the honest credit for type-aware resolution.
type ResolutionReport struct {
	AmbiguousMethodNames int `json:"ambiguous_method_names"`   // method names defined on >1 receiver type
	AmbiguousMethodDefs  int `json:"ambiguous_method_defs"`    // total defs across those names
	TypeDisambiguated    int `json:"type_disambiguated_edges"` // calls resolved to a specific type (recv_type)
	EmbeddingResolved    int `json:"embedding_resolved_edges"` // calls resolved via promoted (embedded) methods
	// CriticalDisambiguations counts type-resolved calls that point at an
	// ambiguous-named method (defined on >1 type) — the calls a bare-name / grep
	// approach simply cannot resolve correctly. This is the metric that credits
	// type-aware resolution without the misleading denominator of external-type
	// calls (rows.Scan, resp.Body, …) we never had the symbols to resolve.
	CriticalDisambiguations int `json:"critical_disambiguations"`
}

// Resolution computes the resolution-quality report from the indexed graph.
func Resolution(ctx context.Context, st *graph.Store, repoID string) (ResolutionReport, error) {
	var rep ResolutionReport
	db := st.DB()

	// Method names shared by more than one receiver type (parent_id holds the
	// receiver type for Go methods).
	_ = db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(c),0) FROM (
			SELECT name, COUNT(DISTINCT parent_id) c FROM symbols
			WHERE repo_id=? AND kind='method' AND parent_id<>''
			GROUP BY name HAVING COUNT(DISTINCT parent_id) > 1
		)`, repoID).Scan(&rep.AmbiguousMethodNames, &rep.AmbiguousMethodDefs)

	// Edges resolved by the type-aware strategies (confidence stamps).
	_ = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM edges WHERE repo_id=? AND kind='calls' AND confidence=0.92`, repoID).Scan(&rep.TypeDisambiguated)
	_ = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM edges WHERE repo_id=? AND kind='calls' AND confidence=0.88`, repoID).Scan(&rep.EmbeddingResolved)

	// Type-resolved calls whose target method name is itself ambiguous — i.e.
	// only receiver-type/embedding resolution could have gotten them right.
	_ = db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM edges e
		JOIN symbols s ON s.id = e.dst_id AND s.repo_id = e.repo_id
		WHERE e.repo_id=? AND e.kind='calls' AND e.confidence IN (0.92, 0.88)
		AND s.name IN (
			SELECT name FROM symbols WHERE repo_id=? AND kind='method' AND parent_id<>''
			GROUP BY name HAVING COUNT(DISTINCT parent_id) > 1
		)`, repoID, repoID).Scan(&rep.CriticalDisambiguations)
	return rep, nil
}

// ComparativeReport quantifies the value of using codehelper's `context` tool to
// answer "who uses / what calls symbol X" versus the honest baseline an agent
// uses without it: grep for the name, then READ every file that matches. It
// reports tokens-per-answer and tool-calls-to-answer for both, plus the precision
// gap (grep matches include comments, the definition, and unrelated uses; the
// graph returns only real resolved callers).
type ComparativeReport struct {
	Tasks                 int     `json:"tasks"`
	MedianCodehelperToks  int     `json:"median_codehelper_tokens"`
	MedianBaselineToks    int     `json:"median_baseline_tokens"`
	TokenReductionPct     float64 `json:"token_reduction_pct"`
	MedianCodehelperCalls int     `json:"median_codehelper_tool_calls"`
	MedianBaselineCalls   int     `json:"median_baseline_tool_calls"`
	CallReductionPct      float64 `json:"tool_call_reduction_pct"`
	MeanGrepPrecision     float64 `json:"mean_grep_precision_vs_resolved"`
	// Locate task: "where is symbol X / what already does this" — one `query`
	// returning ranked compact hits vs grep+read of every file mentioning X.
	LocateMedianCodehelperToks int              `json:"locate_median_codehelper_tokens"`
	LocateMedianBaselineToks   int              `json:"locate_median_baseline_tokens"`
	LocateTokenReductionPct    float64          `json:"locate_token_reduction_pct"`
	Note                       string           `json:"note"`
	Caveat                     string           `json:"caveat"`
	Rows                       []ComparativeRow `json:"rows,omitempty"`
}

// ComparativeRow is one "who calls X" task measured both ways.
type ComparativeRow struct {
	Symbol           string `json:"symbol"`
	CodehelperTokens int    `json:"codehelper_tokens"`
	CodehelperCalls  int    `json:"codehelper_tool_calls"`
	BaselineTokens   int    `json:"baseline_tokens"`
	BaselineCalls    int    `json:"baseline_tool_calls"`
	BaselineFiles    int    `json:"baseline_files_read"`
}

// Comparative measures the with-tool vs without-tool cost for the "who calls X"
// task over the target set. Honest by construction: the baseline reads whole
// files (what a grep+read agent actually ingests), and we count the index build
// as a separate amortized cost in the printed note, not hidden in per-query tokens.
func Comparative(ctx context.Context, st *graph.Store, repoID, repoRoot string, targets []string) (ComparativeReport, error) {
	var rep ComparativeReport
	var chToks, baseToks, baseCalls []int
	var locChToks, locBaseToks []int
	var precisionSum float64
	var precisionN int
	for _, name := range targets {
		// codehelper: one `context` call returning compact resolved callers.
		bun, err := retrieval.BuildContext(ctx, st, repoID, name)
		if err != nil || bun == nil {
			continue
		}
		chTok := compactCallerTokens(bun.Callers)

		// baseline: grep the name, then read every matching file.
		files, bytes := baselineGrepReadCost(repoRoot, name)
		baseTok := bytes / 4
		baseCall := 1 + files // 1 grep + one read per matching file

		// Locate task: one `query` returning the top-5 ranked compact hits vs the
		// same grep+read baseline (the agent still has to read the files to locate X).
		if hits, qerr := retrieval.QueryHybrid(ctx, st, repoID, name, 5); qerr == nil {
			locChToks = append(locChToks, compactHitTokens(hits))
			locBaseToks = append(locBaseToks, baseTok)
		}

		// precision proxy: resolved callers / files that textually mention the name.
		if files > 0 {
			callerFiles := map[string]struct{}{}
			for _, c := range bun.Callers {
				callerFiles[c.Path] = struct{}{}
			}
			precisionSum += float64(len(callerFiles)) / float64(files)
			precisionN++
		}

		rep.Rows = append(rep.Rows, ComparativeRow{
			Symbol: name, CodehelperTokens: chTok, CodehelperCalls: 1,
			BaselineTokens: baseTok, BaselineCalls: baseCall, BaselineFiles: files,
		})
		chToks = append(chToks, chTok)
		baseToks = append(baseToks, baseTok)
		baseCalls = append(baseCalls, baseCall)
	}
	rep.Tasks = len(rep.Rows)
	rep.MedianCodehelperToks = medianInt(chToks)
	rep.MedianBaselineToks = medianInt(baseToks)
	rep.MedianCodehelperCalls = 1
	rep.MedianBaselineCalls = medianInt(baseCalls)
	if rep.MedianBaselineToks > 0 {
		rep.TokenReductionPct = 100 * (1 - float64(rep.MedianCodehelperToks)/float64(rep.MedianBaselineToks))
	}
	if rep.MedianBaselineCalls > 0 {
		rep.CallReductionPct = 100 * (1 - float64(rep.MedianCodehelperCalls)/float64(rep.MedianBaselineCalls))
	}
	if precisionN > 0 {
		rep.MeanGrepPrecision = precisionSum / float64(precisionN)
	}
	rep.LocateMedianCodehelperToks = medianInt(locChToks)
	rep.LocateMedianBaselineToks = medianInt(locBaseToks)
	if rep.LocateMedianBaselineToks > 0 {
		rep.LocateTokenReductionPct = 100 * (1 - float64(rep.LocateMedianCodehelperToks)/float64(rep.LocateMedianBaselineToks))
	}
	rep.Note = "Tasks: (1) 'who calls symbol X' — codehelper = one context call with resolved callers; (2) 'locate X / find similar' — codehelper = one query with ranked compact hits. Baseline for both = grep + read every matching file (what a grep+read agent ingests). Index build is a one-time amortized cost, not counted per query. Grep precision = resolved-caller-files / files-textually-mentioning-name."
	rep.Caveat = "Honest scope: these tasks ('who calls X', 'locate X') are where structured retrieval wins most. A tuned grep that returns inline matches (not whole files) narrows the token gap and can match raw F1 on some tasks; the durable, reproducible win here is tokens + tool-calls, at ~the same answer quality. Single-repo (this repo) — run on your own repos to confirm."
	return rep, nil
}

// compactHitTokens estimates the tokens of codehelper's compact query hits
// (name/kind/loc per hit — the real concise tool output).
func compactHitTokens(hits []retrieval.RankedSymbol) int {
	chars := len("hits: ")
	for _, h := range hits {
		chars += len(h.Symbol.Name) + len(h.Symbol.Path) + 16 // name + loc + kind + separators
	}
	return chars / 4
}

// compactCallerTokens estimates the tokens of codehelper's compact caller list
// (one `name path:line` line per resolved caller, ~the real tool output).
func compactCallerTokens(callers []types.Symbol) int {
	chars := len("callers: ")
	for _, c := range callers {
		chars += len(c.Name) + len(c.Path) + 8 // name + path + ":line" + separators
	}
	return chars / 4
}

// baselineGrepReadCost returns how many source files textually contain `name` and
// the total bytes a grep+read agent would ingest by opening them.
func baselineGrepReadCost(repoRoot, name string) (files, bytes int) {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
	_ = filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			base := d.Name()
			if base == ".git" || base == "node_modules" || base == "vendor" || base == ".codehelper" {
				return filepath.SkipDir
			}
			return nil
		}
		if !sourceExt[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if re.Match(b) {
			files++
			bytes += len(b)
		}
		return nil
	})
	return files, bytes
}

func medianInt(v []int) int {
	if len(v) == 0 {
		return 0
	}
	s := append([]int(nil), v...)
	sort.Ints(s)
	return s[len(s)/2]
}

// codehelperCallerFiles returns the distinct files that call symbol `name`
// according to codehelper's resolved call graph.
func codehelperCallerFiles(ctx context.Context, st *graph.Store, repoID, name string) ([]string, error) {
	syms, err := st.SymbolsByName(ctx, repoID, name, 50)
	if err != nil {
		return nil, err
	}
	files := map[string]struct{}{}
	for _, s := range syms {
		if s.Name != name {
			continue // exact-name targets only
		}
		in, err := st.EdgesTo(ctx, repoID, s.ID, "calls")
		if err != nil {
			return nil, err
		}
		for _, e := range in {
			if p := pathOfSymbolID(e.SourceID); p != "" {
				files[p] = struct{}{}
			}
		}
	}
	return sortedKeys(files), nil
}

// pathOfSymbolID extracts the file path from a `sym:repoID:relPath:line:name` id.
func pathOfSymbolID(id string) string {
	if !strings.HasPrefix(id, "sym:") {
		return ""
	}
	parts := strings.SplitN(id, ":", 5)
	if len(parts) < 5 {
		return ""
	}
	return parts[2]
}

var sourceExt = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".py": true, ".rs": true, ".java": true, ".cs": true, ".php": true,
}

// declKeywordRe matches a line that introduces a definition by keyword.
var declKeywordRe = regexp.MustCompile(`\b(func|def|function|fn|class)\b`)

// isDeclarationLine reports whether a line that textually contains `name(` is a
// declaration of that symbol rather than a call to it. A bare `\bname\s*\(`
// regex cannot tell the two apart, so this skips:
//   - keyword definitions: `func Foo(`, `def foo(`, `class Foo(`, …
//   - interface / abstract method declarations where the name starts the line
//     (after indentation) and the `(params)` is immediately followed by a
//     return type or `:` annotation — Go interface `Foo() Bar`, TS `foo(): Bar`.
//     These read as call sites to a textual scan but are signatures, so counting
//     them as callers understates accuracy.
//
// A genuine call is never the start of a line followed by a type token: bare
// statement calls (`Foo()`), assignments (`x := Foo()`), and chained/operator
// uses (`Foo().Bar()`, `Foo() + 1`) all fail the trailing `[A-Za-z_(:]` test.
func isDeclarationLine(line, name string) bool {
	if declKeywordRe.MatchString(line) && strings.Contains(line, name) {
		return true
	}
	declRe := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(name) + `\s*\([^)]*\)\s*[A-Za-z_(:]`)
	return declRe.MatchString(line)
}

// groundTruthCallerFiles scans repoRoot source files for textual call sites of
// `name` (\bname\s*\(), excluding lines that declare it (keyword definitions and
// interface/abstract method signatures). File-granularity.
func groundTruthCallerFiles(repoRoot, name string) []string {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*\(`)
	files := map[string]struct{}{}
	_ = filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			base := d.Name()
			if base == ".git" || base == "node_modules" || base == "vendor" || base == ".codehelper" {
				return filepath.SkipDir
			}
			return nil
		}
		if !sourceExt[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(repoRoot, path)
		rel = filepath.ToSlash(rel)
		for _, line := range strings.Split(string(b), "\n") {
			if !re.MatchString(line) {
				continue
			}
			// Skip declaration sites (keyword defs and interface/abstract signatures).
			if isDeclarationLine(line, name) {
				continue
			}
			files[rel] = struct{}{}
			break
		}
		return nil
	})
	return sortedKeys(files)
}

func scoreFiles(name string, gt, got []string) CallerMetric {
	gtSet := toSet(gt)
	gotSet := toSet(got)
	m := CallerMetric{Symbol: name, GTFiles: gt, GotFiles: got}
	for f := range gotSet {
		if _, ok := gtSet[f]; ok {
			m.TP++
		} else {
			m.FP++
		}
	}
	for f := range gtSet {
		if _, ok := gotSet[f]; !ok {
			m.FN++
		}
	}
	if m.TP+m.FP > 0 {
		m.Precision = float64(m.TP) / float64(m.TP+m.FP)
	}
	if m.TP+m.FN > 0 {
		m.Recall = float64(m.TP) / float64(m.TP+m.FN)
	}
	if m.Precision+m.Recall > 0 {
		m.F1 = 2 * m.Precision * m.Recall / (m.Precision + m.Recall)
	}
	return m
}

// SelectTargets picks function/method symbols with reasonably unique names to
// use as benchmark targets (longer names reduce textual ground-truth noise).
func SelectTargets(ctx context.Context, st *graph.Store, repoID string, max int) ([]string, error) {
	rows, err := st.DB().QueryContext(ctx,
		`SELECT name, COUNT(*) c FROM symbols WHERE repo_id=? AND kind IN ('function','method') AND length(name)>=6
		 GROUP BY name HAVING c=1 ORDER BY name LIMIT ?`, repoID, max)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		var c int
		if err := rows.Scan(&n, &c); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func edgeCounts(ctx context.Context, st *graph.Store, repoID string) (calls, symref int) {
	_ = st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM edges WHERE repo_id=? AND kind='calls' AND dst_id LIKE 'sym:%'`, repoID).Scan(&calls)
	_ = st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM edges WHERE repo_id=? AND kind='calls' AND dst_id LIKE 'symref:%'`, repoID).Scan(&symref)
	return
}

// unresolvedSplit partitions unresolved `calls` symref edges into those whose
// target name is defined somewhere in the repo (internal — a real recall gap)
// and those whose name is not (external — calls into the stdlib or a dependency,
// which the local index can never resolve). For a type-qualified target
// `Type.Method`, the method's bare name is what is matched against repo symbols.
func unresolvedSplit(ctx context.Context, st *graph.Store, repoID string) (internal, external int) {
	names := map[string]bool{}
	rows, err := st.DB().QueryContext(ctx, `SELECT DISTINCT name FROM symbols WHERE repo_id=?`, repoID)
	if err != nil {
		return 0, 0
	}
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil && n != "" {
			names[n] = true
		}
	}
	rows.Close()

	erows, err := st.DB().QueryContext(ctx,
		`SELECT dst_id FROM edges WHERE repo_id=? AND kind='calls' AND dst_id LIKE 'symref:%'`, repoID)
	if err != nil {
		return 0, 0
	}
	defer erows.Close()
	for erows.Next() {
		var dst string
		if erows.Scan(&dst) != nil {
			continue
		}
		name := dst[strings.LastIndexByte(dst, ':')+1:]
		if i := strings.LastIndexByte(name, '.'); i >= 0 { // Type.Method -> Method
			name = name[i+1:]
		}
		if names[name] {
			internal++
		} else {
			external++
		}
	}
	return internal, external
}

func toSet(in []string) map[string]struct{} {
	m := make(map[string]struct{}, len(in))
	for _, s := range in {
		m[s] = struct{}{}
	}
	return m
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func percentile(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	idx := int(p / 100 * float64(len(s)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(s) {
		idx = len(s) - 1
	}
	return s[idx]
}
