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
// placeholders to concrete symbols (call/read/inherits/implements power
// context + impact — PHP/Ruby/C# class inbound relies on inheritance edges).
var resolvableEdgeKinds = map[string]bool{
	"calls":      true,
	"reads":      true,
	"inherits":   true,
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
	nameOf := map[string]string{}
	kindOf := map[string]string{}
	pathOf := map[string]string{}
	methodsByType := map[string]map[string][]string{}
	embedsOf := map[string][]string{} // type name -> embedded/parent/trait names (promoted methods)
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
			nameOf[id] = name
		}
		kindOf[id] = kind
		// Methods + ParentID-bound functions (GDScript class_name funcs before
		// Kind=method, Lua/Python class methods stored as function).
		if parent != "" && name != "" && (kind == "method" || kind == "function") {
			byMethod := methodsByType[parent]
			if byMethod == nil {
				byMethod = map[string][]string{}
				methodsByType[parent] = byMethod
			}
			byMethod[name] = append(byMethod[name], id)
		}
		// Go/PHP/Ruby store embeds= anywhere in Signature (may follow docs/frameworks=).
		if (kind == "class" || kind == "interface") && name != "" {
			for _, emb := range embedsFromSignature(sig) {
				addEmbed(embedsOf, name, emb)
			}
		}
		pathOf[id] = path
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return stats, err
	}
	rows.Close()

	// Seed embedsOf from inherits/implements edges (PHP traits, Ruby mixins/superclass,
	// C# base_list) so recv_type can promote trait/parent methods even when Signature
	// lacked embeds= (older indexes / edge-only densification).
	if irows, err := s.db.QueryContext(ctx,
		`SELECT src_id, dst_id FROM edges WHERE repo_id=? AND kind IN ('inherits','implements')`, repoID); err == nil {
		for irows.Next() {
			var src, dst string
			if irows.Scan(&src, &dst) != nil {
				continue
			}
			srcName := nameOf[src]
			parent := ""
			if strings.HasPrefix(dst, "symref:") {
				parent = symrefName(dst)
			} else {
				parent = nameOf[dst]
			}
			addEmbed(embedsOf, srcName, parent)
		}
		irows.Close()
	}

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
			if id := pickUniqueOrImpl(methodsByType[recvType][method], pathOf); id != "" {
				target, strategy = id, "recv_type"
				conf = ConfidenceForStrategy(strategy)
			} else if id := lookupEmbeddedMethod(recvType, method, methodsByType, embedsOf, pathOf); id != "" {
				// Promoted method reached through struct embedding.
				target, strategy = id, "embedded"
				conf = ConfidenceForStrategy(strategy)
			}
			// Fall back to the bare method name for the cascade below.
			name = method
		}
		candidates := byName[name]
		if len(candidates) > 0 {
			filtered := make([]string, 0, len(candidates))
			for _, c := range candidates {
				if c != e.src {
					filtered = append(filtered, c)
				}
			}
			candidates = filtered
		}
		callerPath := pathOf[e.src]
		if target == "" {
			target, conf, strategy = pickCandidate(name, candidates, callerPath, importsByFile[callerPath], pathOf, kindOf, e.kind)
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
		// Resolved edges can collide with parser-emitted edges that already have
		// the same src/kind/dst (densify + alias expansion). Upsert like AddEdge.
		// The WHERE clause is required, not decorative: SQLite cannot tell an
		// UPSERT's ON from a join's ON in INSERT…SELECT without it, and rejects
		// the statement as a syntax error near "DO".
		if _, err := tx.ExecContext(ctx, `
INSERT INTO edges (id, repo_id, kind, src_id, dst_id, confidence)
SELECT id, ?, kind, src_id, dst_id, confidence FROM _symref_new WHERE true
ON CONFLICT(id) DO UPDATE SET confidence=MAX(edges.confidence, excluded.confidence)`, repoID); err != nil {
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
func lookupEmbeddedMethod(recvType, method string, methodsByType map[string]map[string][]string, embedsOf map[string][]string, pathOf map[string]string) string {
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
			if id := pickUniqueOrImpl(methodsByType[t][method], pathOf); id != "" {
				found = append(found, id)
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
// one. edgeKind narrows last-resort demotion (calls/reads prefer app over vendor).
func pickCandidate(name string, candidates []string, srcPath string, callerImports []string, pathOf map[string]string, kindOf map[string]string, edgeKind string) (string, float64, string) {
	switch len(candidates) {
	case 0:
		return "", 0, ""
	case 1:
		return candidates[0], ConfidenceForStrategy("unique"), "unique"
	}
	// 0. Type reads/inherits: prefer a unique class/interface over same-named
	//    ctor/method collisions (Unreal UHealthComponent class vs ctor decl).
	if edgeKind == "reads" || edgeKind == "inherits" || edgeKind == "implements" {
		if pref := filterByKinds(candidates, kindOf, "class", "interface", "struct"); len(pref) == 1 {
			return pref[0], ConfidenceForStrategy("type_kind"), "type_kind"
		}
	}
	// 1. Same file as the caller (strongest local signal for forward refs).
	if sameFile := filterByFile(candidates, srcPath, pathOf); len(sameFile) == 1 {
		return sameFile[0], ConfidenceForStrategy("same_file"), "same_file"
	}
	// 2. Import-aware: the caller imports exactly one of the candidates'
	//    packages. Relative JS/TS imports (./x, ../y) resolve against the caller file.
	//    PHP/Composer FQCNs (Foo\Bar\Baz) match PSR-ish path segments.
	if imp := filterByImportWithCaller(candidates, callerImports, srcPath, pathOf); len(imp) == 1 {
		return imp[0], ConfidenceForStrategy("import"), "import"
	}
	if ns := filterByNamespacedImport(name, candidates, callerImports, pathOf); len(ns) == 1 {
		return ns[0], ConfidenceForStrategy("import"), "import"
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
	// 6. For calls/reads only: prefer a unique non-vendor candidate (PHP/Ruby
	//    app methods colliding with vendor helpers). Never for inherits —
	//    Laravel `extends User` must still bind Illuminate via import.
	if edgeKind == "calls" || edgeKind == "reads" {
		if pref := filterNonVendor(candidates, pathOf); len(pref) == 1 {
			return pref[0], ConfidenceForStrategy("non_vendor"), "non_vendor"
		}
	}
	// 7. C++/Unreal header+cpp method twins — prefer .cpp definition.
	if edgeKind == "calls" || edgeKind == "reads" {
		if id := pickUniqueOrImpl(candidates, pathOf); id != "" && len(candidates) > 1 {
			return id, ConfidenceForStrategy("impl_over_decl"), "impl_over_decl"
		}
	}
	// Otherwise ambiguous: leave it as a placeholder (prefer unknown over wrong).
	return "", 0, ""
}

// filterByKinds keeps candidates whose kind is in want.
func filterByKinds(ids []string, kindOf map[string]string, want ...string) []string {
	set := map[string]bool{}
	for _, w := range want {
		set[w] = true
	}
	var out []string
	for _, id := range ids {
		if set[kindOf[id]] {
			out = append(out, id)
		}
	}
	return out
}

// pickUniqueOrImpl returns the sole id, or the sole .cpp/.cc/.cxx impl when
// header+definition twins share a name (Unreal/C++ ApplyDamage.h/.cpp).
func pickUniqueOrImpl(ids []string, pathOf map[string]string) string {
	switch len(ids) {
	case 0:
		return ""
	case 1:
		return ids[0]
	}
	var impls []string
	for _, id := range ids {
		p := strings.ToLower(pathOf[id])
		switch {
		case strings.HasSuffix(p, ".cpp"), strings.HasSuffix(p, ".cc"),
			strings.HasSuffix(p, ".cxx"), strings.HasSuffix(p, ".c"):
			impls = append(impls, id)
		}
	}
	if len(impls) == 1 {
		return impls[0]
	}
	return ""
}

// embedsFromSignature extracts type names from an embeds=… fragment anywhere in Signature.
func embedsFromSignature(sig string) []string {
	i := strings.Index(sig, "embeds=")
	if i < 0 {
		return nil
	}
	rest := sig[i+len("embeds="):]
	end := len(rest)
	for j := 0; j < len(rest); j++ {
		if rest[j] == ' ' || rest[j] == ';' {
			end = j
			break
		}
	}
	var out []string
	for _, p := range strings.Split(rest[:end], ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func addEmbed(embedsOf map[string][]string, typeName, parent string) {
	typeName = strings.TrimSpace(typeName)
	parent = strings.TrimSpace(parent)
	if typeName == "" || parent == "" || typeName == parent {
		return
	}
	for _, e := range embedsOf[typeName] {
		if e == parent {
			return
		}
	}
	embedsOf[typeName] = append(embedsOf[typeName], parent)
}

// filterNonVendor keeps candidates outside vendor/node_modules trees.
func filterNonVendor(candidates []string, pathOf map[string]string) []string {
	var out []string
	for _, c := range candidates {
		p := strings.ToLower(strings.ReplaceAll(pathOf[c], "\\", "/"))
		if strings.Contains(p, "/vendor/") || strings.HasPrefix(p, "vendor/") ||
			strings.Contains(p, "/node_modules/") || strings.HasPrefix(p, "node_modules/") {
			continue
		}
		out = append(out, c)
	}
	return out
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
		"/addons/", "/library/", // Godot addons + Unity Library cache
		// PHP / Laravel scaffold (factories & seeders collide with model names).
		"/database/factories/", "/database/seeders/", "/datafixtures/",
		"/factories/", "/seeders/",
	} {
		if strings.Contains(p, seg) {
			return true
		}
	}
	for _, prefix := range []string{
		"docs_src/", "sample/", "samples/", "examples/", "example/",
		"integration/", "fixtures/", "test/", "tests/",
		"addons/", "library/",
		"database/factories/", "database/seeders/",
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
// Repo-relative targets (expanded aliases, PHP require paths) match by full
// path — never by a trailing package-dir suffix — so `src/lib/db` cannot
// latch onto an unrelated `db/` package.
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
			case (strings.HasPrefix(imp, "./") || strings.HasPrefix(imp, "../")) && callerDir != "":
				matched = importPathMatchesCandidate(normalizePath(path.Clean(callerDir+"/"+imp)), candPath)
			case looksRepoRootedImport(imp):
				// Expanded JS/TS alias (@/lib/db → src/lib/db) or PHP require of a
				// literal repo path. Path-anchored only — no package-suffix fallback.
				matched = importPathMatchesCandidate(normalizePath(path.Clean(imp)), candPath)
			case imp == pkgDir || strings.HasSuffix(imp, "/"+pkgDir):
				// Go/module package path (example.com/proj/pkgb → pkgb/). Require a
				// path separator before the package dir so "foodb" ≠ "db".
				matched = true
			}
			if matched && !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}

// importPathMatchesCandidate reports whether a resolved import path designates
// the candidate's file: the same file, the same file ignoring extensions, or a
// directory containing it (an `index.ts` / directory import).
func importPathMatchesCandidate(resolved, candPath string) bool {
	if resolved == "" || candPath == "" {
		return false
	}
	candNoExt := stripPathExt(candPath)
	resolvedNoExt := stripPathExt(resolved)
	return candPath == resolved || candNoExt == resolved || candNoExt == resolvedNoExt ||
		strings.HasPrefix(candPath, resolved+"/") || strings.HasPrefix(candNoExt, resolved+"/")
}

// looksRepoRootedImport reports an import string that is already a repo-relative
// path rather than a package name or a language namespace. Package specifiers
// ("react", "@scope/pkg"), PHP/C# namespaces (Foo\Bar), and domain-qualified
// module paths (example.com/proj/pkg, github.com/foo/bar) are excluded so those
// keep using package-suffix matching.
func looksRepoRootedImport(imp string) bool {
	if imp == "" || strings.HasPrefix(imp, "@") || strings.HasPrefix(imp, "#") ||
		strings.HasPrefix(imp, "~") || strings.HasPrefix(imp, "/") ||
		strings.Contains(imp, `\`) || strings.Contains(imp, "://") {
		return false
	}
	if !strings.Contains(imp, "/") {
		return false
	}
	// Domain-like first segment → Go/npm module path, not a repo file path.
	first, _, _ := strings.Cut(imp, "/")
	if strings.Contains(first, ".") {
		return false
	}
	return true
}

// filterByNamespacedImport matches PHP/Composer-style FQCN imports
// (`Illuminate\Foundation\Auth\User`) to candidates whose path contains the
// PSR-ish path and whose basename equals the resolved leaf name.
func filterByNamespacedImport(name string, candidates []string, callerImports []string, pathOf map[string]string) []string {
	if name == "" || len(callerImports) == 0 {
		return nil
	}
	var psrPaths []string
	for _, imp := range callerImports {
		imp = strings.TrimSpace(imp)
		if !strings.Contains(imp, `\`) {
			continue
		}
		parts := strings.Split(imp, `\`)
		leaf := parts[len(parts)-1]
		if leaf != name {
			continue
		}
		psrPaths = append(psrPaths, strings.Join(parts, "/"))
	}
	if len(psrPaths) == 0 {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range candidates {
		p := normalizePath(pathOf[c])
		if p == "" {
			continue
		}
		base := path.Base(stripPathExt(p))
		if base != name {
			continue
		}
		for _, psr := range psrPaths {
			parent := path.Dir(psr)
			if strings.Contains(p, psr) || (parent != "." && parent != "" && strings.Contains(p, parent+"/"+name)) ||
				(parent != "." && parent != "" && strings.Contains(p, parent)) {
				if !seen[c] {
					seen[c] = true
					out = append(out, c)
				}
				break
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
