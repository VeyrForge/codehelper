// Package hints is a GLOBAL, local-first, tech-scoped memory of things an agent
// learned about a stack and wants to remember next time — "don't forget X when
// working with Y". Unlike per-project memory, hints are stored once in
// ~/.codehelper/learned_hints.json and applied to ANY project whose framework,
// language, project_type, or dependency matches a hint's scope. The file is plain
// JSON so it syncs/exports/imports trivially across machines.
package hints

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/VeyrForge/codehelper/internal/paths"
)

// ScopeType identifies what a hint is keyed on.
const (
	ScopeFramework   = "framework"
	ScopeLanguage    = "language"
	ScopeDependency  = "dependency"
	ScopeProjectType = "project_type"
	ScopeGlobal      = "global"
)

var validScopeTypes = map[string]bool{
	ScopeFramework: true, ScopeLanguage: true, ScopeDependency: true,
	ScopeProjectType: true, ScopeGlobal: true,
}

// Hint is one learned rule, scoped to a technology (or global).
type Hint struct {
	ID         string    `json:"id"`
	ScopeType  string    `json:"scope_type"`            // framework|language|dependency|project_type|global
	Scope      string    `json:"scope"`                 // e.g. "wordpress","go","tailwindcss" ("" for global)
	Text       string    `json:"text"`                  // the rule/hint itself
	SourceRepo string    `json:"source_repo,omitempty"` // where it was learned (informational)
	CreatedAt  time.Time `json:"created_at"`
}

type doc struct {
	Hints []Hint `json:"hints"`
}

var mu sync.Mutex

func filePath() (string, error) {
	d, err := paths.RegistryDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "learned_hints.json"), nil
}

func load() (doc, error) {
	var d doc
	p, err := filePath()
	if err != nil {
		return d, err
	}
	b, err := os.ReadFile(p)
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

func save(d doc) error {
	p, err := filePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func idFor(scopeType, scope, text string) string {
	sum := sha1.Sum([]byte(strings.ToLower(scopeType + "\x00" + scope + "\x00" + strings.TrimSpace(text))))
	return hex.EncodeToString(sum[:])[:10]
}

// NormalizeScopeType validates/normalizes a scope type, defaulting to global.
func NormalizeScopeType(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if !validScopeTypes[s] {
		return ScopeGlobal
	}
	return s
}

// Add upserts a hint (deduped by scope_type+scope+text). Returns the stored hint.
func Add(scopeType, scope, text, sourceRepo string) (Hint, error) {
	mu.Lock()
	defer mu.Unlock()
	scopeType = NormalizeScopeType(scopeType)
	scope = strings.TrimSpace(scope)
	text = strings.TrimSpace(text)
	if scopeType == ScopeGlobal {
		scope = ""
	}
	h := Hint{ID: idFor(scopeType, scope, text), ScopeType: scopeType, Scope: scope, Text: text, SourceRepo: sourceRepo, CreatedAt: time.Now().UTC()}
	d, err := load()
	if err != nil {
		return Hint{}, err
	}
	for i := range d.Hints {
		if d.Hints[i].ID == h.ID {
			return d.Hints[i], nil // already present
		}
	}
	d.Hints = append(d.Hints, h)
	if err := save(d); err != nil {
		return Hint{}, err
	}
	return h, nil
}

// List returns hints, optionally filtered by scope type and/or scope value.
func List(scopeType, scope string) ([]Hint, error) {
	d, err := load()
	if err != nil {
		return nil, err
	}
	scopeType = strings.ToLower(strings.TrimSpace(scopeType))
	scope = strings.ToLower(strings.TrimSpace(scope))
	var out []Hint
	for _, h := range d.Hints {
		if scopeType != "" && h.ScopeType != scopeType {
			continue
		}
		if scope != "" && !strings.EqualFold(h.Scope, scope) {
			continue
		}
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ScopeType != out[j].ScopeType {
			return out[i].ScopeType < out[j].ScopeType
		}
		return out[i].Scope < out[j].Scope
	})
	return out, nil
}

// Remove deletes a hint by id. Returns whether one was removed.
func Remove(id string) (bool, error) {
	mu.Lock()
	defer mu.Unlock()
	id = strings.TrimSpace(id)
	d, err := load()
	if err != nil {
		return false, err
	}
	out := d.Hints[:0]
	removed := false
	for _, h := range d.Hints {
		if h.ID == id {
			removed = true
			continue
		}
		out = append(out, h)
	}
	if !removed {
		return false, nil
	}
	d.Hints = out
	return true, save(d)
}

// MatchingFor returns the hints that apply to a project with the given attributes
// (case-insensitive): every global hint, plus any whose scope matches the
// framework, project_type, one of the languages, or one of the dependencies.
func MatchingFor(framework, projectType string, languages, deps []string) ([]string, error) {
	d, err := load()
	if err != nil {
		return nil, err
	}
	langSet := lowerSet(languages)
	depSet := lowerSet(deps)
	var out []string
	seen := map[string]bool{}
	emit := func(text string) {
		if !seen[text] {
			out = append(out, text)
			seen[text] = true
		}
	}
	for _, h := range d.Hints {
		s := strings.ToLower(h.Scope)
		match := false
		switch h.ScopeType {
		case ScopeGlobal:
			match = true
		case ScopeFramework:
			match = s != "" && s == strings.ToLower(framework)
		case ScopeProjectType:
			match = s != "" && s == strings.ToLower(projectType)
		case ScopeLanguage:
			match = s != "" && langSet[s]
		case ScopeDependency:
			match = s != "" && depSet[s]
		}
		if match {
			emit(h.Text)
		}
	}
	return out, nil
}

// Export returns the raw JSON of all hints for transfer to another machine.
func Export() ([]byte, error) {
	d, err := load()
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(d, "", "  ")
}

// ImportMerge merges hints from exported JSON, deduped by id. Returns how many new
// hints were added.
func ImportMerge(data []byte) (int, error) {
	mu.Lock()
	defer mu.Unlock()
	var incoming doc
	if err := json.Unmarshal(data, &incoming); err != nil {
		return 0, err
	}
	cur, err := load()
	if err != nil {
		return 0, err
	}
	have := map[string]bool{}
	for _, h := range cur.Hints {
		have[h.ID] = true
	}
	added := 0
	for _, h := range incoming.Hints {
		if h.ID == "" {
			h.ID = idFor(h.ScopeType, h.Scope, h.Text)
		}
		if have[h.ID] {
			continue
		}
		cur.Hints = append(cur.Hints, h)
		have[h.ID] = true
		added++
	}
	if added == 0 {
		return 0, nil
	}
	return added, save(cur)
}

func lowerSet(in []string) map[string]bool {
	m := make(map[string]bool, len(in))
	for _, s := range in {
		m[strings.ToLower(strings.TrimSpace(s))] = true
	}
	return m
}
