package review

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/VeyrForge/codehelper/internal/detect"
	"github.com/VeyrForge/codehelper/internal/graph"
)

type ContractGuardResult struct {
	BreakingChanges []Finding `json:"breaking_changes"`
}

func ContractGuard(ctx context.Context, st *graph.Store, repoRoot, repoName, baseRef string) (*ContractGuardResult, error) {
	if strings.TrimSpace(baseRef) == "" {
		baseRef = "HEAD~1"
	}
	ids, err := detect.ChangedSymbols(ctx, repoRoot, repoName, baseRef, st)
	if err != nil {
		return nil, err
	}
	out := make([]Finding, 0, 8)
	for _, id := range ids {
		sym, err := st.SymbolByID(ctx, repoName, id)
		if err != nil || sym == nil {
			continue
		}
		// Tests are never part of the public contract — flagging Test* funcs
		// in *_test.go as breaking changes is pure noise.
		if IsTestPath(sym.Path) {
			continue
		}
		// Lockfiles, configs and compiled output are not "public symbols"
		// either; only consider real source code.
		if !IsCodeSourceFile(sym.Path) {
			continue
		}
		if likelyPublic(sym.Path, sym.Name) {
			out = append(out, Finding{
				Severity: SeverityHigh,
				Category: "contract",
				File:     sym.Path,
				Symbol:   sym.Name,
				Message:  "Public symbol changed; may break external callers.",
			})
		}
	}
	return &ContractGuardResult{BreakingChanges: out}, nil
}

func likelyPublic(path, name string) bool {
	p := strings.ToLower(strings.ReplaceAll(path, "\\", "/"))

	// Go convention: anything under an `internal/` directory is, by language
	// rule, not importable outside the owning module — so it cannot be a
	// public contract no matter how the symbol is named.
	if strings.HasPrefix(p, "internal/") || strings.Contains(p, "/internal/") {
		return false
	}
	// Project-local "private" markers used by Node/TS/Python codebases.
	if strings.Contains(p, "/_internal/") || strings.Contains(p, "/private/") {
		return false
	}

	r, _ := utf8.DecodeRuneInString(name)
	exported := r != utf8.RuneError && unicode.IsUpper(r)

	// Public surface heuristics: capitalized symbols OR symbols living in
	// canonical "shared" / "API" locations.
	return exported ||
		strings.Contains(p, "/pkg/") ||
		strings.Contains(p, "/cmd/") ||
		strings.Contains(p, "/api/") ||
		strings.HasPrefix(p, "pkg/") ||
		strings.HasPrefix(p, "cmd/") ||
		strings.HasPrefix(p, "api/")
}
