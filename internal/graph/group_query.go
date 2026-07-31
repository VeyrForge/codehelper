package graph

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// GroupQueryHit is one fan-out symbol match from a workspace-group member graph.
type GroupQueryHit struct {
	Repo     string `json:"repo"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Path     string `json:"path"`
	Language string `json:"language,omitempty"`
	ID       string `json:"id"`
}

// RankGroupQueryHits filters by optional path substring (suffix/contains, like
// MCP path=), prefers non-fixture paths, then applies limit. Shared multi-root
// sqlite is still deferred — this only ranks per-member fan-out results.
func RankGroupQueryHits(hits []GroupQueryHit, pathFilter string, limit int) []GroupQueryHit {
	pathFilter = strings.TrimSpace(pathFilter)
	out := make([]GroupQueryHit, 0, len(hits))
	for _, h := range hits {
		if pathFilter != "" && !groupPathMatches(h.Path, pathFilter) {
			continue
		}
		out = append(out, h)
	}
	sort.SliceStable(out, func(i, j int) bool {
		fi, fj := isFixturePath(out[i].Path), isFixturePath(out[j].Path)
		if fi != fj {
			return !fi && fj
		}
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Name < out[j].Name
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// GroupQueryAmbiguous reports whether the same symbol name appears under more
// than one (repo, path) — agents should pass path= on follow-up context/impact.
func GroupQueryAmbiguous(hits []GroupQueryHit) bool {
	if len(hits) < 2 {
		return false
	}
	seenName := map[string]map[string]bool{}
	for _, h := range hits {
		key := h.Name
		loc := h.Repo + "\x00" + h.Path
		if seenName[key] == nil {
			seenName[key] = map[string]bool{}
		}
		seenName[key][loc] = true
		if len(seenName[key]) > 1 {
			return true
		}
	}
	return false
}

// PathMatches reports whether symPath matches MCP/CLI path= (suffix, contains, or exact).
func PathMatches(symPath, want string) bool {
	return groupPathMatches(symPath, want)
}

func groupPathMatches(symPath, want string) bool {
	a := strings.ToLower(strings.ReplaceAll(symPath, "\\", "/"))
	b := strings.ToLower(strings.ReplaceAll(want, "\\", "/"))
	b = strings.TrimPrefix(b, "./")
	if b == "" {
		return true
	}
	return a == b || strings.HasSuffix(a, "/"+b) || strings.HasSuffix(a, b) || strings.Contains(a, b)
}

// GroupMemberDB is one workspace-group member graph opened for fan-out search.
type GroupMemberDB struct {
	Repo  string
	Store *Store
}

// GroupQueryResult is the ranked fan-out payload shared by CLI and MCP.
type GroupQueryResult struct {
	GroupID   string          `json:"group_id"`
	Query     string          `json:"query"`
	Path      string          `json:"path,omitempty"`
	Hits      []GroupQueryHit `json:"hits"`
	Count     int             `json:"count"`
	Ambiguous bool            `json:"ambiguous,omitempty"`
	Note      string          `json:"note,omitempty"`
	WhatNext  string          `json:"what_next,omitempty"`
}

// SearchMemberSymbols looks up name in one member graph (FTS, then path/name LIKE).
// Group fan-out is name-oriented: when any hit has an exact (case-insensitive)
// symbol name match, doc-comment / soft FTS hits (e.g. InventoryClient mentioned
// in CheckoutService's comment) are dropped so agents get the real definition.
func SearchMemberSymbols(ctx context.Context, st *Store, repoID, name string, limit int) ([]types.Symbol, error) {
	if st == nil {
		return nil, fmt.Errorf("graph store is nil")
	}
	if limit <= 0 {
		limit = 20
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	syms, err := st.SearchSymbolsFTS(ctx, repoID, []string{name}, limit)
	if err != nil {
		return nil, err
	}
	if len(syms) == 0 {
		syms, err = st.SearchSymbolsPath(ctx, repoID, name, limit)
		if err != nil {
			return nil, err
		}
	}
	return preferExactSymbolNameHits(syms, name), nil
}

// preferExactSymbolNameHits keeps EqualFold(name) hits when any exist; otherwise
// keeps Type.Method / suffix matches for dotted queries; otherwise returns nil
// (no soft doc-only FTS noise for group locate).
func preferExactSymbolNameHits(syms []types.Symbol, name string) []types.Symbol {
	name = strings.TrimSpace(name)
	if name == "" || len(syms) == 0 {
		return syms
	}
	var exact []types.Symbol
	for _, s := range syms {
		if strings.EqualFold(s.Name, name) {
			exact = append(exact, s)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	// Dotted locate (App.Use): accept exact full name or trailing .Method.
	if strings.Contains(name, ".") {
		var dotted []types.Symbol
		suf := "." + name[strings.LastIndex(name, ".")+1:]
		for _, s := range syms {
			if strings.EqualFold(s.Name, name) || strings.HasSuffix(strings.ToLower(s.Name), strings.ToLower(suf)) {
				dotted = append(dotted, s)
			}
		}
		if len(dotted) > 0 {
			return dotted
		}
	}
	return nil
}

// FanOutGroupQuery searches each member graph for name, ranks hits, and sets ambiguity hints.
func FanOutGroupQuery(ctx context.Context, groupID, name, pathFilter string, limit int, members []GroupMemberDB) GroupQueryResult {
	groupID = strings.TrimSpace(groupID)
	name = strings.TrimSpace(name)
	if limit <= 0 {
		limit = 20
	}
	perRepo := limit
	if perRepo > 50 {
		perRepo = 50
	}
	var raw []GroupQueryHit
	for _, m := range members {
		if m.Store == nil || strings.TrimSpace(m.Repo) == "" {
			continue
		}
		syms, err := SearchMemberSymbols(ctx, m.Store, m.Repo, name, perRepo)
		if err != nil || len(syms) == 0 {
			continue
		}
		for _, s := range syms {
			raw = append(raw, GroupQueryHit{
				Repo: m.Repo, Name: s.Name, Kind: string(s.Kind),
				Path: s.Path, Language: s.Language, ID: s.ID,
			})
		}
	}
	hits := RankGroupQueryHits(raw, pathFilter, limit)
	res := GroupQueryResult{
		GroupID: groupID,
		Query:   name,
		Path:    strings.TrimSpace(pathFilter),
		Hits:    hits,
		Count:   len(hits),
		Note:    "fan-out across member graph.db files; not a shared multi-root sqlite",
	}
	if GroupQueryAmbiguous(hits) {
		res.Ambiguous = true
		res.Note += "; duplicate names — pass path= (or MCP path=) on context/impact"
		res.WhatNext = "Retry query with path=… then context name=… repo=<hit.repo> path=… (or sym: id from group_hits)"
	}
	return res
}

// GroupQueryAmbiguousNames returns symbol names that appear under more than one (repo, path).
func GroupQueryAmbiguousNames(hits []GroupQueryHit) []string {
	seen := map[string]map[string]bool{}
	for _, h := range hits {
		loc := h.Repo + "\x00" + h.Path
		if seen[h.Name] == nil {
			seen[h.Name] = map[string]bool{}
		}
		seen[h.Name][loc] = true
	}
	var out []string
	for name, locs := range seen {
		if len(locs) > 1 {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
