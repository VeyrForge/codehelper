package orchestrator

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/paths"
	_ "modernc.org/sqlite"
)

const initSQL = `
CREATE TABLE IF NOT EXISTS ch_runs (
    id TEXT PRIMARY KEY,
    created_at TEXT NOT NULL,
    task TEXT NOT NULL,
    workflow TEXT NOT NULL,
    status TEXT NOT NULL,
    confidence REAL NOT NULL,
    final_answer TEXT NOT NULL,
    constraints_json TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS ch_tool_calls (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    step_index INTEGER NOT NULL,
    tool_name TEXT NOT NULL,
    args_json TEXT NOT NULL,
    result_summary TEXT NOT NULL,
    result_hash TEXT NOT NULL,
    duration_ms INTEGER NOT NULL,
    why TEXT NOT NULL DEFAULT '',
    error TEXT,
    FOREIGN KEY(run_id) REFERENCES ch_runs(id)
);

CREATE TABLE IF NOT EXISTS ch_feedback (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    feedback_text TEXT NOT NULL,
    correction_type TEXT NOT NULL,
    accepted INTEGER NOT NULL DEFAULT 0,
    preferred_entities TEXT NOT NULL DEFAULT '[]',
    avoid_entities TEXT NOT NULL DEFAULT '[]',
    FOREIGN KEY(run_id) REFERENCES ch_runs(id)
);

CREATE TABLE IF NOT EXISTS ch_memory (
    id TEXT PRIMARY KEY,
    created_at TEXT NOT NULL,
    memory_type TEXT NOT NULL,
    rule TEXT NOT NULL,
    source_run TEXT NOT NULL DEFAULT '',
    weight REAL NOT NULL DEFAULT 0.5,
    scope TEXT NOT NULL DEFAULT 'this_project'
);

CREATE INDEX IF NOT EXISTS idx_ch_tool_calls_run ON ch_tool_calls(run_id);
CREATE INDEX IF NOT EXISTS idx_ch_feedback_run ON ch_feedback(run_id);
CREATE INDEX IF NOT EXISTS idx_ch_memory_type ON ch_memory(memory_type);
`

// Store persists orchestration runs, tool traces, feedback, and memory.
type Store struct {
	db *sql.DB
}

// OpenStore opens the orchestration sqlite database for repoRoot.
func OpenStore(repoRoot string) (*Store, error) {
	dir := paths.RepoIndexDir(repoRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dir, "orchestration.db")
	dsn := "file:" + dbPath + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)" +
		"&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	s := &Store{db: db}
	if _, err := s.db.ExecContext(context.Background(), initSQL); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database connection.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// RunRecord is a persisted orchestration run.
type RunRecord struct {
	ID              string    `json:"id"`
	CreatedAt       time.Time `json:"created_at"`
	Task            string    `json:"task"`
	Workflow        string    `json:"workflow"`
	Status          string    `json:"status"`
	Confidence      float64   `json:"confidence"`
	FinalAnswer     string    `json:"final_answer,omitempty"`
	ConstraintsJSON string    `json:"constraints_json,omitempty"`
}

// ToolCallRecord is one traced tool invocation.
type ToolCallRecord struct {
	ID            string `json:"id"`
	RunID         string `json:"run_id"`
	StepIndex     int    `json:"step_index"`
	ToolName      string `json:"tool_name"`
	ArgsJSON      string `json:"args_json"`
	ResultSummary string `json:"result_summary"`
	ResultHash    string `json:"result_hash"`
	DurationMS    int64  `json:"duration_ms"`
	Why           string `json:"why"`
	Error         string `json:"error,omitempty"`
}

// FeedbackRecord is agent/user correction for a run.
type FeedbackRecord struct {
	ID                string    `json:"id"`
	RunID             string    `json:"run_id"`
	CreatedAt         time.Time `json:"created_at"`
	FeedbackText      string    `json:"feedback_text"`
	CorrectionType    string    `json:"correction_type"`
	Accepted          bool      `json:"accepted"`
	PreferredEntities []string  `json:"preferred_entities,omitempty"`
	AvoidEntities     []string  `json:"avoid_entities,omitempty"`
}

// MemoryRecord is a learned orchestration rule.
type MemoryRecord struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	MemoryType string    `json:"memory_type"`
	Rule       string    `json:"rule"`
	SourceRun  string    `json:"source_run,omitempty"`
	Weight     float64   `json:"weight"`
	Scope      string    `json:"scope"`
}

func (s *Store) InsertRun(ctx context.Context, rec RunRecord) error {
	if rec.ConstraintsJSON == "" {
		rec.ConstraintsJSON = "{}"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ch_runs (id, created_at, task, workflow, status, confidence, final_answer, constraints_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.CreatedAt.UTC().Format(time.RFC3339), rec.Task, rec.Workflow, rec.Status,
		rec.Confidence, rec.FinalAnswer, rec.ConstraintsJSON)
	return err
}

func (s *Store) InsertToolCall(ctx context.Context, rec ToolCallRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ch_tool_calls (id, run_id, step_index, tool_name, args_json, result_summary, result_hash, duration_ms, why, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.RunID, rec.StepIndex, rec.ToolName, rec.ArgsJSON, rec.ResultSummary,
		rec.ResultHash, rec.DurationMS, rec.Why, nullIfEmpty(rec.Error))
	return err
}

func (s *Store) InsertFeedback(ctx context.Context, rec FeedbackRecord) error {
	pref, _ := json.Marshal(rec.PreferredEntities)
	avoid, _ := json.Marshal(rec.AvoidEntities)
	accepted := 0
	if rec.Accepted {
		accepted = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ch_feedback (id, run_id, created_at, feedback_text, correction_type, accepted, preferred_entities, avoid_entities)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.RunID, rec.CreatedAt.UTC().Format(time.RFC3339), rec.FeedbackText,
		rec.CorrectionType, accepted, string(pref), string(avoid))
	return err
}

func (s *Store) InsertMemory(ctx context.Context, rec MemoryRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ch_memory (id, created_at, memory_type, rule, source_run, weight, scope)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.CreatedAt.UTC().Format(time.RFC3339), rec.MemoryType, rec.Rule,
		rec.SourceRun, rec.Weight, rec.Scope)
	return err
}

func (s *Store) GetRun(ctx context.Context, runID string) (*RunRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, created_at, task, workflow, status, confidence, final_answer, constraints_json
		FROM ch_runs WHERE id = ?`, runID)
	var rec RunRecord
	var created string
	if err := row.Scan(&rec.ID, &created, &rec.Task, &rec.Workflow, &rec.Status,
		&rec.Confidence, &rec.FinalAnswer, &rec.ConstraintsJSON); err != nil {
		return nil, err
	}
	rec.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return &rec, nil
}

func (s *Store) ListToolCalls(ctx context.Context, runID string) ([]ToolCallRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, run_id, step_index, tool_name, args_json, result_summary, result_hash, duration_ms, why, COALESCE(error,'')
		FROM ch_tool_calls WHERE run_id = ? ORDER BY step_index`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ToolCallRecord
	for rows.Next() {
		var rec ToolCallRecord
		if err := rows.Scan(&rec.ID, &rec.RunID, &rec.StepIndex, &rec.ToolName, &rec.ArgsJSON,
			&rec.ResultSummary, &rec.ResultHash, &rec.DurationMS, &rec.Why, &rec.Error); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *Store) ListFeedback(ctx context.Context, runID string) ([]FeedbackRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, run_id, created_at, feedback_text, correction_type, accepted, preferred_entities, avoid_entities
		FROM ch_feedback WHERE run_id = ? ORDER BY created_at`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FeedbackRecord
	for rows.Next() {
		var rec FeedbackRecord
		var created, pref, avoid string
		var accepted int
		if err := rows.Scan(&rec.ID, &rec.RunID, &created, &rec.FeedbackText, &rec.CorrectionType,
			&accepted, &pref, &avoid); err != nil {
			return nil, err
		}
		rec.CreatedAt, _ = time.Parse(time.RFC3339, created)
		rec.Accepted = accepted == 1
		_ = json.Unmarshal([]byte(pref), &rec.PreferredEntities)
		_ = json.Unmarshal([]byte(avoid), &rec.AvoidEntities)
		out = append(out, rec)
	}
	return out, rows.Err()
}

// SearchMemory returns orchestration memory entries matching query tokens.
func (s *Store) SearchMemory(ctx context.Context, query string, limit int) ([]MemoryRecord, error) {
	if limit <= 0 {
		limit = 8
	}
	toks := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, created_at, memory_type, rule, source_run, weight, scope
		FROM ch_memory ORDER BY created_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type scored struct {
		rec   MemoryRecord
		score float64
	}
	var hits []scored
	for rows.Next() {
		var rec MemoryRecord
		var created string
		if err := rows.Scan(&rec.ID, &created, &rec.MemoryType, &rec.Rule, &rec.SourceRun, &rec.Weight, &rec.Scope); err != nil {
			return nil, err
		}
		rec.CreatedAt, _ = time.Parse(time.RFC3339, created)
		sc := scoreMemory(rec.Rule+" "+rec.MemoryType, toks)
		if sc > 0 || len(toks) == 0 {
			hits = append(hits, scored{rec: rec, score: sc})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Simple sort by score then weight.
	for i := 0; i < len(hits); i++ {
		for j := i + 1; j < len(hits); j++ {
			if hits[j].score > hits[i].score || (hits[j].score == hits[i].score && hits[j].rec.Weight > hits[i].rec.Weight) {
				hits[i], hits[j] = hits[j], hits[i]
			}
		}
	}
	out := make([]MemoryRecord, 0, limit)
	for i := 0; i < len(hits) && len(out) < limit; i++ {
		out = append(out, hits[i].rec)
	}
	return out, nil
}

func scoreMemory(text string, toks []string) float64 {
	if len(toks) == 0 {
		return 1
	}
	lt := strings.ToLower(text)
	hits := 0
	for _, t := range toks {
		if len(t) < 2 {
			continue
		}
		if strings.Contains(lt, t) {
			hits++
		}
	}
	return float64(hits) / float64(len(toks))
}

func nullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// HashResult returns a short hash of tool output for dedup/debug.
func HashResult(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

// NewRunID generates a run id like chrun_20260701_221500_a91c.
func NewRunID() string {
	now := time.Now().UTC()
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d", now.UnixNano())))
	return fmt.Sprintf("chrun_%s_%s", now.Format("20060102_150405"), hex.EncodeToString(sum[:3]))
}
