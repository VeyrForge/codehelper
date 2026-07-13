package eval

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/pkg/types"
)

// TestRunComputesRankMetrics proves the full Run -> retrieval -> rank-metrics path
// on a deterministic synthetic index: an exact-name match must rank #1, so MRR and
// Recall@1 are 1.0 for that query and the not-found query contributes 0.
func TestRunComputesRankMetrics(t *testing.T) {
	root := t.TempDir()
	const repo = "repo"
	if err := os.MkdirAll(paths.RepoIndexDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := graph.Open(paths.DBPath(root))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	syms := []types.Symbol{
		{ID: "sym:t", RepoID: repo, Name: "HandleRequest", Kind: types.SymbolKindFunction, Path: "internal/foo/target.go", Language: "go"},
		{ID: "sym:n1", RepoID: repo, Name: "parseConfig", Kind: types.SymbolKindFunction, Path: "internal/bar/other.go", Language: "go"},
		{ID: "sym:n2", RepoID: repo, Name: "writeOutput", Kind: types.SymbolKindFunction, Path: "internal/baz/sink.go", Language: "go"},
	}
	for _, s := range syms {
		if err := st.UpsertSymbol(ctx, s); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	_ = st.Close()

	suite := Suite{Queries: []QueryCase{
		{Query: "HandleRequest", MustContainPath: []string{"internal/foo/target.go"}, TopK: 10},
		{Query: "totallyabsentsymbolxyz", MustContainPath: []string{"internal/nowhere.go"}, TopK: 10},
	}}
	res, err := Run(ctx, root, repo, suite, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Rank == nil {
		t.Fatal("rank metrics not computed")
	}
	if res.Cases[0].Rank != 1 {
		t.Errorf("exact-name target should rank #1, got %d (hits=%v)", res.Cases[0].Rank, res.Cases[0].Hits)
	}
	if res.Cases[1].Rank != 0 {
		t.Errorf("absent target should rank 0, got %d", res.Cases[1].Rank)
	}
	// 2 queries: one at rank 1, one not found -> MRR=(1+0)/2=0.5, Recall@1=0.5.
	if res.Rank.MRR != 0.5 || res.Rank.RecallAt1 != 0.5 {
		t.Errorf("MRR=%.3f Recall@1=%.3f want 0.5/0.5", res.Rank.MRR, res.Rank.RecallAt1)
	}
	if res.Rank.Found != 1 {
		t.Errorf("found=%d want 1", res.Rank.Found)
	}
}

func TestFirstRelevantRank(t *testing.T) {
	paths := []string{"a/x.go", "b/y.go", "c/z.go"}
	cases := []struct {
		needles []string
		want    int
	}{
		{[]string{"b/y.go"}, 2},
		{[]string{"a/x.go"}, 1},
		{[]string{"c/z.go"}, 3},
		{[]string{"nope.go"}, 0},
		{[]string{"", "c/z"}, 3},      // empty needle ignored, substring matches
		{[]string{"y.go", "x.go"}, 1}, // first matching hit wins, not first needle
	}
	for _, tc := range cases {
		if got := firstRelevantRank(paths, tc.needles); got != tc.want {
			t.Errorf("firstRelevantRank(%v)=%d want %d", tc.needles, got, tc.want)
		}
	}
}

func TestRankMetrics(t *testing.T) {
	// ranks: one at #1, one at #3, one at #8, one not found (0).
	m := rankMetrics([]int{1, 3, 8, 0})
	if m.Queries != 4 || m.Found != 3 {
		t.Fatalf("queries/found = %d/%d want 4/3", m.Queries, m.Found)
	}
	// MRR = (1/1 + 1/3 + 1/8 + 0) / 4 = 1.4583/4 = 0.365
	if m.MRR != 0.365 {
		t.Errorf("MRR=%.3f want 0.365", m.MRR)
	}
	if m.RecallAt1 != 0.25 { // 1 of 4 at rank 1
		t.Errorf("Recall@1=%.3f want 0.25", m.RecallAt1)
	}
	if m.RecallAt5 != 0.5 { // ranks 1 and 3
		t.Errorf("Recall@5=%.3f want 0.5", m.RecallAt5)
	}
	if m.RecallAt10 != 0.75 { // ranks 1,3,8
		t.Errorf("Recall@10=%.3f want 0.75", m.RecallAt10)
	}
	if rankMetrics(nil).Queries != 0 {
		t.Error("empty ranks must yield zero-value metrics")
	}
}

// TestGoldenRankQualityFloor runs the golden suite against the live codehelper
// index and guards a ranking-quality floor — the regression signal a loose top-K
// pass/fail can't provide: if a change buries targets deeper in the ranking,
// MRR/Recall@1 drop below the floor and this fails.
//
// Gated behind CODEHELPER_EVAL_INTEGRATION=1 (like TestDefaultSuiteRetrievalCases_Pass)
// because it needs a CLEAN committed index — a mid-edit working tree skews the
// diff/recency ranking boosts and makes the numbers meaningless. The pure
// TestRankMetrics / TestFirstRelevantRank guard the metric math on every run.
func TestGoldenRankQualityFloor(t *testing.T) {
	if os.Getenv("CODEHELPER_EVAL_INTEGRATION") == "" {
		t.Skip("set CODEHELPER_EVAL_INTEGRATION=1 to run the live rank-quality floor against the workspace index")
	}
	root := repoRoot(t)
	if root == "" || !dirExists(filepath.Join(root, ".codehelper")) {
		t.Skip("codehelper repo not indexed (.codehelper absent) — skipping live rank floor")
	}
	suite, err := Golden()
	if err != nil || len(suite.Queries) == 0 {
		suite = Default()
	}
	suite.Prompts = nil // rank metrics are about retrieval only
	res, err := Run(context.Background(), root, "codehelper", suite, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rank == nil || res.Rank.Queries == 0 {
		t.Fatal("no rank metrics computed")
	}
	t.Logf("golden rank quality: %d queries MRR=%.3f R@1=%.3f R@5=%.3f R@10=%.3f found=%d",
		res.Rank.Queries, res.Rank.MRR, res.Rank.RecallAt1, res.Rank.RecallAt5, res.Rank.RecallAt10, res.Rank.Found)
	// Conservative floors — a regression that buries targets trips these.
	if res.Rank.RecallAt10 < 0.75 {
		t.Errorf("Recall@10=%.3f below floor 0.75", res.Rank.RecallAt10)
	}
	if res.Rank.MRR < 0.30 {
		t.Errorf("MRR=%.3f below floor 0.30", res.Rank.MRR)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for i := 0; i < 6; i++ {
		if fileExists(filepath.Join(dir, "go.mod")) && dirExists(filepath.Join(dir, ".codehelper")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func fileExists(p string) bool { fi, err := os.Stat(p); return err == nil && !fi.IsDir() }
func dirExists(p string) bool  { fi, err := os.Stat(p); return err == nil && fi.IsDir() }
