// Package eval is a deterministic regression harness for retrieval and
// LLM-guidance prompts. It runs offline against an indexed repository and
// prints a pass/fail summary plus a non-zero exit code on regression so
// CI pipelines can gate merges.
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/prompts"
	"github.com/VeyrForge/codehelper/internal/retrieval"
)

// QueryCase is one retrieval expectation: a query string and the file path
// substrings that MUST appear in the top-K results.
type QueryCase struct {
	Query              string     `json:"query"`
	MustContainPath    []string   `json:"must_contain_path,omitempty"`
	MustContainAnyPath [][]string `json:"must_contain_any_path,omitempty"`
	TopK               int        `json:"top_k,omitempty"`
}

// PromptCase is an intake-prompt expectation: a phrase that must appear.
type PromptCase struct {
	PromptID       string   `json:"prompt_id"`
	MustContain    []string `json:"must_contain"`
	MustNotContain []string `json:"must_not_contain,omitempty"`
}

// Suite is a set of cases run together.
type Suite struct {
	Queries []QueryCase  `json:"queries,omitempty"`
	Prompts []PromptCase `json:"prompts,omitempty"`
}

// Result of running a Suite.
type Result struct {
	Total  int          `json:"total"`
	Passed int          `json:"passed"`
	Failed int          `json:"failed"`
	Cases  []Case       `json:"cases"`
	Rank   *RankMetrics `json:"rank,omitempty"`
}

// RankMetrics are rank-aware retrieval-quality aggregates over the query cases.
// The pass/fail check only asks whether a target appears anywhere in top-K; these
// measure whether it ranks HIGH — so a target sliding from #1 to #100 (a ranking
// regression the loose check misses) shows up as a drop in MRR / Recall@1.
type RankMetrics struct {
	Queries    int     `json:"queries"`      // query cases scored
	Found      int     `json:"found"`        // target appeared at any rank
	RecallAt1  float64 `json:"recall_at_1"`  // fraction with a target at rank 1
	RecallAt5  float64 `json:"recall_at_5"`  // …within the top 5
	RecallAt10 float64 `json:"recall_at_10"` // …within the top 10
	MRR        float64 `json:"mrr"`          // mean reciprocal rank of first relevant hit
}

// Case describes one execution outcome.
type Case struct {
	Name   string   `json:"name"`
	Pass   bool     `json:"pass"`
	Detail string   `json:"detail,omitempty"`
	Hits   []string `json:"hits,omitempty"`
	Rank   int      `json:"rank,omitempty"` // 1-based rank of the first relevant hit, 0 = not found
}

// Default returns the bundled smoke suite that exercises both retrieval and
// the intake prompt content. It is intentionally tiny so CI completes fast.
func Default() Suite {
	return Suite{
		Queries: []QueryCase{
			{Query: "internal/freshness/freshness.go stale_reason indexed_commit", MustContainPath: []string{"internal/freshness/freshness.go"}, TopK: 120},
			{
				Query: "BuildContextPack dependency_distance_1",
				MustContainAnyPath: [][]string{
					{"internal/retrieval/context.go"},
					{"internal/mcpsvc/register.go"},
				},
				TopK: 120,
			},
			{
				Query: "mcpimpact Analyze blast radius",
				MustContainAnyPath: [][]string{
					{"internal/mcpimpact/impact.go"},
					{"internal/mcpsvc/register.go"},
				},
				TopK: 80,
			},
			{
				Query: "ParseTypeScript nextjs route handler",
				MustContainAnyPath: [][]string{
					{"internal/parser/ts.go"},
				},
				TopK: 120,
			},
			{
				Query: "ParsePHP laravel wordpress route hook",
				MustContainAnyPath: [][]string{
					{"internal/parser/php.go"},
				},
				TopK: 120,
			},
			{
				Query: "ParsePython django fastapi decorator route",
				MustContainAnyPath: [][]string{
					{"internal/parser/python.go"},
				},
				TopK: 120,
			},
		},
		Prompts: []PromptCase{
			{PromptID: "intake_project_brief", MustContain: []string{"PRIMARY OUTCOME", "ASSUMPTION:", "[UNCERTAIN]"}},
			{PromptID: "planning_contract", MustContain: []string{"VERIFICATION", "ROLLBACK", "FAILURE CONDITIONS", "[UNCERTAIN]"}},
			{PromptID: "agent_guardrails", MustContain: []string{"argv", "graph tools", "fail closed", "[UNCERTAIN]"}, MustNotContain: []string{"console.log", "FIXME"}},
		},
	}
}

// PromptText returns the LLM-facing text for a registered prompt id.
func PromptText(id string) (string, error) {
	switch id {
	case "intake_project_brief":
		return prompts.IntakeProjectBrief, nil
	case "planning_contract":
		return prompts.PlanningContract, nil
	case "agent_guardrails":
		return prompts.AgentGuardrails, nil
	case "detect_impact":
		return prompts.DetectImpact, nil
	case "generate_map":
		return prompts.GenerateMap, nil
	default:
		return "", fmt.Errorf("unknown prompt id: %s", id)
	}
}

// Run executes a Suite against the index at indexRoot and writes a human
// summary to w; returns the structured Result. If suite has no Queries,
// indexRoot may be empty (only Prompts are evaluated).
func Run(ctx context.Context, indexRoot, repoID string, suite Suite, w io.Writer) (*Result, error) {
	res := &Result{}
	var st *graph.Store
	if len(suite.Queries) > 0 {
		var err error
		st, err = graph.Open(paths.DBPath(indexRoot))
		if err != nil {
			return nil, err
		}
		defer st.Close()
	}

	var ranks []int
	for _, qc := range suite.Queries {
		topK := qc.TopK
		if topK <= 0 {
			topK = 10
		}
		hits, qerr := retrieval.QueryHybridWithOptions(ctx, st, repoID, qc.Query, topK, retrieval.QueryOptions{
			QueryTokens: strings.Fields(strings.ToLower(qc.Query)),
			RepoRoot:    indexRoot,
		})
		c := Case{Name: "query: " + qc.Query}
		if qerr != nil {
			c.Pass = false
			c.Detail = "error: " + qerr.Error()
		} else {
			paths := uniqPaths(hits)
			c.Hits = paths
			if queryCasePasses(paths, qc) {
				c.Pass = true
			} else {
				c.Detail = queryCaseFailureDetail(paths, qc)
			}
			// Rank uses hit ORDER (uniqPaths sorts, which would erase rank).
			c.Rank = firstRelevantRank(orderedUniqPaths(hits), expectedNeedles(qc))
			ranks = append(ranks, c.Rank)
		}
		appendCase(res, c)
	}
	if len(ranks) > 0 {
		m := rankMetrics(ranks)
		res.Rank = &m
	}

	for _, pc := range suite.Prompts {
		text, err := PromptText(pc.PromptID)
		c := Case{Name: "prompt: " + pc.PromptID}
		if err != nil {
			c.Detail = err.Error()
			appendCase(res, c)
			continue
		}
		var miss []string
		for _, s := range pc.MustContain {
			if !strings.Contains(text, s) {
				miss = append(miss, s)
			}
		}
		var bad []string
		for _, s := range pc.MustNotContain {
			if strings.Contains(text, s) {
				bad = append(bad, s)
			}
		}
		if len(miss) == 0 && len(bad) == 0 {
			c.Pass = true
		} else {
			parts := []string{}
			if len(miss) > 0 {
				parts = append(parts, "missing: "+strings.Join(miss, ", "))
			}
			if len(bad) > 0 {
				parts = append(parts, "forbidden present: "+strings.Join(bad, ", "))
			}
			c.Detail = strings.Join(parts, "; ")
		}
		appendCase(res, c)
	}

	if w != nil {
		writeSummary(w, res)
	}
	return res, nil
}

func appendCase(res *Result, c Case) {
	res.Total++
	if c.Pass {
		res.Passed++
	} else {
		res.Failed++
	}
	res.Cases = append(res.Cases, c)
}

func uniqPaths(hits []retrieval.RankedSymbol) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, h := range hits {
		if _, ok := seen[h.Symbol.Path]; ok {
			continue
		}
		seen[h.Symbol.Path] = struct{}{}
		out = append(out, h.Symbol.Path)
	}
	sort.Strings(out)
	return out
}

// orderedUniqPaths is uniqPaths WITHOUT the sort — first-occurrence (rank) order
// preserved, which the rank metrics need.
func orderedUniqPaths(hits []retrieval.RankedSymbol) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, h := range hits {
		if _, ok := seen[h.Symbol.Path]; ok {
			continue
		}
		seen[h.Symbol.Path] = struct{}{}
		out = append(out, h.Symbol.Path)
	}
	return out
}

// expectedNeedles flattens every path-substring a case considers relevant (the
// required set plus every any-group), so the rank metric measures how quickly the
// FIRST relevant path surfaces regardless of AND/OR case shape.
func expectedNeedles(qc QueryCase) []string {
	var out []string
	out = append(out, qc.MustContainPath...)
	for _, g := range qc.MustContainAnyPath {
		out = append(out, g...)
	}
	return out
}

// firstRelevantRank returns the 1-based position of the first path (in hit order)
// that contains any expected needle, or 0 if none match.
func firstRelevantRank(orderedPaths, needles []string) int {
	for i, p := range orderedPaths {
		for _, n := range needles {
			if n != "" && strings.Contains(p, n) {
				return i + 1
			}
		}
	}
	return 0
}

// rankMetrics aggregates per-query first-relevant ranks (0 = not found) into
// Recall@K and MRR. Pure so it can be unit-tested without a graph.
func rankMetrics(ranks []int) RankMetrics {
	m := RankMetrics{Queries: len(ranks)}
	if len(ranks) == 0 {
		return m
	}
	var mrrSum float64
	for _, r := range ranks {
		if r <= 0 {
			continue
		}
		m.Found++
		mrrSum += 1.0 / float64(r)
		if r <= 1 {
			m.RecallAt1++
		}
		if r <= 5 {
			m.RecallAt5++
		}
		if r <= 10 {
			m.RecallAt10++
		}
	}
	n := float64(len(ranks))
	m.MRR = round3(mrrSum / n)
	m.RecallAt1 = round3(m.RecallAt1 / n)
	m.RecallAt5 = round3(m.RecallAt5 / n)
	m.RecallAt10 = round3(m.RecallAt10 / n)
	return m
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }

func queryCasePasses(paths []string, qc QueryCase) bool {
	if len(qc.MustContainAnyPath) > 0 {
		for _, group := range qc.MustContainAnyPath {
			if len(missingSubstrings(paths, group)) == 0 {
				return true
			}
		}
		return false
	}
	return len(missingSubstrings(paths, qc.MustContainPath)) == 0
}

func queryCaseFailureDetail(paths []string, qc QueryCase) string {
	if len(qc.MustContainAnyPath) > 0 {
		var parts []string
		for i, group := range qc.MustContainAnyPath {
			miss := missingSubstrings(paths, group)
			if len(miss) > 0 {
				parts = append(parts, fmt.Sprintf("group %d missing %s", i+1, strings.Join(miss, ", ")))
			}
		}
		return "no must_contain_any_path group satisfied; " + strings.Join(parts, "; ")
	}
	miss := missingSubstrings(paths, qc.MustContainPath)
	return "missing path-substring(s): " + strings.Join(miss, ", ")
}

func missingSubstrings(paths, needles []string) []string {
	var miss []string
	for _, n := range needles {
		found := false
		for _, p := range paths {
			if strings.Contains(p, n) {
				found = true
				break
			}
		}
		if !found {
			miss = append(miss, n)
		}
	}
	return miss
}

func writeSummary(w io.Writer, res *Result) {
	fmt.Fprintf(w, "eval summary: total=%d passed=%d failed=%d\n", res.Total, res.Passed, res.Failed)
	if res.Rank != nil {
		fmt.Fprintf(w, "rank quality (%d queries): MRR=%.3f Recall@1=%.3f Recall@5=%.3f Recall@10=%.3f found=%d\n",
			res.Rank.Queries, res.Rank.MRR, res.Rank.RecallAt1, res.Rank.RecallAt5, res.Rank.RecallAt10, res.Rank.Found)
	}
	for _, c := range res.Cases {
		marker := "PASS"
		if !c.Pass {
			marker = "FAIL"
		}
		fmt.Fprintf(w, "  %s %s", marker, c.Name)
		if c.Detail != "" {
			fmt.Fprintf(w, " -- %s", c.Detail)
		}
		fmt.Fprintln(w)
	}
}

// LoadSuite reads a Suite JSON document from a reader. Empty JSON yields
// the bundled Default suite for convenience.
func LoadSuite(r io.Reader) (Suite, error) {
	var s Suite
	if r == nil {
		return Default(), nil
	}
	dec := json.NewDecoder(r)
	if err := dec.Decode(&s); err != nil {
		if err == io.EOF {
			return Default(), nil
		}
		return Suite{}, err
	}
	if len(s.Queries) == 0 && len(s.Prompts) == 0 {
		return Default(), nil
	}
	return s, nil
}
