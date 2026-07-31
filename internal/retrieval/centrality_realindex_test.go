package retrieval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/graph"
)

// goldenCase is one retrieval expectation: the symbol a developer most likely
// wants for query, identified by name and a path substring.
type goldenCase struct {
	query, name, path string
}

// Group A — specific concept queries. Lexical signal alone is usually enough,
// so these are no-regression controls: centrality must not push the target down.
var goldenSpecific = []goldenCase{
	{"reciprocal rank fusion", "RRF", "internal/retrieval/hybrid.go"},
	{"hybrid query options", "QueryHybridWithOptions", "internal/retrieval/hybrid.go"},
	{"add edge to graph", "AddEdge", "internal/graph/ingest.go"},
	{"callers of symbol", "CallersOf", "internal/graph/querydsl.go"},
	{"resolve repo initialized", "resolveRepoInitialized", "internal/mcpsvc/register.go"},
	{"build context pack", "BuildContextPack", "internal/retrieval/context.go"},
}

// Group B — bare, ambiguous common names (several definitions share the name,
// so lexical scores tie). Ground truth is the canonical, most-depended-on
// definition — the same default an IDE's "go to most-referenced" applies. This
// is exactly the case centrality is meant to disambiguate.
var goldenAmbiguous = []goldenCase{
	{"close", "Close", "internal/graph/store.go"},
	{"open", "Open", "internal/graph/store.go"},
	{"marshal", "Marshal", "internal/toon/toon.go"},
	{"lock", "Lock", "internal/daemon/lock.go"},
	{"save", "Save", "internal/taskstore/store.go"},
	{"load", "Load", "internal/taskstore/store.go"},
	{"walk", "Walk", "internal/parser/walk.go"},
}

// Group C — scout-style natural-language task descriptions (the form `scout`
// receives). The canonical reuse candidate is the load-bearing symbol that
// already does the job; centrality should surface it over a closer-but-obscure
// lexical match, which is exactly scout's "prefer high-caller candidates" advice.
var goldenScout = []goldenCase{
	{"open the graph database", "Open", "internal/graph/store.go"},
	{"close the store", "Close", "internal/graph/store.go"},
	{"save a task to disk", "Save", "internal/taskstore/store.go"},
	{"load a task from the store", "Load", "internal/taskstore/store.go"},
	{"acquire a single instance lock", "Lock", "internal/daemon/lock.go"},
	{"walk the parse tree", "Walk", "internal/parser/walk.go"},
}

// Group D — synonym-gap queries: the verb in the query differs from the verb in
// the symbol name (shut down→Close, obtain→Acquire, store/persist→Save). Pure
// lexical search misses these; query-vocabulary enrichment (synonym expansion)
// is what surfaces them. Ground truth is the canonical symbol that does the job.
var goldenSynonym = []goldenCase{
	{"obtain a single instance lock", "Acquire", "internal/daemon/lock.go"},
	{"shut down the store", "Close", "internal/graph/store.go"},
	{"persist a task to disk", "Save", "internal/taskstore/store.go"},
	{"construct a new task store", "New", "internal/taskstore/store.go"},
}

// TestCentralityBenchmark_RealIndex runs an A/B over the actual codehelper graph
// (.codehelper/graph.db). It reports MRR@10 / P@1 with the centrality boost off
// vs on, per group, and guards against an overall regression. Skipped when no
// local index is present (so it never blocks CI on a clean checkout).
//
// Run it directly to see the table:
//
//	CGO_ENABLED=1 go test ./internal/retrieval/ -run RealIndex -v
func TestCentralityBenchmark_RealIndex(t *testing.T) {
	dbPath := filepath.Join("..", "..", ".codehelper", "graph.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Skipf("no local index at %s — run `codehelper analyze` first", dbPath)
	}
	st, err := graph.Open(dbPath)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	const repo = "codehelper"

	rankOf := func(weight float64, gc goldenCase) int {
		hits, err := QueryHybridWithOptions(ctx, st, repo, gc.query, 10, QueryOptions{
			QueryTokens:      strings.Fields(strings.ToLower(gc.query)),
			CentralityWeight: weight,
		})
		if err != nil {
			t.Fatalf("query %q: %v", gc.query, err)
		}
		for i, h := range hits {
			if h.Symbol.Name == gc.name && strings.Contains(h.Symbol.Path, gc.path) {
				return i + 1
			}
		}
		return 0 // not found in top 10
	}

	runGroup := func(name string, cases []goldenCase) (off, on agg) {
		t.Logf("── %s ──", name)
		t.Logf("  %-26s  %-5s  %-5s", "query", "off", "on")
		for _, gc := range cases {
			ro, rn := rankOf(0, gc), rankOf(DefaultCentralityWeight, gc)
			mark := ""
			if rn != 0 && (ro == 0 || rn < ro) {
				mark = "  ↑ improved"
			} else if ro != 0 && (rn == 0 || rn > ro) {
				mark = "  ↓ REGRESSED"
			}
			t.Logf("  %-26s  %-5s  %-5s%s", gc.query, rankStr(ro), rankStr(rn), mark)
			off.add(ro)
			on.add(rn)
		}
		t.Logf("  %-26s  MRR@10 off=%.3f on=%.3f   P@1 off=%.2f on=%.2f",
			"GROUP", off.mrr/off.n, on.mrr/on.n, off.p1/off.n, on.p1/on.n)
		return off, on
	}

	offA, onA := runGroup("Group A: specific (no-regression controls)", goldenSpecific)
	offB, onB := runGroup("Group B: ambiguous (centrality should help)", goldenAmbiguous)
	offC, onC := runGroup("Group C: scout natural-language tasks", goldenScout)
	offD, onD := runGroup("Group D: synonym-gap (query enrichment should help)", goldenSynonym)

	offMRR := (offA.mrr + offB.mrr + offC.mrr + offD.mrr) / (offA.n + offB.n + offC.n + offD.n)
	onMRR := (onA.mrr + onB.mrr + onC.mrr + onD.mrr) / (onA.n + onB.n + onC.n + onD.n)
	offP1 := (offA.p1 + offB.p1 + offC.p1 + offD.p1) / (offA.n + offB.n + offC.n + offD.n)
	onP1 := (onA.p1 + onB.p1 + onC.p1 + onD.p1) / (onA.n + onB.n + onC.n + onD.n)
	t.Logf("══ OVERALL  MRR@10 off=%.3f on=%.3f (Δ%+.3f)   P@1 off=%.2f on=%.2f (Δ%+.2f) ══",
		offMRR, onMRR, onMRR-offMRR, offP1, onP1, onP1-offP1)

	// Guard: synonym-gap queries must land their target in the top-10.
	if onD.mrr/onD.n < 0.75 {
		t.Errorf("synonym-gap group too weak: MRR@10=%.3f (expansion not helping)", onD.mrr/onD.n)
	}

	// Guard: overall NL retrieval with centrality must stay strong.
	if onMRR < 0.92 {
		t.Errorf("overall MRR@10=%.3f below gate 0.92", onMRR)
	}

	// Guard: centrality must never make overall ranking worse.
	if onMRR < offMRR-1e-9 {
		t.Errorf("centrality regressed overall MRR: off=%.4f on=%.4f", offMRR, onMRR)
	}
}

type agg struct{ mrr, p1, n float64 }

func (a *agg) add(rank int) {
	a.n++
	if rank > 0 {
		a.mrr += 1.0 / float64(rank)
		if rank == 1 {
			a.p1++
		}
	}
}

func rankStr(r int) string {
	if r == 0 {
		return "—"
	}
	return itoa(r)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [4]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
