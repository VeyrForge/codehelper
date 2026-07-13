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
	sort.Slice(exact, func(i, j int) bool {
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
