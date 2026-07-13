// Package memory stores project-scoped learning artifacts and approved patterns.
package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/VeyrForge/codehelper/internal/paths"
)

const filename = "project_memory.json"

// FixPattern is a reusable verified fix description.
type FixPattern struct {
	ID         string    `json:"id"`
	Problem    string    `json:"problem"`
	Solution   string    `json:"solution"`
	VerifiedBy []string  `json:"verified_by,omitempty"`
	Files      []string  `json:"files,omitempty"`
	CreatedAt  time.Time `json:"created_at,omitempty"`
}

// ProjectFact is a durable fact about the repo.
type ProjectFact struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// Decision records an explicit decision (an ADR-lite record). Text is what was
// decided; Rationale is the WHY — the part later sessions need so they don't
// re-litigate or unknowingly reverse a considered choice. Tags are optional
// free-form labels (e.g. a package or feature) to aid recall. Rationale/Tags are
// omitempty so decisions written before this field existed load unchanged.
type Decision struct {
	Text      string    `json:"text"`
	Rationale string    `json:"rationale,omitempty"`
	Tags      []string  `json:"tags,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// Document is the merged memory file shape.
type Document struct {
	Decisions    []Decision    `json:"decisions"`
	FixPatterns  []FixPattern  `json:"fix_patterns"`
	ProjectFacts []ProjectFact `json:"project_facts"`
}

type Store struct {
	root string
	mu   sync.Mutex
}

func docPath(repoRoot string) string {
	return filepath.Join(paths.RepoIndexDir(repoRoot), "memory", filename)
}

// Open returns a memory store for repoRoot.
func Open(repoRoot string) *Store {
	return &Store{root: filepath.Clean(repoRoot)}
}

func (s *Store) load() (Document, error) {
	var d Document
	b, err := os.ReadFile(docPath(s.root))
	if err != nil {
		if os.IsNotExist(err) {
			return d, nil
		}
		return d, err
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return d, err
	}
	return d, nil
}

func (s *Store) save(d Document) error {
	dir := filepath.Dir(docPath(s.root))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp := docPath(s.root) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, docPath(s.root))
}

// AddDecision appends a plain decision line (no rationale).
func (s *Store) AddDecision(text string) error {
	return s.AddDecisionRecord(Decision{Text: text})
}

// AddDecisionRecord appends a full decision record (decision + rationale + tags),
// stamping CreatedAt when unset. A blank Text is a no-op. Rationale/Tags are
// trimmed; this is the write path for ADR-style "why we did it this way" memory.
func (s *Store) AddDecisionRecord(rec Decision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return err
	}
	rec.Text = strings.TrimSpace(rec.Text)
	if rec.Text == "" {
		return nil
	}
	rec.Rationale = strings.TrimSpace(rec.Rationale)
	rec.Tags = trimTags(rec.Tags)
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	d.Decisions = append(d.Decisions, rec)
	return s.save(d)
}

// trimTags cleans and drops empty tags, returning nil for an all-empty input so
// the omitempty JSON stays clean.
func trimTags(tags []string) []string {
	out := tags[:0]
	for _, t := range tags {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// AddFixPattern appends or replaces by id.
func (s *Store) AddFixPattern(p FixPattern) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return err
	}
	p.ID = strings.TrimSpace(p.ID)
	if p.ID == "" {
		return nil
	}
	var out []FixPattern
	for _, x := range d.FixPatterns {
		if x.ID != p.ID {
			out = append(out, x)
		}
	}
	p.CreatedAt = time.Now().UTC()
	out = append(out, p)
	d.FixPatterns = out
	return s.save(d)
}

// AddFact upserts by key.
func (s *Store) AddFact(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	var nf []ProjectFact
	for _, f := range d.ProjectFacts {
		if f.Key != key {
			nf = append(nf, f)
		}
	}
	nf = append(nf, ProjectFact{Key: key, Value: strings.TrimSpace(value), CreatedAt: time.Now().UTC()})
	d.ProjectFacts = nf
	return s.save(d)
}

// Facts returns all stored project facts — the approved glossary entries. The
// returned slice is safe for the caller to read; it reflects the file at call
// time.
func (s *Store) Facts() ([]ProjectFact, error) {
	d, err := s.load()
	if err != nil {
		return nil, err
	}
	return d.ProjectFacts, nil
}

// Search returns lightweight matches for query terms against patterns and facts.
func (s *Store) Search(query string, limit int) ([]RelevantMemory, error) {
	d, err := s.load()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 8
	}
	q := strings.ToLower(strings.TrimSpace(query))
	toks := strings.Fields(q)
	var out []RelevantMemory
	add := func(typ string, confidence float64, summary string) {
		if len(out) >= limit {
			return
		}
		out = append(out, RelevantMemory{Type: typ, Confidence: confidence, Summary: summary})
	}
	for _, fp := range d.FixPatterns {
		sc := scoreText(fp.Problem+" "+fp.Solution, toks)
		if sc > 0.2 {
			add("fix_pattern", sc, fp.Solution)
		}
	}
	for _, pf := range d.ProjectFacts {
		sc := scoreText(pf.Key+" "+pf.Value, toks)
		if sc > 0.15 {
			add("project_fact", sc, pf.Key+": "+pf.Value)
		}
	}
	for _, dec := range d.Decisions {
		sc := scoreText(dec.Text+" "+dec.Rationale+" "+strings.Join(dec.Tags, " "), toks)
		if sc > 0.15 {
			add("decision", sc, decisionSummary(dec))
		}
	}
	return out, nil
}

// decisionSummary renders a decision for recall: the decision plus its "why" so
// a later session sees the rationale, not just the conclusion.
func decisionSummary(dec Decision) string {
	if dec.Rationale != "" {
		return dec.Text + " — why: " + dec.Rationale
	}
	return dec.Text
}

// RelevantMemory is surfaced to MCP responses.
type RelevantMemory struct {
	Type       string  `json:"type"`
	Confidence float64 `json:"confidence"`
	Summary    string  `json:"summary"`
}

func scoreText(text string, toks []string) float64 {
	if len(toks) == 0 {
		return 0
	}
	lt := strings.ToLower(text)
	hits := 0
	for _, t := range toks {
		if len(t) < 3 {
			continue
		}
		if strings.Contains(lt, t) {
			hits++
		}
	}
	return float64(hits) / float64(len(toks))
}
