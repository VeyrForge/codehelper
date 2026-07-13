package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/retrieval"
)

// Qrels is a language-agnostic relevance-judgment set: each query lists the
// symbol-name substrings considered relevant. It lets the SAME ranking quality
// metrics (Recall@k, MRR, nDCG) be tracked on ANY indexed project — Go, PHP,
// Python, … — not just Go self-retrieval, so cross-language regressions surface.
// It also exports TREC-format run/qrels files so the standard `ir_measures` /
// `trec_eval` tooling can be pointed at codehelper without re-implementing it.
type Qrels struct {
	Queries []QrelsQuery `json:"queries"`
}

type QrelsQuery struct {
	ID       string   `json:"id"`
	Query    string   `json:"query"`
	Relevant []string `json:"relevant"` // case-insensitive symbol-name substrings
}

// QrelsReport holds aggregate ranking quality over a Qrels set.
type QrelsReport struct {
	Queries  int     `json:"queries"`
	Recall1  float64 `json:"recall_at_1"`
	Recall5  float64 `json:"recall_at_5"`
	Recall10 float64 `json:"recall_at_10"`
	MRR      float64 `json:"mrr"`
	NDCG10   float64 `json:"ndcg_at_10"`
}

// QrelsQueryResult is one query's outcome for verbose reporting.
type QrelsQueryResult struct {
	ID        string `json:"id"`
	Query     string `json:"query"`
	FirstRank int    `json:"first_rank"` // 0 = miss in top 10
	TopName   string `json:"top_name,omitempty"`
	TopPath   string `json:"top_path,omitempty"`
	Hit       bool   `json:"hit"`
}

// QrelsEvalDetail adds per-query rows to QrelsReport.
type QrelsEvalDetail struct {
	QrelsReport
	PerQuery []QrelsQueryResult `json:"per_query,omitempty"`
}

// LoadQrels reads a qrels JSON file.
func LoadQrels(path string) (Qrels, error) {
	var q Qrels
	b, err := os.ReadFile(path)
	if err != nil {
		return q, err
	}
	err = json.Unmarshal(b, &q)
	return q, err
}

func relevantHit(name string, rel []string) bool {
	n := strings.ToLower(name)
	for _, r := range rel {
		if r != "" && strings.Contains(n, strings.ToLower(r)) {
			return true
		}
	}
	return false
}

// QrelsEval runs each query through the live ranker and computes Recall@k, MRR,
// and nDCG@10 with binary relevance. Optionally writes TREC qrels.txt + run.txt
// into trecDir (empty to skip) for ir_measures / trec_eval.
func QrelsEval(ctx context.Context, st *graph.Store, repoID string, qf Qrels, trecDir string) (QrelsReport, error) {
	detail, err := QrelsEvalDetailed(ctx, st, repoID, "", qf, trecDir)
	return detail.QrelsReport, err
}

// QrelsEvalDetailed is like QrelsEval but uses repoRoot for full MCP ranking
// signals (vocab expansion, enrichment, primary language) and returns per-query rows.
func QrelsEvalDetailed(ctx context.Context, st *graph.Store, repoID, repoRoot string, qf Qrels, trecDir string) (QrelsEvalDetail, error) {
	var rep QrelsEvalDetail
	if len(qf.Queries) == 0 {
		return rep, nil
	}
	var runLines, qrelLines []string
	var h1, h5, h10 int
	var rrSum, ndcgSum float64
	retrieval.EnsureEmbedder()
	for _, q := range qf.Queries {
		opts := retrieval.QueryOptions{
			QueryTokens:      strings.Fields(strings.ToLower(q.Query)),
			CentralityWeight: retrieval.DefaultCentralityWeight,
		}
		if repoRoot != "" {
			opts = retrieval.MCPQueryOptionsWithProfile(repoRoot, "", opts.QueryTokens, nil)
		}
		hits, err := retrieval.QueryHybridWithOptions(ctx, st, repoID, q.Query, 10, opts)
		if err != nil {
			return rep, err
		}
		firstRank := 0
		dcg := 0.0
		relFound := 0
		qr := QrelsQueryResult{ID: q.ID, Query: q.Query}
		if len(hits) > 0 {
			qr.TopName = hits[0].Symbol.Name
			qr.TopPath = hits[0].Symbol.Path
		}
		for i, h := range hits {
			rel := relevantHit(h.Symbol.Name, q.Relevant)
			runLines = append(runLines, fmt.Sprintf("%s Q0 %s %d %.6f codehelper", q.ID, h.Symbol.ID, i+1, h.Score))
			if rel {
				if firstRank == 0 {
					firstRank = i + 1
				}
				dcg += 1.0 / math.Log2(float64(i+2)) // binary gain, 0-based discount
				relFound++
				qrelLines = append(qrelLines, fmt.Sprintf("%s 0 %s 1", q.ID, h.Symbol.ID))
			}
		}
		qr.FirstRank = firstRank
		qr.Hit = firstRank > 0
		rep.PerQuery = append(rep.PerQuery, qr)
		// Ideal DCG: all relevant-found ranked first.
		idcg := 0.0
		for i := 0; i < relFound && i < 10; i++ {
			idcg += 1.0 / math.Log2(float64(i+2))
		}
		if idcg > 0 {
			ndcgSum += dcg / idcg
		}
		if firstRank > 0 {
			rrSum += 1.0 / float64(firstRank)
			if firstRank <= 1 {
				h1++
			}
			if firstRank <= 5 {
				h5++
			}
			if firstRank <= 10 {
				h10++
			}
		}
	}
	n := float64(len(qf.Queries))
	rep.Queries = len(qf.Queries)
	rep.Recall1 = float64(h1) / n
	rep.Recall5 = float64(h5) / n
	rep.Recall10 = float64(h10) / n
	rep.MRR = rrSum / n
	rep.NDCG10 = ndcgSum / n

	if trecDir != "" {
		if err := os.MkdirAll(trecDir, 0o755); err == nil {
			_ = os.WriteFile(filepath.Join(trecDir, "run.txt"), []byte(strings.Join(runLines, "\n")+"\n"), 0o644)
			_ = os.WriteFile(filepath.Join(trecDir, "qrels.txt"), []byte(strings.Join(qrelLines, "\n")+"\n"), 0o644)
		}
	}
	return rep, nil
}
