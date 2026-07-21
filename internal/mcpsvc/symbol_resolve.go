package mcpsvc

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/pkg/types"
)

// symbolCandidate is one indexed symbol when a bare name matches multiple defs.
type symbolCandidate struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Loc  string `json:"loc"`
	Recv string `json:"recv,omitempty"`
}

// resolveSymbolByName resolves a symbol for context/impact. name may be a sym: id.
// wantPath disambiguates duplicate names (suffix match on definition path).
// Returns (sym, nil, nil) on unique match; (nil, candidates, nil) when ambiguous.
func resolveSymbolByName(ctx context.Context, st *graph.Store, repoID, name, wantPath string, wantLine int) (*types.Symbol, []symbolCandidate, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil, fmt.Errorf("symbol name is required")
	}
	if strings.HasPrefix(name, "sym:") {
		sym, err := st.SymbolByID(ctx, repoID, name)
		if err != nil {
			return nil, nil, err
		}
		if sym == nil {
			return nil, nil, fmt.Errorf("no symbol with id %q", name)
		}
		return sym, nil, nil
	}
	all, err := st.SymbolsByName(ctx, repoID, name, 200)
	if err != nil {
		return nil, nil, err
	}
	var exact []types.Symbol
	for _, s := range all {
		if s.Name == name {
			exact = append(exact, s)
		}
	}
	if len(exact) == 0 {
		return nil, nil, fmt.Errorf("symbol not found: %s", name)
	}
	if wantPath != "" || wantLine > 0 {
		var filtered []types.Symbol
		for _, s := range exact {
			if wantPath != "" && !pathMatches(s.Path, wantPath) {
				continue
			}
			if wantLine > 0 && s.LineStart != wantLine {
				continue
			}
			filtered = append(filtered, s)
		}
		exact = filtered
	}
	if len(exact) == 1 {
		return &exact[0], nil, nil
	}
	if len(exact) == 0 {
		return nil, nil, fmt.Errorf("no symbol named %q matched path=%q line=%d", name, wantPath, wantLine)
	}
	// Prefer production over sample/test when that leaves a unique match (Nest
	// sample collisions, FastAPI docs_src, Express examples).
	if pref := preferNonFixtureSymbols(exact); len(pref) == 1 {
		return &pref[0], nil, nil
	}
	// Framework monorepos often ONLY ship samples — pick a stable canonical
	// tutorial path so bare impact/context still answers without path=.
	if canon := preferCanonicalSample(exact); canon != nil {
		return canon, nil, nil
	}
	sort.Slice(exact, func(i, j int) bool {
		fi, fj := isFixtureSymbolPath(exact[i].Path), isFixtureSymbolPath(exact[j].Path)
		if fi != fj {
			return !fi && fj // non-fixture first
		}
		if exact[i].Path != exact[j].Path {
			return exact[i].Path < exact[j].Path
		}
		return exact[i].LineStart < exact[j].LineStart
	})
	cands := make([]symbolCandidate, 0, len(exact))
	for _, s := range exact {
		cands = append(cands, symbolCandidate{
			ID:   s.ID,
			Name: s.Name,
			Kind: string(s.Kind),
			Loc:  fmt.Sprintf("%s:%d", s.Path, s.LineStart),
			Recv: s.ParentID,
		})
	}
	return nil, cands, nil
}

func isFixtureSymbolPath(p string) bool {
	p = strings.ToLower(strings.ReplaceAll(p, "\\", "/"))
	for _, seg := range []string{
		"/sample/", "/samples/", "/examples/", "/example/", "/docs_src/",
		"/integration/", "/fixtures/", "/fixture/", "/testdata/",
		"/test/", "/tests/", "/__tests__/", "/spec/", "/specs/",
		"/playground/", "/playgrounds/", "/benchmarking/",
	} {
		if strings.Contains(p, seg) {
			return true
		}
	}
	for _, prefix := range []string{
		"sample/", "samples/", "examples/", "example/", "docs_src/",
		"integration/", "fixtures/", "test/", "tests/",
	} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

func preferNonFixtureSymbols(syms []types.Symbol) []types.Symbol {
	var out []types.Symbol
	for _, s := range syms {
		if !isFixtureSymbolPath(s.Path) {
			out = append(out, s)
		}
	}
	return out
}

// preferCanonicalSample picks sample/01-* (or lexicographically first sample)
// when every candidate lives under demo/fixture trees.
func preferCanonicalSample(syms []types.Symbol) *types.Symbol {
	if len(syms) == 0 {
		return nil
	}
	for _, s := range syms {
		if !isFixtureSymbolPath(s.Path) {
			return nil // mixed set — do not auto-pick a sample
		}
	}
	var best *types.Symbol
	bestScore := 1 << 30
	for i := range syms {
		s := &syms[i]
		p := strings.ToLower(strings.ReplaceAll(s.Path, "\\", "/"))
		score := 1000 + len(p)
		if strings.Contains(p, "/sample/01-") || strings.HasPrefix(p, "sample/01-") {
			score = 1
		} else if strings.Contains(p, "/sample/") || strings.HasPrefix(p, "sample/") {
			score = 10 + len(p)
		} else if strings.Contains(p, "/examples/") || strings.HasPrefix(p, "examples/") {
			score = 50 + len(p)
		} else if strings.Contains(p, "/integration/") {
			score = 100 + len(p)
		}
		if score < bestScore {
			bestScore = score
			best = s
		}
	}
	return best
}
