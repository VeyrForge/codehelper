package review

import (
	"context"
	"strings"

	"github.com/VeyrForge/codehelper/internal/detect"
	"github.com/VeyrForge/codehelper/internal/graph"
)

type TestGapResult struct {
	MissingTests []MissingTest `json:"missing_tests"`
}

func TestGap(ctx context.Context, st *graph.Store, repoRoot, repoName, baseRef string) (*TestGapResult, error) {
	if strings.TrimSpace(baseRef) == "" {
		baseRef = "HEAD~1"
	}
	ids, err := detect.ChangedSymbols(ctx, repoRoot, repoName, baseRef, st)
	if err != nil {
		return nil, err
	}
	out := make([]MissingTest, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		sym, err := st.SymbolByID(ctx, repoName, id)
		if err != nil || sym == nil {
			continue
		}
		if IsTestPath(sym.Path) {
			continue
		}
		if !IsCodeSourceFile(sym.Path) {
			continue
		}
		// Structs, type aliases, enums, interfaces have no executable
		// behavior of their own; asking for "happy-path tests" of a
		// data declaration is misleading noise.
		if !IsBehavioralSymbolKind(string(sym.Kind)) {
			continue
		}
		// If the file already has a colocated test, the test gap is at
		// best at the symbol level. Until we can verify symbol-level
		// coverage we skip the file rather than emit noisy false-positives.
		if HasSiblingTestFile(repoRoot, sym.Path) {
			continue
		}
		// Don't report the same symbol name twice (the diff often touches
		// many lines of one function, but the gap only needs reporting once).
		dedupeKey := sym.Path + "::" + sym.Name
		if _, ok := seen[dedupeKey]; ok {
			continue
		}
		seen[dedupeKey] = struct{}{}
		out = append(out, MissingTest{
			Symbol: sym.Name,
			Risk:   classifyTestRisk(sym.Path),
			NeededTests: []string{
				"happy-path behavior remains unchanged",
				"error-path regression coverage",
			},
		})
	}
	return &TestGapResult{MissingTests: out}, nil
}

func classifyTestRisk(path string) string {
	p := strings.ToLower(path)
	switch {
	case strings.Contains(p, "billing") || strings.Contains(p, "payment"):
		return "money_calculation"
	case strings.Contains(p, "auth") || strings.Contains(p, "security"):
		return "auth_boundary"
	default:
		return "behavior_regression"
	}
}
