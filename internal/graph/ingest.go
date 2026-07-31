package graph

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ClearRepo removes all rows for repo_id (for full reindex).
func (s *Store) ClearRepo(ctx context.Context, repoID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{
		`DELETE FROM edges WHERE repo_id = ?`,
		`DELETE FROM symbols WHERE repo_id = ?`,
		`DELETE FROM files WHERE repo_id = ?`,
		`DELETE FROM processes WHERE repo_id = ?`,
		`DELETE FROM clusters WHERE repo_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, repoID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpsertFile inserts or replaces a file row.
func (s *Store) UpsertFile(ctx context.Context, f types.FileMeta) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO files (id, repo_id, path, language, size_bytes, hash)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  path=excluded.path, language=excluded.language, size_bytes=excluded.size_bytes, hash=excluded.hash`,
		f.ID, f.RepoID, f.Path, f.Language, f.Size, f.Hash)
	return err
}

// UpsertSymbol inserts or replaces symbol.
func (s *Store) UpsertSymbol(ctx context.Context, sym types.Symbol) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO symbols (id, repo_id, name, kind, path, line_start, line_end, language, signature, parent_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  name=excluded.name, kind=excluded.kind, path=excluded.path,
  line_start=excluded.line_start, line_end=excluded.line_end, language=excluded.language,
  signature=excluded.signature, parent_id=excluded.parent_id`,
		sym.ID, sym.RepoID, sym.Name, string(sym.Kind), sym.Path, sym.LineStart, sym.LineEnd,
		sym.Language, sym.Signature, nullStr(sym.ParentID))
	return err
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// AddEdge inserts an edge (idempotent by id).
func (s *Store) AddEdge(ctx context.Context, e types.Reference) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO edges (id, repo_id, kind, src_id, dst_id, confidence)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET confidence=excluded.confidence`,
		e.ID, e.RepoID, string(e.Kind), e.SourceID, e.TargetID, e.Confidence)
	return err
}

// FileIngest is one file's parsed output, ready to persist in a batch.
type FileIngest struct {
	File    types.FileMeta
	Symbols []types.Symbol
	Edges   []types.Reference
}

// IngestFiles writes many files' file/symbol/edge rows in a SINGLE transaction
// using prepared statements. On a cold build this is dramatically faster than
// the per-row autocommit path (one commit/fsync for the whole batch instead of
// one per symbol and edge — the dominant cost when indexing 100k+ files). Writes
// are idempotent upserts, so a re-run overwrites cleanly. Returns the number of
// symbol and edge rows written.
func (s *Store) IngestFiles(ctx context.Context, batch []FileIngest) (symCount, edgeCount int, err error) {
	if len(batch) == 0 {
		return 0, 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	fileStmt, err := tx.PrepareContext(ctx, `
INSERT INTO files (id, repo_id, path, language, size_bytes, hash)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  path=excluded.path, language=excluded.language, size_bytes=excluded.size_bytes, hash=excluded.hash`)
	if err != nil {
		return 0, 0, err
	}
	defer fileStmt.Close()
	symStmt, err := tx.PrepareContext(ctx, `
INSERT INTO symbols (id, repo_id, name, kind, path, line_start, line_end, language, signature, parent_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  name=excluded.name, kind=excluded.kind, path=excluded.path,
  line_start=excluded.line_start, line_end=excluded.line_end, language=excluded.language,
  signature=excluded.signature, parent_id=excluded.parent_id`)
	if err != nil {
		return 0, 0, err
	}
	defer symStmt.Close()
	edgeStmt, err := tx.PrepareContext(ctx, `
INSERT INTO edges (id, repo_id, kind, src_id, dst_id, confidence)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET confidence=excluded.confidence`)
	if err != nil {
		return 0, 0, err
	}
	defer edgeStmt.Close()

	for _, fi := range batch {
		f := fi.File
		if _, err = fileStmt.ExecContext(ctx, f.ID, f.RepoID, f.Path, f.Language, f.Size, f.Hash); err != nil {
			return 0, 0, err
		}
		for _, sym := range fi.Symbols {
			if _, err = symStmt.ExecContext(ctx, sym.ID, sym.RepoID, sym.Name, string(sym.Kind), sym.Path,
				sym.LineStart, sym.LineEnd, sym.Language, sym.Signature, nullStr(sym.ParentID)); err != nil {
				return 0, 0, err
			}
			symCount++
		}
		for _, e := range fi.Edges {
			if _, err = edgeStmt.ExecContext(ctx, e.ID, e.RepoID, string(e.Kind), e.SourceID, e.TargetID, e.Confidence); err != nil {
				return 0, 0, err
			}
			edgeCount++
		}
	}
	err = tx.Commit()
	return symCount, edgeCount, err
}

// UpsertProcess saves a process trace.
func (s *Store) UpsertProcess(ctx context.Context, p types.Process) error {
	steps, _ := json.Marshal(p.StepSymbols)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO processes (id, repo_id, name, entry_symbol, steps_json)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET name=excluded.name, entry_symbol=excluded.entry_symbol, steps_json=excluded.steps_json`,
		p.ID, p.RepoID, p.Name, p.EntrySymbol, string(steps))
	return err
}

// UpsertCluster saves cluster members.
func (s *Store) UpsertCluster(ctx context.Context, c types.Cluster) error {
	mem, _ := json.Marshal(c.Members)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO clusters (id, repo_id, name, cohesion, members_json)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET name=excluded.name, cohesion=excluded.cohesion, members_json=excluded.members_json`,
		c.ID, c.RepoID, c.Name, c.Cohesion, string(mem))
	return err
}

// DeleteClustersByNames removes clusters for repo and provided directory names.
func (s *Store) DeleteClustersByNames(ctx context.Context, repoID string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	ph := placeholders(len(names))
	args := []interface{}{repoID}
	for _, n := range names {
		args = append(args, n)
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM clusters WHERE repo_id=? AND name IN (`+ph+`)`, args...)
	return err
}

// Counts returns symbol and edge counts for repo.
func (s *Store) Counts(ctx context.Context, repoID string) (symbols, edges, files int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM symbols WHERE repo_id=?`, repoID).Scan(&symbols)
	if err != nil {
		return
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM edges WHERE repo_id=?`, repoID).Scan(&edges)
	if err != nil {
		return
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE repo_id=?`, repoID).Scan(&files)
	return
}

// LanguageIndexHealth returns how many symbols exist for language and how many
// call / import edges leave those symbols. Used by doctor to WARN when the
// primary language indexed zero symbols, is contains-only, or has a sparse
// call graph (inventory-only usefulness).
func (s *Store) LanguageIndexHealth(ctx context.Context, repoID, language string) (symbols, callEdges, importEdges int, err error) {
	language = strings.TrimSpace(language)
	if language == "" {
		return 0, 0, 0, nil
	}
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM symbols WHERE repo_id=? AND language=?`, repoID, language).Scan(&symbols)
	if err != nil {
		return
	}
	err = s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM edges e
JOIN symbols s ON s.id = e.src_id
WHERE e.repo_id=? AND e.kind=? AND s.language=?`,
		repoID, string(types.RefKindCalls), language).Scan(&callEdges)
	if err != nil {
		return
	}
	err = s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM edges e
JOIN symbols s ON s.id = e.src_id
WHERE e.repo_id=? AND e.kind=? AND s.language=?`,
		repoID, string(types.RefKindImports), language).Scan(&importEdges)
	return
}

// SymbolsByIDs loads symbols for the given ids (order not preserved).
func (s *Store) SymbolsByIDs(ctx context.Context, repoID string, ids []string) ([]types.Symbol, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph := placeholders(len(ids))
	args := []interface{}{repoID}
	for _, id := range ids {
		args = append(args, id)
	}
	q := `SELECT id, repo_id, name, kind, path, line_start, line_end, language, COALESCE(signature,''), COALESCE(parent_id,'')
FROM symbols WHERE repo_id=? AND id IN (` + ph + `)`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// SymbolByID loads one symbol.
func (s *Store) SymbolByID(ctx context.Context, repoID, id string) (*types.Symbol, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, repo_id, name, kind, path, line_start, line_end, language, COALESCE(signature,''), COALESCE(parent_id,'')
FROM symbols WHERE repo_id=? AND id=?`, repoID, id)
	var sym types.Symbol
	var kind string
	if err := row.Scan(&sym.ID, &sym.RepoID, &sym.Name, &kind, &sym.Path, &sym.LineStart, &sym.LineEnd, &sym.Language, &sym.Signature, &sym.ParentID); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	sym.Kind = types.SymbolKind(kind)
	return &sym, nil
}

// SymbolsByName finds symbols matching name (prefix or contains). Empty name returns nil slice.
func (s *Store) SymbolsByName(ctx context.Context, repoID, name string, limit int) ([]types.Symbol, error) {
	if name == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	// Rank so a bare name resolves to the symbol a human means: an EXACT match first,
	// then NON-TEST over test (so "projectBrief" beats "TestProjectBrief"), then the
	// shortest name (closest). Without this, callers that take the top result
	// (context/impact/test_impact/trace via SymbolsByName(...,1)[0]) picked an
	// arbitrary substring match — often a test whose name contains the query.
	rows, err := s.db.QueryContext(ctx, `
SELECT id, repo_id, name, kind, path, line_start, line_end, language, COALESCE(signature,''), COALESCE(parent_id,'')
FROM symbols WHERE repo_id=? AND (name LIKE ? OR name LIKE ?)
ORDER BY
  CASE WHEN name = ? THEN 0 ELSE 1 END,
  CASE WHEN path LIKE '%_test.%' OR name LIKE 'Test%' THEN 1 ELSE 0 END,
  length(name)
LIMIT ?`, repoID, name+"%", "%"+name+"%", name, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

func scanSymbols(rows *sql.Rows) ([]types.Symbol, error) {
	var out []types.Symbol
	for rows.Next() {
		var sym types.Symbol
		var kind string
		if err := rows.Scan(&sym.ID, &sym.RepoID, &sym.Name, &kind, &sym.Path, &sym.LineStart, &sym.LineEnd, &sym.Language, &sym.Signature, &sym.ParentID); err != nil {
			return nil, err
		}
		sym.Kind = types.SymbolKind(kind)
		out = append(out, sym)
	}
	return out, rows.Err()
}

// EdgesFrom returns outgoing edges from src_id.
func (s *Store) EdgesFrom(ctx context.Context, repoID, srcID string, kinds ...string) ([]types.Reference, error) {
	q := `SELECT id, repo_id, kind, src_id, dst_id, confidence FROM edges WHERE repo_id=? AND src_id=?`
	args := []interface{}{repoID, srcID}
	if len(kinds) > 0 {
		q += ` AND kind IN (` + placeholders(len(kinds)) + `)`
		for _, k := range kinds {
			args = append(args, k)
		}
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEdges(rows)
}

// EdgesTo returns incoming edges to dst_id.
func (s *Store) EdgesTo(ctx context.Context, repoID, dstID string, kinds ...string) ([]types.Reference, error) {
	q := `SELECT id, repo_id, kind, src_id, dst_id, confidence FROM edges WHERE repo_id=? AND dst_id=?`
	args := []interface{}{repoID, dstID}
	if len(kinds) > 0 {
		q += ` AND kind IN (` + placeholders(len(kinds)) + `)`
		for _, k := range kinds {
			args = append(args, k)
		}
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEdges(rows)
}

// InDegrees returns, for the whole repo in a single grouped scan, how many
// inbound edges target each symbol (keyed by dst_id). With no kinds it counts
// all edge kinds; pass "calls" for call-graph centrality (how load-bearing a
// symbol is). Symbols with no inbound edge are simply absent from the map.
// This is the bulk form of EdgesTo — one query for ranking, not one per hit.
// InDegreesFor returns inbound "calls" counts for ONLY the given symbol IDs.
// Loading the whole repo's in-degree map (InDegrees) is O(all edges) — ~430ms on
// a repo with ~1M edges, run on every query. Since centrality only needs the
// degrees of the candidate symbols (a few hundred), this bounds the cost to the
// candidate set and keeps ranking fast at scale.
func (s *Store) InDegreesFor(ctx context.Context, repoID, kind string, ids []string) (map[string]int, error) {
	out := map[string]int{}
	if len(ids) == 0 {
		return out, nil
	}
	const chunk = 500 // stay well under SQLite's variable limit
	for start := 0; start < len(ids); start += chunk {
		end := start + chunk
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		args := make([]interface{}, 0, len(batch)+2)
		args = append(args, repoID, kind)
		q := `SELECT dst_id, COUNT(*) FROM edges WHERE repo_id=? AND kind=? AND dst_id IN (` + placeholders(len(batch)) + `) GROUP BY dst_id`
		for _, id := range batch {
			args = append(args, id)
		}
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			var n int
			if err := rows.Scan(&id, &n); err != nil {
				rows.Close()
				return nil, err
			}
			if id != "" {
				out[id] = n
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) InDegrees(ctx context.Context, repoID string, kinds ...string) (map[string]int, error) {
	q := `SELECT dst_id, COUNT(*) FROM edges WHERE repo_id=?`
	args := []interface{}{repoID}
	if len(kinds) > 0 {
		q += ` AND kind IN (` + placeholders(len(kinds)) + `)`
		for _, k := range kinds {
			args = append(args, k)
		}
	}
	q += ` GROUP BY dst_id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		if id != "" {
			out[id] = n
		}
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ",")
}

func scanEdges(rows *sql.Rows) ([]types.Reference, error) {
	var out []types.Reference
	for rows.Next() {
		var e types.Reference
		var k string
		if err := rows.Scan(&e.ID, &e.RepoID, &k, &e.SourceID, &e.TargetID, &e.Confidence); err != nil {
			return nil, err
		}
		e.Kind = types.ReferenceKind(k)
		out = append(out, e)
	}
	return out, rows.Err()
}

// AllSymbolPaths returns distinct paths for repo (for file walking diff).
func (s *Store) AllSymbolPaths(ctx context.Context, repoID string) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT path FROM symbols WHERE repo_id=?`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]struct{}{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		m[p] = struct{}{}
	}
	return m, rows.Err()
}

// SymbolNameRow is a lightweight projection of a symbol used for whole-repo
// scans (e.g. vocabulary extraction) where the full row is unnecessary.
type SymbolNameRow struct {
	Name      string
	Kind      string
	Language  string
	Signature string
}

// AllSymbolNames returns the name/kind/language/signature of every symbol in the
// repo. It selects only the columns a frequency scan needs, so it stays cheap at
// the 100k-symbol scale where loading full rows would be wasteful.
func (s *Store) AllSymbolNames(ctx context.Context, repoID string) ([]SymbolNameRow, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT name, kind, COALESCE(language,''), COALESCE(signature,'')
FROM symbols WHERE repo_id=?`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SymbolNameRow
	for rows.Next() {
		var r SymbolNameRow
		if err := rows.Scan(&r.Name, &r.Kind, &r.Language, &r.Signature); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SymbolsForPath returns symbols in a single file.
func (s *Store) SymbolsForPath(ctx context.Context, repoID, path string) ([]types.Symbol, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, repo_id, name, kind, path, line_start, line_end, language, COALESCE(signature,''), COALESCE(parent_id,'')
FROM symbols WHERE repo_id=? AND path=? ORDER BY line_start`, repoID, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// SymbolsByPathPrefix returns symbols whose path starts with prefix (a package
// or directory), ordered by path then line — the basis for an API-surface view.
func (s *Store) SymbolsByPathPrefix(ctx context.Context, repoID, prefix string, limit int) ([]types.Symbol, error) {
	if limit <= 0 {
		limit = 2000
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, repo_id, name, kind, path, line_start, line_end, language, COALESCE(signature,''), COALESCE(parent_id,'')
FROM symbols WHERE repo_id=? AND path LIKE ? ORDER BY path, line_start LIMIT ?`,
		repoID, prefix+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// MethodNamesByReceiver returns, for the whole repo, the set of method names
// defined on each receiver type (parent_id) — the raw material for a heuristic
// interface→implementation match without go/types.
func (s *Store) MethodNamesByReceiver(ctx context.Context, repoID string) (map[string]map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT COALESCE(parent_id,''), name FROM symbols
WHERE repo_id=? AND kind='method' AND COALESCE(parent_id,'') != ''`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]struct{}{}
	for rows.Next() {
		var recv, name string
		if err := rows.Scan(&recv, &name); err != nil {
			return nil, err
		}
		if out[recv] == nil {
			out[recv] = map[string]struct{}{}
		}
		out[recv][name] = struct{}{}
	}
	return out, rows.Err()
}

// SearchSymbolsPath matches path substring for query tool.
func (s *Store) SearchSymbolsPath(ctx context.Context, repoID, substr string, limit int) ([]types.Symbol, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, repo_id, name, kind, path, line_start, line_end, language, COALESCE(signature,''), COALESCE(parent_id,'')
FROM symbols
WHERE repo_id=? AND (path LIKE ? OR name LIKE ? OR signature LIKE ?)
ORDER BY path, name, line_start, id
LIMIT ?`,
		repoID, "%"+substr+"%", "%"+substr+"%", "%"+substr+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

func (s *Store) SearchFilePaths(ctx context.Context, repoID string, terms []string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 50
	}
	var clauses []string
	var args []any
	args = append(args, repoID)
	for _, t := range terms {
		t = strings.TrimSpace(strings.ToLower(t))
		if len(t) < 3 {
			continue
		}
		clauses = append(clauses, "LOWER(path) LIKE ?")
		args = append(args, "%"+t+"%")
	}
	if len(clauses) == 0 {
		return nil, nil
	}
	args = append(args, limit)
	q := fmt.Sprintf(`
SELECT path FROM files
WHERE repo_id=? AND (%s)
ORDER BY path
LIMIT ?`, strings.Join(clauses, " OR "))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) ListFilesBySuffix(ctx context.Context, repoID string, suffixes []string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 50
	}
	var clauses []string
	var args []any
	args = append(args, repoID)
	for _, sfx := range suffixes {
		sfx = strings.TrimSpace(strings.ToLower(sfx))
		if sfx == "" {
			continue
		}
		clauses = append(clauses, "LOWER(path) LIKE ?")
		args = append(args, "%"+sfx)
	}
	if len(clauses) == 0 {
		return nil, nil
	}
	args = append(args, limit)
	q := fmt.Sprintf(`
SELECT path FROM files
WHERE repo_id=? AND (%s)
ORDER BY path
LIMIT ?`, strings.Join(clauses, " OR "))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RebuildSymbolFTS repopulates the trigram full-text index for one repo from the
// symbols table. Called once after ingest. Repo-scoped so re-analyzing one repo
// doesn't disturb others' FTS rows. Best-effort: a failure (e.g. an old SQLite
// without the trigram tokenizer) is non-fatal — retrieval falls back to LIKE.
func (s *Store) RebuildSymbolFTS(ctx context.Context, repoID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM symbols_fts WHERE repo_id=?`, repoID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO symbols_fts(name, path, signature, sym_id, repo_id)
SELECT name, path, COALESCE(signature,''), id, repo_id FROM symbols WHERE repo_id=?`, repoID); err != nil {
		return err
	}
	return tx.Commit()
}

// HasFTSRows reports (cheaply) whether the trigram index has any rows for a repo,
// so retrieval can choose the FTS fast path vs the LIKE fallback for an index
// built before the FTS table existed.
func (s *Store) HasFTSRows(ctx context.Context, repoID string) bool {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM symbols_fts WHERE repo_id=? LIMIT 1`, repoID).Scan(&one)
	return err == nil && one == 1
}

// SearchSymbolsFTS unions per-term matches via the trigram FTS index — indexed
// substring search that scales to 100k+ symbols. Terms shorter than 3 chars are
// dropped (the trigram tokenizer needs >=3). Returns (nil, nil) when the FTS
// table is empty for this repo so the caller can fall back to LIKE.
func (s *Store) SearchSymbolsFTS(ctx context.Context, repoID string, terms []string, limit int) ([]types.Symbol, error) {
	if limit <= 0 {
		limit = 1000
	}
	var quoted []string
	for _, t := range terms {
		t = strings.TrimSpace(strings.ToLower(t))
		if len(t) < 3 {
			continue
		}
		// Escape embedded quotes; wrap in quotes so FTS treats it as a literal
		// (trigram) string rather than parsing operators inside identifiers.
		quoted = append(quoted, `"`+strings.ReplaceAll(t, `"`, `""`)+`"`)
	}
	if len(quoted) == 0 {
		return nil, nil
	}
	match := strings.Join(quoted, " OR ")
	// ORDER BY rank (FTS5 bm25, best first) so when the match set exceeds the limit
	// the most relevant candidates survive — otherwise a common term could fill the
	// cap with rowid-early rows and drop the real target before our ranker sees it.
	rows, err := s.db.QueryContext(ctx, `
SELECT s.id, s.repo_id, s.name, s.kind, s.path, s.line_start, s.line_end, s.language, COALESCE(s.signature,''), COALESCE(s.parent_id,'')
FROM symbols_fts f JOIN symbols s ON s.id = f.sym_id
WHERE f.repo_id=? AND symbols_fts MATCH ?
ORDER BY f.rank
LIMIT ?`, repoID, match, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSymbols(rows)
}

// ListProcesses returns all processes for repo.
func (s *Store) ListProcesses(ctx context.Context, repoID string) ([]types.Process, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, repo_id, name, COALESCE(entry_symbol,''), steps_json FROM processes WHERE repo_id=?`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Process
	for rows.Next() {
		var p types.Process
		var steps string
		if err := rows.Scan(&p.ID, &p.RepoID, &p.Name, &p.EntrySymbol, &steps); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(steps), &p.StepSymbols)
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListClusters returns all clusters for repo.
func (s *Store) ListClusters(ctx context.Context, repoID string) ([]types.Cluster, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, repo_id, name, cohesion, members_json FROM clusters WHERE repo_id=?`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Cluster
	for rows.Next() {
		var c types.Cluster
		var members string
		if err := rows.Scan(&c.ID, &c.RepoID, &c.Name, &c.Cohesion, &members); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(members), &c.Members)
		out = append(out, c)
	}
	return out, rows.Err()
}

// RenameSymbol updates symbol name and all edge ids that reference... simplified: update symbols.name only
func (s *Store) RenameSymbol(ctx context.Context, repoID, symID, newName string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE symbols SET name=? WHERE repo_id=? AND id=?`, newName, repoID, symID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("symbol not found")
	}
	return nil
}
