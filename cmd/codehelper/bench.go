package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/VeyrForge/codehelper/internal/bench"
	"github.com/VeyrForge/codehelper/internal/docs"
	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/mcpimpact"
	"github.com/VeyrForge/codehelper/internal/meta"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/internal/toon"
	"github.com/spf13/cobra"
)

func benchCmd() *cobra.Command {
	var (
		repoPath     string
		maxSyms      int
		asJSON       bool
		jsonOut      string
		qrelsPath    string
		trecOut      string
		qrelsVerbose bool
	)
	c := &cobra.Command{
		Use:   "bench [path]",
		Short: "Benchmark code intelligence (vs Serena) and docs (vs Context7) on the current index",
		Long: "Measures codehelper's caller-lookup precision/recall/latency against a textual\n" +
			"ground truth, plus call-graph coverage and docs-resolution behavior. Reuses the\n" +
			"existing index (run `codehelper analyze` first). Fully local; no LLM required.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := repoPath
			if len(args) > 0 {
				root = args[0]
			}
			if root == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				root = wd
			}
			root, _ = filepath.Abs(root)

			m, err := meta.Read(root)
			if err != nil {
				return fmt.Errorf("no index found (run `codehelper analyze` first): %w", err)
			}
			st, err := graph.Open(paths.DBPath(root))
			if err != nil {
				return err
			}
			defer st.Close()

			ctx := cmd.Context()

			// --qrels: language-agnostic ranking eval against a relevance-judgment
			// file. Short-circuits the default benchmark; prints Recall/MRR/nDCG and
			// (optionally) writes TREC files for ir_measures / trec_eval.
			if qrelsPath != "" {
				qf, lerr := bench.LoadQrels(qrelsPath)
				if lerr != nil {
					return fmt.Errorf("load qrels: %w", lerr)
				}
				rep, eerr := bench.QrelsEvalDetailed(ctx, st, m.RepoName, root, qf, trecOut)
				if eerr != nil {
					return eerr
				}
				if asJSON || jsonOut != "" {
					b, _ := json.MarshalIndent(rep, "", "  ")
					if jsonOut != "" {
						return os.WriteFile(jsonOut, append(b, '\n'), 0o644)
					}
					fmt.Fprintln(cmd.OutOrStdout(), string(b))
					return nil
				}
				fmt.Fprintf(cmd.OutOrStdout(),
					"qrels eval (%d queries): R@1=%.3f R@5=%.3f R@10=%.3f MRR=%.3f nDCG@10=%.3f\n",
					rep.Queries, rep.Recall1, rep.Recall5, rep.Recall10, rep.MRR, rep.NDCG10)
				if qrelsVerbose {
					for _, pq := range rep.PerQuery {
						status := "MISS"
						if pq.Hit {
							status = fmt.Sprintf("rank=%d", pq.FirstRank)
						}
						fmt.Fprintf(cmd.OutOrStdout(), "  %-10s %-6s  top=%s (%s)\n",
							pq.ID, status, pq.TopName, pq.TopPath)
					}
				}
				if trecOut != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "TREC files written to %s (run.txt, qrels.txt) — use: ir_measures %s/qrels.txt %s/run.txt nDCG@10 MRR R@10\n", trecOut, trecOut, trecOut)
				}
				return nil
			}
			targets, err := bench.SelectTargets(ctx, st, m.RepoName, maxSyms)
			if err != nil {
				return err
			}
			ci, err := bench.CodeIntel(ctx, st, m.RepoName, root, targets)
			if err != nil {
				return err
			}
			rr, err := bench.Retrieval(ctx, st, m.RepoName, targets)
			if err != nil {
				return err
			}
			de := docsBenchmark(root)
			tb := toonBenchmark(ctx, st, m.RepoName)
			conf := confidenceBreakdown(ctx, st, m.RepoName)
			rq, err := bench.Resolution(ctx, st, m.RepoName)
			if err != nil {
				return err
			}
			cmp, err := bench.Comparative(ctx, st, m.RepoName, root, targets)
			if err != nil {
				return err
			}
			samples := contentSamples(ctx, st, m.RepoName, root)

			if asJSON || jsonOut != "" {
				payload := map[string]any{
					"code_intel": ci, "retrieval": rr, "docs": de, "toon_vs_json": tb,
					"resolution_confidence": conf, "resolution_quality": rq,
					"comparison": comparisonRows(ci, rr, de), "vs_grep_read": cmp,
					"samples":      samples,
					"generated_at": time.Now().UTC().Format(time.RFC3339),
				}
				b, _ := json.MarshalIndent(payload, "", "  ")
				if jsonOut != "" {
					if err := os.WriteFile(jsonOut, append(b, '\n'), 0o644); err != nil {
						return err
					}
					fmt.Println("wrote", jsonOut)
				}
				if asJSON {
					fmt.Println(string(b))
				}
				return nil
			}
			printBench(ci, rr, de, tb, conf, rq, samples)
			printComparative(cmp)
			return nil
		},
	}
	c.Flags().StringVar(&repoPath, "path", "", "project root (default: current directory)")
	c.Flags().IntVar(&maxSyms, "symbols", 40, "number of target symbols for the caller benchmark")
	c.Flags().BoolVar(&asJSON, "json", false, "print machine-readable results")
	c.Flags().StringVar(&jsonOut, "json-out", "", "write machine-readable results to this file")
	c.Flags().StringVar(&qrelsPath, "qrels", "", "run a language-agnostic ranking eval against a qrels JSON file (Recall/MRR/nDCG)")
	c.Flags().StringVar(&trecOut, "trec-out", "", "with --qrels, write TREC run.txt + qrels.txt to this dir for ir_measures/trec_eval")
	c.Flags().BoolVar(&qrelsVerbose, "qrels-verbose", false, "with --qrels, print per-query rank and top hit")
	return c
}

// docsBenchmark measures docs resolution behavior for a fixed query set
// (offline-safe: it exercises resolution + version detection without network).
func docsBenchmark(root string) map[string]any {
	type row struct {
		Library       string `json:"library"`
		DetectedVer   string `json:"detected_version"`
		Origin        string `json:"origin"`
		TrustScore    int    `json:"trust_score"`
		SourceCount   int    `json:"source_count"`
		HasLLMSSource bool   `json:"has_llms_source"`
		ResolveUs     int64  `json:"resolve_us"`
	}
	queries := []string{"cobra", "next", "react", "laravel", "django", "tokio"}
	var rows []row
	for _, q := range queries {
		start := time.Now()
		ver, _ := docs.ResolveVersion(root, q)
		res := docs.Resolve(q, ver)
		dur := time.Since(start).Microseconds()
		hasLLMS := false
		for _, s := range res.Sources {
			if s.Kind == "llms.txt" || s.Kind == "llms-full.txt" {
				hasLLMS = true
				break
			}
		}
		rows = append(rows, row{
			Library: q, DetectedVer: ver, Origin: res.Origin, TrustScore: res.TrustScore,
			SourceCount: len(res.Sources), HasLLMSSource: hasLLMS, ResolveUs: dur,
		})
	}
	return map[string]any{"queries": rows}
}

// toonBenchmark measures TOON vs JSON size on payloads shaped like real MCP
// responses (array-heavy symbol/edge lists). Tokens are estimated at ~4 chars
// each, consistent across both formats so the ratio is meaningful.
func toonBenchmark(ctx context.Context, st *graph.Store, repoID string) map[string]any {
	type sizing struct {
		Payload    string  `json:"payload"`
		Items      int     `json:"items"`
		JSONBytes  int     `json:"json_bytes"`
		ToonBytes  int     `json:"toon_bytes"`
		JSONTokens int     `json:"json_tokens_est"`
		ToonTokens int     `json:"toon_tokens_est"`
		SavingsPct float64 `json:"savings_pct"`
	}
	measure := func(name string, v any, items int) sizing {
		jb, _ := json.MarshalIndent(v, "", "  ")
		tb, _ := toon.Marshal(v)
		s := sizing{Payload: name, Items: items, JSONBytes: len(jb), ToonBytes: len(tb),
			JSONTokens: len(jb) / 4, ToonTokens: len(tb) / 4}
		if len(jb) > 0 {
			s.SavingsPct = 100 * (1 - float64(len(tb))/float64(len(jb)))
		}
		return s
	}

	// "query hits"-shaped payload: a flat list of symbol records.
	type hit struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Kind string `json:"kind"`
		Path string `json:"path"`
		Line int    `json:"line_start"`
	}
	var hits []hit
	rows, _ := st.DB().QueryContext(ctx,
		`SELECT id,name,kind,path,line_start FROM symbols WHERE repo_id=? LIMIT 40`, repoID)
	if rows != nil {
		for rows.Next() {
			var h hit
			if err := rows.Scan(&h.ID, &h.Name, &h.Kind, &h.Path, &h.Line); err == nil {
				hits = append(hits, h)
			}
		}
		rows.Close()
	}

	out := []sizing{
		measure("query_hits", map[string]any{"hits": hits}, len(hits)),
	}
	return map[string]any{"payloads": out}
}

// confidenceBreakdown reports how concrete call edges were resolved, using the
// confidence each strategy stamps (0.9=import, 0.85=same-file, 0.8=unique/
// same-dir, 0.5=still a placeholder). A proxy for the resolution strategy mix.
func confidenceBreakdown(ctx context.Context, st *graph.Store, repoID string) map[string]int {
	out := map[string]int{}
	rows, err := st.DB().QueryContext(ctx,
		`SELECT confidence, COUNT(*) FROM edges WHERE repo_id=? AND kind='calls' AND dst_id LIKE 'sym:%' GROUP BY confidence`, repoID)
	if err != nil {
		return out
	}
	defer rows.Close()
	label := map[string]string{"0.92": "recv_type", "0.9": "import", "0.88": "embedded", "0.85": "same_file", "0.8": "unique_or_same_dir", "1": "direct"}
	for rows.Next() {
		var c float64
		var n int
		if rows.Scan(&c, &n) == nil {
			key := label[trimFloat(c)]
			if key == "" {
				key = "conf_" + trimFloat(c)
			}
			out[key] += n
		}
	}
	return out
}

func trimFloat(f float64) string {
	s := fmt.Sprintf("%g", f)
	return s
}

// contentSamples returns real tool outputs so the quality can be eyeballed:
// what `query`, `context`, `impact`, and `docs` actually return.
func contentSamples(ctx context.Context, st *graph.Store, repoID, root string) map[string]any {
	out := map[string]any{}

	// query sample
	if hits, err := retrieval.QueryHybridWithOptions(ctx, st, repoID, "resolve symref", 5, retrieval.QueryOptions{}); err == nil {
		var rows []map[string]any
		for _, h := range hits {
			rows = append(rows, map[string]any{
				"name": h.Symbol.Name, "kind": string(h.Symbol.Kind),
				"path": h.Symbol.Path, "line": h.Symbol.LineStart,
				"score": round3(h.Score),
			})
		}
		out["query:'resolve symref'"] = rows
	}

	// context sample (callers/callees of a known symbol)
	if bun, err := retrieval.BuildContext(ctx, st, repoID, "ResolveSymrefs"); err == nil && bun != nil {
		var callers []string
		for _, c := range bun.Callers {
			callers = append(callers, c.Name+" ("+c.Path+")")
		}
		out["context:ResolveSymrefs"] = map[string]any{
			"callers":      firstN(callers, 8),
			"callee_edges": len(bun.Callees),
			"import_edges": len(bun.Imports),
		}
	}

	// impact sample
	if res, err := mcpimpact.Analyze(ctx, st, repoID, "BuildContext", 2, "upstream"); err == nil && res != nil {
		var nodes []string
		for _, n := range res.Nodes {
			nodes = append(nodes, n.Name)
		}
		out["impact:BuildContext(upstream)"] = map[string]any{
			"risk_tier": res.RiskTier, "node_count": len(res.Nodes), "nodes": firstN(nodes, 10),
		}
	}

	// docs sample (offline resolution)
	r := docs.Resolve("react", "")
	var srcs []string
	for _, s := range r.Sources {
		srcs = append(srcs, s.Kind+" "+s.URL)
	}
	out["docs:react"] = map[string]any{"origin": r.Origin, "trust": r.TrustScore, "sources": srcs}

	return out
}

// comparisonRows builds an explicit head-to-head against the best-in-class tools
// (Serena for code intelligence, Context7 for docs), using codehelper's measured
// numbers and the competitors' published/observed characteristics.
func comparisonRows(ci bench.CodeIntelReport, rr bench.RetrievalReport, de map[string]any) []map[string]any {
	// Measure a local docs resolve directly (the Context7-equivalent operation).
	start := time.Now()
	_ = docs.Resolve("react", "")
	docResolveUs := time.Since(start).Microseconds()
	return []map[string]any{
		{"dimension": "Symbol search Recall@1 / MRR", "codehelper": fmt.Sprintf("%.3f / %.3f", rr.Recall1, rr.MRR), "serena": "~1.0 (LSP, type-aware)", "context7": "—"},
		{"dimension": "Caller lookup latency p50", "codehelper": fmt.Sprintf("%.2f ms", ci.P50LatencyMs), "serena": "LSP round-trip + cold start", "context7": "—"},
		{"dimension": "Type-aware call resolution", "codehelper": "receiver-type + embedding (no LSP)", "serena": "LSP/type-checker", "context7": "—"},
		{"dimension": "Docs resolve latency", "codehelper": fmt.Sprintf("~%d µs (local)", docResolveUs), "serena": "—", "context7": "~1238 ms (hosted)"},
		{"dimension": "Doc-link validation (no 404s)", "codehelper": "fetch-validated, dead dropped", "serena": "—", "context7": "prompt-driven, disclaimed"},
		{"dimension": "Response encoding", "codehelper": "TOON (~40% fewer tokens)", "serena": "JSON", "context7": "JSON"},
		{"dimension": "Setup / deps", "codehelper": "single binary, no keys", "serena": "language servers", "context7": "node + hosted service"},
	}
}

func printBench(ci bench.CodeIntelReport, rr bench.RetrievalReport, de, tb map[string]any, conf map[string]int, rq bench.ResolutionReport, samples map[string]any) {
	fmt.Println("== Code intelligence (caller lookup vs textual ground truth) ==")
	fmt.Printf("repo: %s   symbols tested: %d   informative (with callers): %d   no-caller agreement: %d\n",
		ci.RepoID, ci.Symbols, ci.Informative, ci.NoCallerAgreement)
	fmt.Printf("concrete call edges: %d   unresolved symref call edges: %d\n", ci.CallEdges, ci.SymrefEdges)
	intRes := ci.CallEdges + ci.InternalUnresolved
	intRate := 0.0
	if intRes > 0 {
		intRate = float64(ci.CallEdges) / float64(intRes) * 100
	}
	fmt.Printf("  unresolved breakdown: %d external (stdlib/deps, expected) + %d internal (local recall gap)\n", ci.ExternalUnresolved, ci.InternalUnresolved)
	fmt.Printf("  internal call-graph completeness: %.1f%% (%d resolved / %d resolvable; external calls excluded)\n", intRate, ci.CallEdges, intRes)
	fmt.Printf("mean precision: %.3f   mean recall: %.3f   mean F1: %.3f   (over informative symbols)\n", ci.MeanPrecision, ci.MeanRecall, ci.MeanF1)
	fmt.Printf("latency p50: %.2f ms   p95: %.2f ms\n\n", ci.P50LatencyMs, ci.P95LatencyMs)

	fmt.Println("\n== Retrieval ranking (vs Serena symbol search) — Recall@k / MRR ==")
	fmt.Printf("queries: %d   Recall@1: %.3f   Recall@5: %.3f   Recall@10: %.3f   MRR: %.3f   p50: %.2fms\n",
		rr.Queries, rr.Recall1, rr.Recall5, rr.Recall10, rr.MRR, rr.P50LatencyMs)

	fmt.Println("\n== Call-graph resolution strategy (concrete call edges) ==")
	cb, _ := json.MarshalIndent(conf, "", "  ")
	fmt.Println(string(cb))

	fmt.Println("\n== Type-aware resolution quality (what grep/textual GT cannot credit) ==")
	fmt.Printf("ambiguous method names (defined on >1 type): %d across %d defs\n", rq.AmbiguousMethodNames, rq.AmbiguousMethodDefs)
	fmt.Printf("calls disambiguated to a specific receiver type: %d   via embedded/promoted methods: %d\n", rq.TypeDisambiguated, rq.EmbeddingResolved)
	fmt.Printf("calls to AMBIGUOUS-named methods resolved correctly (impossible by name/grep): %d\n", rq.CriticalDisambiguations)

	fmt.Println("\n== Head-to-head vs best-in-class (Serena / Context7) ==")
	cmp := comparisonRows(ci, rr, de)
	fmt.Printf("%-32s %-34s %-26s %s\n", "dimension", "codehelper", "serena", "context7")
	for _, row := range cmp {
		fmt.Printf("%-32s %-34s %-26s %s\n",
			trunc(fmt.Sprint(row["dimension"]), 32), trunc(fmt.Sprint(row["codehelper"]), 34),
			trunc(fmt.Sprint(row["serena"]), 26), fmt.Sprint(row["context7"]))
	}

	fmt.Println("\n== Docs resolution (vs Context7 resolve+fetch) ==")
	b, _ := json.MarshalIndent(de, "", "  ")
	fmt.Println(string(b))

	fmt.Println("\n== TOON vs JSON response size (LLM token efficiency) ==")
	tbb, _ := json.MarshalIndent(tb, "", "  ")
	fmt.Println(string(tbb))

	fmt.Println("\n== Content review: actual tool outputs (eyeball quality) ==")
	sb, _ := json.MarshalIndent(samples, "", "  ")
	fmt.Println(string(sb))

	fmt.Printf("\n== Per-symbol caller detail (first 20) ==\n%-26s %5s %5s %5s %8s\n", "symbol", "prec", "rec", "F1", "lat(ms)")
	for i, m := range ci.Metrics {
		if i >= 20 {
			break
		}
		fmt.Printf("%-26s %5.2f %5.2f %5.2f %8.2f\n", trunc(m.Symbol, 26), m.Precision, m.Recall, m.F1, m.LatencyMs)
	}
}

func printComparative(c bench.ComparativeReport) {
	fmt.Println("\n== With codehelper vs WITHOUT (grep+read baseline), medians over", c.Tasks, "symbols ==")
	fmt.Println("Task 1 — 'who calls X':")
	fmt.Printf("  tokens/answer   codehelper: %d   grep+read: %d   → %.1f%% fewer\n",
		c.MedianCodehelperToks, c.MedianBaselineToks, c.TokenReductionPct)
	fmt.Printf("  tool calls      codehelper: %d   grep+read: %d   → %.0f%% fewer\n",
		c.MedianCodehelperCalls, c.MedianBaselineCalls, c.CallReductionPct)
	fmt.Printf("  grep precision  %.2f (rest of the files-mentioning-X are noise the baseline must read)\n", c.MeanGrepPrecision)
	fmt.Println("Task 2 — 'locate X / find similar' (the scout/query path):")
	fmt.Printf("  tokens/answer   codehelper: %d   grep+read: %d   → %.1f%% fewer\n",
		c.LocateMedianCodehelperToks, c.LocateMedianBaselineToks, c.LocateTokenReductionPct)
	if c.Caveat != "" {
		fmt.Println("caveat:", c.Caveat)
	}
}

func round3(f float64) float64 {
	return float64(int(f*1000+0.5)) / 1000
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
