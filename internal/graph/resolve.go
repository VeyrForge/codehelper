package graph

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"strings"
)

// SymrefStats reports the outcome of a symref resolution pass.
type SymrefStats struct {
	Total      int            `json:"total"`      // symref edges examined
	Resolved   int            `json:"resolved"`   // rewritten to a concrete symbol
	Ambiguous  int            `json:"ambiguous"`  // multiple candidates, left unresolved
	Unresolved int            `json:"unresolved"` // no defined symbol with that name
	ByStrategy map[string]int `json:"by_strategy,omitempty"`
}

func (s *SymrefStats) bump(strategy string) {
	if s.ByStrategy == nil {
		s.ByStrategy = map[string]int{}
	}
	s.ByStrategy[strategy]++
}

// resolvableEdgeKinds are the reference kinds worth resolving from symref
// placeholders to concrete symbols (call/read/implements power context + impact).
var resolvableEdgeKinds = map[string]bool{
	"calls":      true,
	"reads":      true,
	"implements": true,
}

// ResolveSymrefs rewrites edges that point at unresolved `symref:` placeholders
// to concrete symbol IDs whenever the referenced name resolves unambiguously.
//
// This is the precision fix for cross-file and forward-reference callers: the
// per-file parser emits `symref:repoID:relPath:name` when it cannot bind a call
// or read to a symbol it has already seen. This pass runs after the whole repo
// is indexed, so it can resolve those names against every defined symbol.
//
// Resolution is conservative to protect precision:
//   - a unique repo-wide name match is resolved (confidence 0.8);
//   - otherwise, a unique match within the caller's own file is resolved
//     (confidence 0.7, recovers same-file forward references);
//   - genuinely ambiguous names are left as symref placeholders.
func (s *Store) ResolveSymrefs(ctx context.Context, repoID string) (SymrefStats, error) {
	var stats SymrefStats

	// Load every defined symbol: name -> candidate ids, and id -> file path.
	// methodsByType indexes methods by their receiver type (stored in parent_id
	// by the Go parser) so `x.Foo()` calls can resolve to the right type's method.
	byName := map[string][]string{}
	pathOf := map[string]string{}
	methodsByType := map[string]map[string][]string{}
	embedsOf := map[string][]string{} // type name -> embedded type names (promoted methods)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, path, kind, COALESCE(parent_id,''), COALESCE(signature,'') FROM symbols WHERE repo_id=?`, repoID)
	if err != nil {
		return stats, err
	}
	for rows.Next() {
		var id, name, path, kind, parent, sig string
		if err := rows.Scan(&id, &name, &path, &kind, &parent, &sig); err != nil {
			rows.Close()
			return stats, err
		}
		if name != "" {
			byName[name] = append(byName[name], id)
		}
		if kind == "method" && parent != "" && name != "" {
			byMethod := methodsByType[parent]
			if byMethod == nil {
				byMethod = map[string][]string{}
				methodsByType[parent] = byMethod
			}
			byMethod[name] = append(byMethod[name], id)
		}
		if kind == "class" && name != "" && strings.HasPrefix(sig, "embeds=") {
			embedsOf[name] = strings.Split(strings.TrimPrefix(sig, "embeds="), ",")
		}
		pathOf[id] = path
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return stats, err
	}
	rows.Close()

	// Load per-file imports so resolution can disambiguate cross-package calls
	// (import-aware matching — the highest-confidence strategy). Maps a file's
	// relPath to the set of module paths it imports.
	importsByFile := map[string][]string{}
	irows, err := s.db.QueryContext(ctx,
		`SELECT src_id, dst_id FROM edges WHERE repo_id=? AND kind='imports' AND src_id LIKE 'file:%'`, repoID)
	if err == nil {
		for irows.Next() {
			var src, dst string
			if irows.Scan(&src, &dst) == nil {
				fp := fileNodePath(src)
				mp := modulePath(dst)
				if fp != "" && mp != "" {
					importsByFile[fp] = append(importsByFile[fp], mp)
				}
			}
		}
		irows.Close()
	}

	// Load symref edges to resolve.
	type symrefEdge struct {
		id, kind, src, dst string
	}
	var edges []symrefEdge
	erows, err := s.db.QueryContext(ctx,
		`SELECT id, kind, src_id, dst_id FROM edges WHERE repo_id=? AND dst_id LIKE 'symref:%'`, repoID)
	if err != nil {
		return stats, err
	}
	for erows.Next() {
		var e symrefEdge
		if err := erows.Scan(&e.id, &e.kind, &e.src, &e.dst); err != nil {
			erows.Close()
			return stats, err
		}
		edges = append(edges, symrefEdge{e.id, e.kind, e.src, e.dst})
	}
	if err := erows.Err(); err != nil {
		erows.Close()
		return stats, err
	}
	erows.Close()

	// Resolve every symref edge in memory, then apply inserts/deletes in one
	// set-oriented pass via temp tables. Per-edge ExecContext was the dominant
	// cost on large repos (~35s on Django); batched prepared statements were
	// tried in v2.43 and regressed on modernc.org/sqlite — temp-table bulk
	// writes avoid both pitfalls.
	var toInsert []symrefInsert
	var toDelete []string
	for _, e := range edges {
		if !resolvableEdgeKinds[e.kind] {
			continue
		}
		stats.Total++
		name := symrefName(e.dst)
		if name == "" {
			stats.Unresolved++
			continue
		}
		var target, strategy string
		var conf float64
		// Type-qualified call (`T.Foo` from receiver-type inference): resolve
		// against the type's own methods first — the highest-precision strategy,
		// since it disambiguates same-named methods on different types.
		if recvType, method, ok := splitRecv(name); ok {
			if ids := methodsByType[recvType][method]; len(ids) == 1 {
				target, strategy = ids[0], "recv_type"
				conf = ConfidenceForStrategy(strategy)
			} else if id := lookupEmbeddedMethod(recvType, method, methodsByType, embedsOf); id != "" {
				// Promoted method reached through struct embedding.
				target, strategy = id, "embedded"
				conf = ConfidenceForStrategy(strategy)
			}
			// Fall back to the bare method name for the cascade below.
			name = method
		}
		candidates := byName[name]
		callerPath := pathOf[e.src]
		if target == "" {
			target, conf, strategy = pickCandidate(name, candidates, callerPath, importsByFile[callerPath], pathOf)
		}
		switch {
		case target == "":
			if len(candidates) > 1 {
				stats.Ambiguous++
			} else {
				stats.Unresolved++
			}
			continue
		case target == e.src:
			// Self reference (e.g. recursion); drop the symref noise.
			toDelete = append(toDelete, e.id)
			stats.Resolved++
			stats.bump("self")
			continue
		}
		stats.bump(strategy)
		newID := fmt.Sprintf("e:%s:%s:%s:%s", repoID, e.src, e.kind, target)
		toInsert = append(toInsert, symrefInsert{id: newID, kind: e.kind, src: e.src, dst: target, conf: conf})
		toDelete = append(toDelete, e.id)
		stats.Resolved++
	}

	if len(toInsert) == 0 && len(toDelete) == 0 {
		return stats, nil
	}
	toInsert = dedupeSymrefInserts(toInsert)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return stats, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := bulkApplySymrefResolutions(ctx, tx, repoID, toInsert, toDelete); err != nil {
		return stats, err
	}
	if err := tx.Commit(); err != nil {
		return stats, err
	}
	return stats, nil
}

// symrefInsert is one resolved edge row staged for bulk insert.
type symrefInsert struct {
	id, kind, src, dst string
	conf               float64
}

// bulkSymrefChunk is the max rows per multi-value INSERT into a temp table.
// Five bind vars per row; stay well under SQLite's 999-variable limit.
const bulkSymrefChunk = 150

// bulkApplySymrefResolutions writes resolved edges and removes symref placeholders
// using in-memory temp tables and set-oriented SQL — one INSERT…SELECT and one
// DELETE…IN (SELECT…) instead of two ExecContext calls per resolved edge.
func bulkApplySymrefResolutions(ctx context.Context, tx *sql.Tx, repoID string, inserts []symrefInsert, deleteIDs []string) error {
	if len(inserts) > 0 {
		if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS _symref_new (
			id TEXT PRIMARY KEY, kind TEXT NOT NULL, src_id TEXT NOT NULL,
			dst_id TEXT NOT NULL, confidence REAL NOT NULL)`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM _symref_new`); err != nil {
			return err
		}
		for start := 0; start < len(inserts); start += bulkSymrefChunk {
			end := start + bulkSymrefChunk
			if end > len(inserts) {
				end = len(inserts)
			}
			chunk := inserts[start:end]
			var sb strings.Builder
			sb.WriteString(`INSERT INTO _symref_new (id, kind, src_id, dst_id, confidence) VALUES `)
			args := make([]any, 0, len(chunk)*5)
			for i, ins := range chunk {
				if i > 0 {
					sb.WriteByte(',')
				}
				sb.WriteString(`(?,?,?,?,?)`)
				args = append(args, ins.id, ins.kind, ins.src, ins.dst, ins.conf)
			}
			if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO edges (id, repo_id, kind, src_id, dst_id, confidence)
SELECT id, ?, kind, src_id, dst_id, confidence FROM _symref_new`, repoID); err != nil {
			return err
		}
	}
	if len(deleteIDs) > 0 {
		if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS _symref_del (id TEXT PRIMARY KEY)`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM _symref_del`); err != nil {
			return err
		}
		for start := 0; start < len(deleteIDs); start += bulkSymrefChunk {
			end := start + bulkSymrefChunk
			if end > len(deleteIDs) {
				end = len(deleteIDs)
			}
			chunk := deleteIDs[start:end]
			var sb strings.Builder
			sb.WriteString(`INSERT INTO _symref_del (id) VALUES `)
			args := make([]any, 0, len(chunk))
			for i, id := range chunk {
				if i > 0 {
					sb.WriteByte(',')
				}
				sb.WriteString(`(?)`)
				args = append(args, id)
			}
			if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM edges WHERE repo_id=? AND id IN (SELECT id FROM _symref_del)`, repoID); err != nil {
			return err
		}
	}
	return nil
}

// dedupeSymrefInserts merges rows that share an edge id (two symrefs resolving to
// the same src/kind/target) keeping the highest confidence — the semantics the
// per-row ON CONFLICT MAX upsert provided before bulk writes.
func dedupeSymrefInserts(inserts []symrefInsert) []symrefInsert {
	if len(inserts) <= 1 {
		return inserts
	}
	byID := make(map[string]symrefInsert, len(inserts))
	order := make([]string, 0, len(inserts))
	for _, ins := range inserts {
		if prev, ok := byID[ins.id]; ok {
			if ins.conf > prev.conf {
				byID[ins.id] = ins
			}
			continue
		}
		byID[ins.id] = ins
		order = append(order, ins.id)
	}
	out := make([]symrefInsert, len(order))
	for i, id := range order {
		out[i] = byID[id]
	}
	return out
}

// lookupEmbeddedMethod resolves a promoted method by walking the embedding chain
// of recvType breadth-first, returning a unique match or "" when none/ambiguous.
// The visited set guards against embedding cycles (illegal in Go, but cheap to
// defend). A method found on exactly one embedded type at the shallowest depth
// wins; ties at the same depth are left unresolved (Go would flag them ambiguous).
func lookupEmbeddedMethod(recvType, method string, methodsByType map[string]map[string][]string, embedsOf map[string][]string) string {
	visited := map[string]bool{recvType: true}
	frontier := append([]string(nil), embedsOf[recvType]...)
	for len(frontier) > 0 {
		var found []string
		var next []string
		for _, t := range frontier {
			t = strings.TrimSpace(t)
			if t == "" || visited[t] {
				continue
			}
			visited[t] = true
			if ids := methodsByType[t][method]; len(ids) == 1 {
				found = append(found, ids[0])
			}
			next = append(next, embedsOf[t]...)
		}
		if len(found) == 1 {
			return found[0]
		}
		if len(found) > 1 {
			return "" // ambiguous promotion at this depth
		}
		frontier = next
	}
	return ""
}

// RevertEdgesIntoPaths reverts resolved call/read edges whose TARGET symbol lives
// in one of the given paths back to `symref:` placeholders, so a subsequent
// ResolveSymrefs re-binds them. This is the incremental-indexing correctness fix:
// re-parsing a file changes its symbol IDs (they encode line numbers), which would
// otherwise orphan caller edges coming from unchanged files. Reverting preserves
// those callers across the edit. Call it BEFORE the changed file's symbols are
// deleted. Method targets are reverted with their receiver type (`T.M`) so the
// high-precision receiver-type strategy still applies on re-resolution.
func (s *Store) RevertEdgesIntoPaths(ctx context.Context, repoID string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	insert, err := tx.PrepareContext(ctx, `
INSERT INTO edges (id, repo_id, kind, src_id, dst_id, confidence)
VALUES (?, ?, ?, ?, ?, 0.5) ON CONFLICT(id) DO NOTHING`)
	if err != nil {
		return err
	}
	defer insert.Close()
	del, err := tx.PrepareContext(ctx, `DELETE FROM edges WHERE id=? AND repo_id=?`)
	if err != nil {
		return err
	}
	defer del.Close()

	type rev struct{ id, kind, src, name, symKind, parent string }
	for _, p := range paths {
		rows, err := tx.QueryContext(ctx, `
SELECT e.id, e.kind, e.src_id, s.name, s.kind, COALESCE(s.parent_id,'')
FROM edges e JOIN symbols s ON s.id = e.dst_id AND s.repo_id = e.repo_id
WHERE e.repo_id=? AND s.path=? AND e.dst_id LIKE 'sym:%' AND e.kind IN ('calls','reads')`, repoID, p)
		if err != nil {
			return err
		}
		var revs []rev
		for rows.Next() {
			var r rev
			if err := rows.Scan(&r.id, &r.kind, &r.src, &r.name, &r.symKind, &r.parent); err != nil {
				rows.Close()
				return err
			}
			revs = append(revs, r)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		for _, r := range revs {
			callerPath := symIDPath(r.src)
			if callerPath == "" || r.name == "" {
				continue
			}
			callee := r.name
			if r.symKind == "method" && r.parent != "" {
				callee = r.parent + "." + r.name
			}
			newDst := fmt.Sprintf("symref:%s:%s:%s", repoID, callerPath, callee)
			newID := fmt.Sprintf("e:%s:%s:%s:%s", repoID, r.src, r.kind, newDst)
			if _, err := insert.ExecContext(ctx, newID, repoID, r.kind, r.src, newDst); err != nil {
				return err
			}
			if _, err := del.ExecContext(ctx, r.id, repoID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// symIDPath extracts the file path from a `sym:repoID:relPath:line:name` id.
func symIDPath(id string) string {
	if !strings.HasPrefix(id, "sym:") {
		return ""
	}
	parts := strings.Split(id, ":")
	if len(parts) < 5 {
		return ""
	}
	return strings.Join(parts[2:len(parts)-2], ":")
}

// splitRecv splits a type-qualified call name `Type.Method` into its receiver
// type and method. Returns ok=false for bare names (no dot) and for lowercase
// receivers (app.use / res.send Express aliases — those are symbol names, not
// Go-style type-qualified calls).
func splitRecv(name string) (recvType, method string, ok bool) {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 || i+1 >= len(name) {
		return "", "", false
	}
	recv := name[:i]
	meth := name[i+1:]
	if recv == "" || meth == "" {
		return "", "", false
	}
	// Type names are capitalized; Express/CJS aliases (app.use) are not.
	if recv[0] < 'A' || recv[0] > 'Z' {
		return "", "", false
	}
	return recv, meth, true
}

// symrefName extracts the trailing identifier from a `symref:repoID:relPath:name`
// placeholder. Identifiers contain no colons, so the last segment is the name.
func symrefName(dst string) string {
	if !strings.HasPrefix(dst, "symref:") {
		return ""
	}
	i := strings.LastIndexByte(dst, ':')
	if i < 0 || i+1 >= len(dst) {
		return ""
	}
	return dst[i+1:]
}

// pickCandidate chooses a concrete target for a name using a prioritized,
// precision-first cascade (modeled on the import-map → same-module → unique-name
// strategy stack from current code-graph research). It returns the chosen id, a
// confidence, and the strategy name, or "" when genuinely ambiguous — the
// correctness gate that prefers leaving an edge unresolved over wiring a wrong
// one.
func pickCandidate(name string, candidates []string, srcPath string, callerImports []string, pathOf map[string]string) (string, float64, string) {
	switch len(candidates) {
	case 0:
		return "", 0, ""
	case 1:
		return candidates[0], ConfidenceForStrategy("unique"), "unique"
	}
	// 1. Same file as the caller (strongest local signal for forward refs).
	if sameFile := filterByFile(candidates, srcPath, pathOf); len(sameFile) == 1 {
		return sameFile[0], ConfidenceForStrategy("same_file"), "same_file"
	}
	// 2. Import-aware: the caller imports exactly one of the candidates'
	//    packages. Relative JS/TS imports (./x, ../y) resolve against the caller file.
	if imp := filterByImportWithCaller(candidates, callerImports, srcPath, pathOf); len(imp) == 1 {
		return imp[0], ConfidenceForStrategy("import"), "import"
	}
	// 3. Same directory/package as the caller (same-module resolution).
	if sameDir := filterByDir(candidates, dirOf(srcPath), pathOf); len(sameDir) == 1 {
		return sameDir[0], ConfidenceForStrategy("same_dir"), "same_dir"
	}
	// 3b. Same app/package subtree (Nest sample/01-cats-app vs core/interceptors).
	if sub := filterBySubtree(candidates, srcPath, pathOf); len(sub) == 1 {
		return sub[0], ConfidenceForStrategy("same_subtree"), "same_subtree"
	}
	// 4. Well-known public API definitions (FastAPI Depends / include_router,
	//    etc.) when multiple same-named symbols exist across a library tree.
	if pref := filterByPublicAPI(name, candidates, pathOf); len(pref) == 1 {
		return pref[0], ConfidenceForStrategy("public_api"), "public_api"
	}
	// 5. Prefer production paths over sample/test/docs when exactly one
	//    candidate survives fixture demotion (Nest sample/ collisions, CSS
	//    fixtures, tutorial trees). Confidence is lower than same-dir.
	if pref := filterNonFixture(candidates, pathOf); len(pref) == 1 {
		return pref[0], ConfidenceForStrategy("non_fixture"), "non_fixture"
	}
	// Otherwise ambiguous: leave it as a placeholder (prefer unknown over wrong).
	return "", 0, ""
}

// publicAPIPathHints maps ambiguous public helper names to preferred defining
// path suffixes. Used only when same-file / import / same-dir fail.
var publicAPIPathHints = map[string][]string{
	"Depends":        {"param_functions.py"},
	"include_router": {"applications.py"},
	"APIRouter":      {"routing.py"},
	"FastAPI":        {"applications.py"},
}

func filterByPublicAPI(name string, candidates []string, pathOf map[string]string) []string {
	hints := publicAPIPathHints[name]
	if len(hints) == 0 {
		return nil
	}
	var out []string
	for _, c := range candidates {
		p := strings.ToLower(strings.ReplaceAll(pathOf[c], "\\", "/"))
		// Prefer library package paths over docs/tests/samples.
		if isFixturePath(p) {
			continue
		}
		for _, h := range hints {
			if strings.HasSuffix(p, strings.ToLower(h)) || strings.Contains(p, "/"+strings.ToLower(h)) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// filterNonFixture keeps candidates whose paths are not sample/test/docs noise.
// Used as a last-resort disambiguator when exactly one production definition remains.
func filterNonFixture(candidates []string, pathOf map[string]string) []string {
	var out []string
	for _, c := range candidates {
		p := strings.ToLower(strings.ReplaceAll(pathOf[c], "\\", "/"))
		if isFixturePath(p) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// isFixturePath mirrors hub noise demotion for symref resolution.
func isFixturePath(p string) bool {
	p = strings.ToLower(strings.ReplaceAll(p, "\\", "/"))
	for _, seg := range []string{
		"/docs_src/", "/sample/", "/samples/", "/examples/", "/example/",
		"/integration/", "/fixtures/", "/fixture/", "/testdata/",
		"/test/", "/tests/", "/__tests__/", "/spec/", "/specs/",
		"/_expected/", "/benchmarking/", "/playground/", "/playgrounds/",
	} {
		if strings.Contains(p, seg) {
			return true
		}
	}
	for _, prefix := range []string{
		"docs_src/", "sample/", "samples/", "examples/", "example/",
		"integration/", "fixtures/", "test/", "tests/",
	} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	base := p
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		base = p[i+1:]
	}
	if strings.HasPrefix(base, "expected.") || strings.Contains(base, "_expected") {
		return true
	}
	return false
}

// filterByFile keeps candidates defined in the exact file path.
func filterByFile(candidates []string, path string, pathOf map[string]string) []string {
	if path == "" {
		return nil
	}
	var out []string
	for _, c := range candidates {
		if pathOf[c] == path {
			out = append(out, c)
		}
	}
	return out
}

// filterByDir keeps candidates whose file is in dir.
func filterByDir(candidates []string, dir string, pathOf map[string]string) []string {
	if dir == "" {
		return nil
	}
	var out []string
	for _, c := range candidates {
		if dirOf(pathOf[c]) == dir {
			out = append(out, c)
		}
	}
	return out
}

// filterByImport keeps candidates whose package directory is imported by the
// caller (Go-style package path suffix). Prefer filterByImportWithCaller when
// the caller's file path is known (relative JS/TS imports).
func filterByImport(candidates []string, callerImports []string, pathOf map[string]string) []string {
	return filterByImportWithCaller(candidates, callerImports, "", pathOf)
}

// filterByImportWithCaller resolves package-suffix imports and relative JS/TS
// imports (./x, ../y) against the caller's file so Nest
// `import { X } from './interceptors/x'` disambiguates same-named samples.
func filterByImportWithCaller(candidates []string, callerImports []string, callerPath string, pathOf map[string]string) []string {
	if len(callerImports) == 0 {
		return nil
	}
	callerDir := dirOf(normalizePath(callerPath))
	var out []string
	seen := map[string]bool{}
	for _, c := range candidates {
		candPath := normalizePath(pathOf[c])
		pkgDir := dirOf(candPath)
		if pkgDir == "" {
			continue
		}
		for _, imp := range callerImports {
			imp = strings.TrimSpace(imp)
			if imp == "" {
				continue
			}
			matched := false
			switch {
			case imp == pkgDir || strings.HasSuffix(imp, "/"+pkgDir) || strings.HasSuffix(imp, pkgDir):
				matched = true
			case (strings.HasPrefix(imp, "./") || strings.HasPrefix(imp, "../")) && callerDir != "":
				resolved := normalizePath(path.Clean(callerDir + "/" + imp))
				candNoExt := stripPathExt(candPath)
				resolvedNoExt := stripPathExt(resolved)
				if candPath == resolved || candNoExt == resolved || candNoExt == resolvedNoExt ||
					strings.HasPrefix(candPath, resolved+"/") || strings.HasPrefix(candNoExt, resolved+"/") {
					matched = true
				}
			}
			if matched && !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}

func normalizePath(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}

func stripPathExt(p string) string {
	p = normalizePath(p)
	if i := strings.LastIndexByte(p, '.'); i > strings.LastIndexByte(p, '/') {
		return p[:i]
	}
	return p
}

// filterBySubtree keeps candidates that share the caller's app/package root
// (sample/01-cats-app, packages/core, integration/hello-world, …).
func filterBySubtree(candidates []string, srcPath string, pathOf map[string]string) []string {
	root := subtreeRoot(srcPath)
	if root == "" {
		return nil
	}
	var out []string
	for _, c := range candidates {
		cp := normalizePath(pathOf[c])
		if cp == root || strings.HasPrefix(cp, root+"/") {
			out = append(out, c)
		}
	}
	return out
}

// subtreeRoot returns a stable multi-segment project root for monorepo samples
// and packages, or "" when the path is not under a recognized layout.
func subtreeRoot(p string) string {
	p = normalizePath(p)
	parts := strings.Split(p, "/")
	if len(parts) < 2 {
		return ""
	}
	switch parts[0] {
	case "sample", "samples", "integration", "packages", "apps", "services", "modules":
		return parts[0] + "/" + parts[1]
	}
	return ""
}

func dirOf(p string) string {
	if p == "" {
		return ""
	}
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return "."
}

// fileNodePath extracts relPath from a `file:repoID:relPath` node id.
func fileNodePath(id string) string {
	if !strings.HasPrefix(id, "file:") {
		return ""
	}
	parts := strings.SplitN(id, ":", 3)
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}

// modulePath extracts the import path from a `mod:repoID:path` node id.
func modulePath(id string) string {
	if !strings.HasPrefix(id, "mod:") {
		return ""
	}
	parts := strings.SplitN(id, ":", 3)
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}
