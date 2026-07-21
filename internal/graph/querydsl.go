package graph

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// CallersOf returns symbols with a calls OR reads edge pointing to calleeID.
// Including reads catches module/factory references (common in JS/TS CJS
// exports and similar) that never form a direct call edge — without this,
// change_kit/context report "no callers" and agents treat edits as risk-free.
func (s *Store) CallersOf(ctx context.Context, repoID, calleeID string) ([]types.Symbol, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT s.id, s.repo_id, s.name, s.kind, s.path, s.line_start, s.line_end, s.language, COALESCE(s.signature,''), COALESCE(s.parent_id,'')
FROM edges e JOIN symbols s ON s.id = e.src_id AND s.repo_id = e.repo_id
WHERE e.repo_id = ? AND e.dst_id = ? AND e.kind IN (?, ?)`,
		repoID, calleeID, string(types.RefKindCalls), string(types.RefKindReads))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// Hub is a highly-referenced symbol — the load-bearing code the rest of the repo
// leans on. Surfaced in project_context so an agent grasps "what's linked" up
// front instead of discovering it with a chain of trace/context calls.
type Hub struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Kind    string `json:"kind"`
	Callers int    `json:"callers"`
}

// TopHubs returns the `limit` symbols with the most inbound calls edges
// (call-graph centrality), most-called first. It is one grouped query with a
// LIMIT — far cheaper than loading the whole in-degree map — so it is safe on the
// once-per-session project_context path even on large repos.
func (s *Store) TopHubs(ctx context.Context, repoID string, limit int) ([]Hub, error) {
	if limit <= 0 {
		limit = 8
	}
	// Over-fetch so vendored/demo/fixture code (filtered below) doesn't shrink the
	// list — hubs are about THIS project's own load-bearing code, not deps or
	// tutorial trees that otherwise drown real centrality (FastAPI docs_src, Nest sample/).
	rows, err := s.db.QueryContext(ctx, `
SELECT s.id, s.name, s.path, s.line_start, s.kind, COUNT(*) AS deg
FROM edges e JOIN symbols s ON s.id = e.dst_id AND s.repo_id = e.repo_id
WHERE e.repo_id = ? AND e.kind = ? AND e.dst_id LIKE 'sym:%'
GROUP BY e.dst_id
ORDER BY deg DESC, s.path ASC
LIMIT ?`, repoID, string(types.RefKindCalls), limit*24)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hub
	for rows.Next() {
		var h Hub
		if err := rows.Scan(&h.ID, &h.Name, &h.Path, &h.Line, &h.Kind, &h.Callers); err != nil {
			return nil, err
		}
		if isVendorPath(h.Path) {
			continue
		}
		// CSS / stylesheet paths inflate hubs via accidental call edges; demote.
		if isStyleHubPath(h.Path) {
			continue
		}
		out = append(out, h)
		if len(out) >= limit {
			break
		}
	}
	return out, rows.Err()
}

// PackageHub is a directory (package) that the rest of the repo calls into a lot
// — the architectural load-bearing modules. Package-level "what's linked",
// complementing the symbol-level hubs.
type PackageHub struct {
	Dir      string `json:"dir"`
	Callers  int    `json:"callers"`   // inbound calls from OTHER packages
	FromPkgs int    `json:"from_pkgs"` // distinct calling packages (fan-in breadth)
}

// TopPackages ranks packages by cross-package inbound calls — "which modules does
// the rest of the code depend on". It streams the resolved call edges and buckets
// them by directory in Go (SQLite has no dirname), counting only calls that cross
// a package boundary so a package isn't inflated by its own internal chatter.
// Language-agnostic (directories are packages everywhere) and internal by
// construction (the call graph only holds resolved in-repo edges).
func (s *Store) TopPackages(ctx context.Context, repoID string, limit int) ([]PackageHub, error) {
	if limit <= 0 {
		limit = 6
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT ss.path, sd.path
FROM edges e
JOIN symbols ss ON ss.id = e.src_id AND ss.repo_id = e.repo_id
JOIN symbols sd ON sd.id = e.dst_id AND sd.repo_id = e.repo_id
WHERE e.repo_id = ? AND e.kind = ? AND e.dst_id LIKE 'sym:%'`, repoID, string(types.RefKindCalls))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	calls := map[string]int{}
	from := map[string]map[string]struct{}{}
	for rows.Next() {
		var srcPath, dstPath string
		if err := rows.Scan(&srcPath, &dstPath); err != nil {
			return nil, err
		}
		sd, dd := dirOf(srcPath), dirOf(dstPath)
		if dd == "" || dd == "." || sd == dd || isVendorPath(dstPath) {
			continue // same-package, root, or vendored target: not an architecture edge
		}
		calls[dd]++
		if from[dd] == nil {
			from[dd] = map[string]struct{}{}
		}
		from[dd][sd] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]PackageHub, 0, len(calls))
	for dir, c := range calls {
		out = append(out, PackageHub{Dir: dir, Callers: c, FromPkgs: len(from[dir])})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Callers != out[j].Callers {
			return out[i].Callers > out[j].Callers
		}
		return out[i].Dir < out[j].Dir
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// isVendorPath reports whether a path is vendored, generated, dependency, test,
// or secondary demo/fixture code that isn't part of the project's own architecture
// hubs. Kept broad so TopHubs/TopPackages surface production packages.
func isVendorPath(p string) bool {
	p = strings.ToLower(strings.ReplaceAll(p, "\\", "/"))
	switch {
	case strings.HasPrefix(p, "third_party/"), strings.HasPrefix(p, "vendor/"),
		strings.Contains(p, "/vendor/"), strings.Contains(p, "node_modules/"),
		strings.Contains(p, "/.venv/"), strings.Contains(p, "site-packages/"),
		strings.Contains(p, "/dist/"), strings.HasSuffix(p, ".min.js"),
		// Build / generated output dirs (Nuxt/Next, Rust/Maven, Python bytecode).
		strings.HasPrefix(p, ".output/"), strings.Contains(p, "/.output/"),
		strings.Contains(p, "/.next/"), strings.Contains(p, "/.nuxt/"),
		strings.Contains(p, "/target/"), strings.Contains(p, "__pycache__/"):
		return true
	}
	// Test + secondary demo/fixture trees drown library hubs (Nest sample/,
	// FastAPI docs_src/, Fiber *_test.go, Express examples/).
	for _, seg := range []string{
		"/test/", "/tests/", "/__tests__/", "/spec/", "/specs/",
		"/docs_src/", "/sample/", "/samples/", "/examples/", "/example/",
		"/integration/", "/fixtures/", "/fixture/", "/testdata/",
		"/_expected/", "/benchmarking/", "/playground/", "/playgrounds/",
		"/test/acceptance/", "/acceptance/",
	} {
		if strings.Contains(p, seg) {
			return true
		}
	}
	for _, prefix := range []string{
		"test/", "tests/", "docs_src/", "sample/", "samples/", "examples/",
		"example/", "integration/", "fixtures/", "benchmarking/", "playground/",
		"playgrounds/",
	} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	if strings.HasSuffix(p, "_test.go") || strings.Contains(path.Base(p), "_expected") ||
		strings.HasPrefix(path.Base(p), "expected.") {
		return true
	}
	return false
}

// isStyleHubPath demotes CSS/stylesheet symbols from architecture hubs (Svelte
// expected.css fixtures and global stylesheets drown real code centrality).
func isStyleHubPath(p string) bool {
	p = strings.ToLower(strings.ReplaceAll(p, "\\", "/"))
	base := path.Base(p)
	switch {
	case strings.HasSuffix(base, ".css"), strings.HasSuffix(base, ".scss"),
		strings.HasSuffix(base, ".sass"), strings.HasSuffix(base, ".less"),
		strings.HasSuffix(base, ".styl"):
		return true
	case strings.Contains(p, "/styles/"), strings.Contains(p, "/css/"),
		strings.HasPrefix(p, "styles/"), strings.HasPrefix(p, "css/"):
		return true
	}
	return false
}

// CallersOfLimited is CallersOf capped at `limit` rows — for consumers that only
// display a handful (e.g. context shows 12). Without an ORDER BY the LIMIT lets
// SQLite stop early instead of materializing every caller of a hub symbol (Django's
// assertEqual has ~9.6k), turning a ~40ms full scan of the result set into sub-ms.
func (s *Store) CallersOfLimited(ctx context.Context, repoID, calleeID string, limit int) ([]types.Symbol, error) {
	if limit <= 0 {
		return s.CallersOf(ctx, repoID, calleeID)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT s.id, s.repo_id, s.name, s.kind, s.path, s.line_start, s.line_end, s.language, COALESCE(s.signature,''), COALESCE(s.parent_id,'')
FROM edges e JOIN symbols s ON s.id = e.src_id AND s.repo_id = e.repo_id
WHERE e.repo_id = ? AND e.dst_id = ? AND e.kind IN (?, ?)
LIMIT ?`, repoID, calleeID, string(types.RefKindCalls), string(types.RefKindReads), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// CountCallers returns how many distinct symbols reference calleeID via calls
// or reads — an index-backed COUNT, so a consumer can report "N callers
// (showing 12)" without materializing all N.
func (s *Store) CountCallers(ctx context.Context, repoID, calleeID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT src_id) FROM edges WHERE repo_id = ? AND dst_id = ? AND kind IN (?, ?)`,
		repoID, calleeID, string(types.RefKindCalls), string(types.RefKindReads)).Scan(&n)
	return n, err
}

// CalleesOf returns symbols reached by outgoing calls from callerID.
func (s *Store) CalleesOf(ctx context.Context, repoID, callerID string) ([]types.Symbol, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT s.id, s.repo_id, s.name, s.kind, s.path, s.line_start, s.line_end, s.language, COALESCE(s.signature,''), COALESCE(s.parent_id,'')
FROM edges e JOIN symbols s ON s.id = e.dst_id AND s.repo_id = e.repo_id
WHERE e.repo_id = ? AND e.src_id = ? AND e.kind = ?`, repoID, callerID, string(types.RefKindCalls))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// NeighborSymbol is a one-hop graph neighbor plus the edge that reached it.
type NeighborSymbol struct {
	Symbol     types.Symbol
	Confidence float64
	EdgeKind   string
}

// Neighbors returns the symbols one hop from nodeID along any of the given edge
// kinds, in a SINGLE indexed JOIN — replacing EdgesTo/EdgesFrom + a SymbolByID per
// edge (an N+1 that fired a query per neighbor, ruinous on hub symbols with tens
// of thousands of callers). incoming=true walks toward nodeID (callers/dependents);
// incoming=false walks away (callees/dependencies). Non-symbol endpoints (files,
// modules) are naturally excluded by the JOIN, matching the old per-edge skip.
func (s *Store) Neighbors(ctx context.Context, repoID, nodeID string, incoming bool, kinds ...string) ([]NeighborSymbol, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	joinCol, whereCol := "src_id", "dst_id" // incoming: the neighbor is the edge source
	if !incoming {
		joinCol, whereCol = "dst_id", "src_id"
	}
	q := `SELECT s.id, s.repo_id, s.name, s.kind, s.path, s.line_start, s.line_end, s.language, COALESCE(s.signature,''), COALESCE(s.parent_id,''), e.confidence, e.kind
FROM edges e JOIN symbols s ON s.id = e.` + joinCol + ` AND s.repo_id = e.repo_id
WHERE e.repo_id = ? AND e.` + whereCol + ` = ? AND e.kind IN (` + placeholders(len(kinds)) + `)`
	args := make([]any, 0, len(kinds)+2)
	args = append(args, repoID, nodeID)
	for _, k := range kinds {
		args = append(args, k)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NeighborSymbol
	for rows.Next() {
		var n NeighborSymbol
		var sym types.Symbol
		if err := rows.Scan(&sym.ID, &sym.RepoID, &sym.Name, &sym.Kind, &sym.Path, &sym.LineStart, &sym.LineEnd, &sym.Language, &sym.Signature, &sym.ParentID, &n.Confidence, &n.EdgeKind); err != nil {
			return nil, err
		}
		n.Symbol = sym
		out = append(out, n)
	}
	return out, rows.Err()
}

// ImportsOf returns symbols/files that import dstID (incoming imports edges).
func (s *Store) ImportsOf(ctx context.Context, repoID, dstID string) ([]types.Symbol, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT s.id, s.repo_id, s.name, s.kind, s.path, s.line_start, s.line_end, s.language, COALESCE(s.signature,''), COALESCE(s.parent_id,'')
FROM edges e JOIN symbols s ON s.id = e.src_id AND s.repo_id = e.repo_id
WHERE e.repo_id = ? AND e.dst_id = ? AND e.kind = ?`, repoID, dstID, string(types.RefKindImports))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// PathBetweenSymbols searches a short calls path from startID to endID (BFS, bounded).
func (s *Store) PathBetweenSymbols(ctx context.Context, repoID, startID, endID string, maxDepth int) ([]types.Symbol, error) {
	if maxDepth <= 0 {
		maxDepth = 6
	}
	type node struct {
		id   string
		path []string
	}
	queue := []node{{id: startID, path: []string{startID}}}
	seen := map[string]struct{}{startID: {}}
	for depth := 0; depth < maxDepth && len(queue) > 0; depth++ {
		next := []node{}
		for _, cur := range queue {
			if cur.id == endID {
				var out []types.Symbol
				for _, sid := range cur.path {
					sym, err := s.SymbolByID(ctx, repoID, sid)
					if err != nil || sym == nil {
						return nil, fmt.Errorf("path resolve: %s", sid)
					}
					out = append(out, *sym)
				}
				return out, nil
			}
			edges, err := s.EdgesFrom(ctx, repoID, cur.id, string(types.RefKindCalls))
			if err != nil {
				return nil, err
			}
			for _, e := range edges {
				if _, ok := seen[e.TargetID]; ok {
					continue
				}
				seen[e.TargetID] = struct{}{}
				np := append(append([]string{}, cur.path...), e.TargetID)
				next = append(next, node{id: e.TargetID, path: np})
			}
		}
		queue = next
	}
	return nil, fmt.Errorf("no call path within depth %d", maxDepth)
}

// DependencyDistance returns shortest calls-edge distance between symbols.
func (s *Store) DependencyDistance(ctx context.Context, repoID, startID, endID string, maxDepth int) (int, error) {
	if startID == endID {
		return 0, nil
	}
	if maxDepth <= 0 {
		maxDepth = 8
	}
	type hop struct {
		id    string
		depth int
	}
	queue := []hop{{id: startID, depth: 0}}
	seen := map[string]struct{}{startID: {}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= maxDepth {
			continue
		}
		edges, err := s.EdgesFrom(ctx, repoID, cur.id, string(types.RefKindCalls), string(types.RefKindImports))
		if err != nil {
			return -1, err
		}
		for _, e := range edges {
			if e.TargetID == endID {
				return cur.depth + 1, nil
			}
			if _, ok := seen[e.TargetID]; ok {
				continue
			}
			seen[e.TargetID] = struct{}{}
			queue = append(queue, hop{id: e.TargetID, depth: cur.depth + 1})
		}
	}
	return -1, fmt.Errorf("no dependency path within depth %d", maxDepth)
}
