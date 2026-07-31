package graph

// SchemaVersion for sqlite migrations.
const SchemaVersion = 1

// InitSQL creates tables for property-graph style storage.
const InitSQL = `
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;

CREATE TABLE IF NOT EXISTS meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS files (
  id TEXT PRIMARY KEY,
  repo_id TEXT NOT NULL,
  path TEXT NOT NULL,
  language TEXT,
  size_bytes INTEGER DEFAULT 0,
  hash TEXT,
  UNIQUE(repo_id, path)
);

CREATE TABLE IF NOT EXISTS symbols (
  id TEXT PRIMARY KEY,
  repo_id TEXT NOT NULL,
  name TEXT NOT NULL,
  kind TEXT NOT NULL,
  path TEXT NOT NULL,
  line_start INTEGER NOT NULL,
  line_end INTEGER NOT NULL,
  language TEXT,
  signature TEXT,
  parent_id TEXT,
  body_hash TEXT
);
CREATE INDEX IF NOT EXISTS idx_symbols_repo_path ON symbols(repo_id, path);
CREATE INDEX IF NOT EXISTS idx_symbols_repo_name ON symbols(repo_id, name);

-- Full-text index for candidate generation. The trigram tokenizer makes MATCH do
-- indexed SUBSTRING search (LIKE '%x%' semantics, incl. inside camelCase/snake_case
-- identifiers) in O(log n) instead of scanning every row — the difference between
-- ~3s and ~tens-of-ms on a 100k+ symbol repo. Populated per-repo after ingest;
-- queries fall back to LIKE when this is empty (e.g. an index built before it existed).
CREATE VIRTUAL TABLE IF NOT EXISTS symbols_fts USING fts5(
  name, path, signature,
  sym_id UNINDEXED, repo_id UNINDEXED,
  tokenize='trigram'
);

CREATE TABLE IF NOT EXISTS edges (
  id TEXT PRIMARY KEY,
  repo_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  src_id TEXT NOT NULL,
  dst_id TEXT NOT NULL,
  confidence REAL DEFAULT 1.0
);
CREATE INDEX IF NOT EXISTS idx_edges_src ON edges(repo_id, src_id);
CREATE INDEX IF NOT EXISTS idx_edges_dst ON edges(repo_id, dst_id);
CREATE INDEX IF NOT EXISTS idx_edges_kind ON edges(repo_id, kind);

CREATE TABLE IF NOT EXISTS processes (
  id TEXT PRIMARY KEY,
  repo_id TEXT NOT NULL,
  name TEXT NOT NULL,
  entry_symbol TEXT,
  steps_json TEXT
);

CREATE TABLE IF NOT EXISTS clusters (
  id TEXT PRIMARY KEY,
  repo_id TEXT NOT NULL,
  name TEXT NOT NULL,
  cohesion REAL DEFAULT 0,
  members_json TEXT
);
`
