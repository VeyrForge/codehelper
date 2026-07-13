package retrieval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/VeyrForge/codehelper/internal/graph"
)

// ContextPackV2Item is one ranked file entry with token estimate.
type ContextPackV2Item struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Tokens int    `json:"tokens,omitempty"`
}

// ContextPackV2 buckets context by inclusion priority.
type ContextPackV2 struct {
	BudgetTokens  int                 `json:"budget_tokens"`
	MustInclude   []ContextPackV2Item `json:"must_include"`
	ShouldInclude []ContextPackV2Item `json:"should_include"`
	SummarizeOnly []ContextPackV2Item `json:"summarize_only"`
	Exclude       []ContextPackV2Item `json:"exclude"`
	Query         string              `json:"query"`
	Intent        string              `json:"intent,omitempty"`
}

// RoughTokens estimates tokenizer-ish size (heuristic, not exact).
func RoughTokens(text string) int {
	if text == "" {
		return 0
	}
	return utf8.RuneCountInString(text) / 4
}

func RoughTokensForFile(repoRoot, relPath string) int {
	p := filepath.Join(repoRoot, filepath.FromSlash(relPath))
	b, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	if len(b) > 512*1024 {
		b = b[:512*1024]
	}
	return RoughTokens(string(b))
}

var excludeDirNames = map[string]struct{}{
	"node_modules": {}, "vendor": {}, ".vendor": {}, ".git": {}, "dist": {}, "build": {},
	".next": {}, ".nuxt": {}, ".cache": {}, "__pycache__": {},
}

// BuildContextPackV2 ranks retrieval hits into buckets under a token budget.
func BuildContextPackV2(ctx context.Context, st *graph.Store, repoID, repoRoot, query, intent string, hits []RankedSymbol, budgetTokens int) (*ContextPackV2, error) {
	if budgetTokens <= 0 {
		budgetTokens = 24000
	}
	base, err := BuildContextPack(ctx, st, repoID, query, intent, hits, 40)
	if err != nil {
		return nil, err
	}
	out := &ContextPackV2{
		BudgetTokens:  budgetTokens,
		MustInclude:   []ContextPackV2Item{},
		ShouldInclude: []ContextPackV2Item{},
		SummarizeOnly: []ContextPackV2Item{},
		Exclude:       []ContextPackV2Item{},
		Query:         query,
		Intent:        strings.TrimSpace(intent),
	}
	// A retrieval hit list often contains multiple symbols per file. The v2
	// pack buckets *files*, not symbols, so we dedupe by path on first sight.
	seenPath := make(map[string]struct{}, len(base.ContextPack))
	used := 0
	for _, it := range base.ContextPack {
		if _, dup := seenPath[it.Path]; dup {
			continue
		}
		seenPath[it.Path] = struct{}{}
		if shouldExcludePath(it.Path) {
			out.Exclude = append(out.Exclude, ContextPackV2Item{
				Path: it.Path, Reason: "ignored dependency or generated path pattern",
			})
			continue
		}
		tok := RoughTokensForFile(repoRoot, it.Path)
		if tok == 0 {
			tok = 500
		}
		reason := strings.Join(it.Reason, "; ")
		if reason == "" {
			reason = "retrieval hit"
		}
		item := ContextPackV2Item{Path: it.Path, Reason: reason, Tokens: tok}
		switch classifyContextKind(it.Path) {
		case "entrypoint", "implementation":
			if used+tok <= budgetTokens/2 {
				out.MustInclude = append(out.MustInclude, item)
				used += tok
			} else {
				out.SummarizeOnly = append(out.SummarizeOnly, ContextPackV2Item{Path: it.Path, Reason: "budget: summarize only"})
			}
		case "test", "config":
			out.ShouldInclude = append(out.ShouldInclude, item)
		default:
			if used+tok <= budgetTokens {
				out.ShouldInclude = append(out.ShouldInclude, item)
				used += tok
			} else {
				out.SummarizeOnly = append(out.SummarizeOnly, ContextPackV2Item{Path: it.Path, Reason: "lower priority under budget"})
			}
		}
	}
	_ = ctx
	return out, nil
}

func shouldExcludePath(p string) bool {
	p = filepath.ToSlash(strings.ToLower(p))
	parts := strings.Split(p, "/")
	for _, part := range parts {
		if _, ok := excludeDirNames[part]; ok {
			return true
		}
	}
	return false
}
